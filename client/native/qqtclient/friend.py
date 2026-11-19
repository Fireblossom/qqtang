"""Friends: the lobby's friend tab, the add/answer dialogs, the message
envelope and the player-info lookup (FindFriend).

Behaviour follows QQTSection.dll's CFriendHandler. The login roster (0x00A2)
installs the friend UINs and each 0x00A5 fills in or updates one of them; an
update for a UIN that is not in the roster is ignored (0x10010EBD). A friend
coming online moves to the front of the list, one going offline to the end.
After every change the original runs the script's updatePlayer()
(0x100110A9); the lobby list itself is redrawn by ui_NotifyPlayerUpdate when
the next player list arrives.

Applications and results go into the "leave word" queue: the chat bar's
envelope glints (startGlint/endGlint, Client.exe 0x0052D1D0) and each click
on it (GetLeaveWord, QQTSection 0x1002A206) pops one message. An application
opens UI.friendDlg through ReceivedRequestAddFriend(uin); other messages are
shown in the system message box, with a "next" handle (-3) while more remain.

The server answers applications with 0x00A6, which the original client does
not dispatch (QQTSection 0x100189CE routes only 0xA2..0xA5 to the friend
handler); it is mapped here to the original's result messages.
"""

from __future__ import annotations

import dataclasses
import types
from typing import Any, Callable

from . import lobby as lobby_module
from . import pve
from .chat import CHANNEL_ITEMTEXT, LOBBY_CHAT, ROOM_CHAT
from .slash import SYSTEM_COLOR
from .net import friend
from .net.player import GameInfo
from .net import lobby as lobby_net
from .net.player import GameInfo
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

MODE_FRIENDS = 1
MIN_FIND_UIN = 10000  # FindFriend ignores smaller numbers (Client.exe 0x0048EAB9)
ATTRIBUTE_FRIEND, ATTRIBUTE_SHIELDED, ATTRIBUTE_SELF_IDENTITY = 0, 1, 2
SELF_IDENTITY_BIT = 0x2000
FRIEND_DIALOG = "UI.friendDlg"

# DoFriendTask operations (Client.exe 0x0054DCC0).
TASK_LIST, TASK_ADD, TASK_REMOVE, TASK_PURSUIT, TASK_WHISPER, TASK_SHIELD, TASK_ANSWER, TASK_UNSHIELD = range(8)
ANSWER_YES = ("y", "Y")

# Leave-word texts (QQTSection 0x10017FCA).
ACCEPTED_TEXT = "{name}[{uin}]接受了您的添加好友的请求"
REFUSED_TEXT = "{name}[{uin}]拒绝了您的添加好友的请求"
OFFLINE_TEXT = "{uin}玩家已经下线"
FIND_FAILED_TEXT = "查找好友失败, 请稍后再试"
# QQTSection 0x1005FCFC / 0x1005FD18 / 0x1005FD40.
# Slash commands 0x11 / 0x12 (QQTSection 0x10025027, texts 0x10061CD4,
# 0x10061CB4, 0x10061C90, 0x10061B64); 0x10025016 wants a uin above 10000.
MIN_SHIELD_UIN = 10000
SHIELD_BAD_UIN_TEXT = "输入的玩家帐号不正确"
SHIELD_SELF_TEXT = "拒绝和自己说悄悄话无意义"
SHIELD_TEXT = "你已经屏蔽了玩家[{uin}]的悄悄话"
UNSHIELD_TEXT = "你已经取消了对[{uin}]玩家密聊屏蔽"
PURSUIT_CLOSED_TEXT = "抱歉，好友追踪已关闭!"  # 0x10062798
# The final client answers 抱歉，好友追踪已关闭! (0x1002C68D); the user chose to
# turn the unwired original handler back on.
PURSUIT_ENABLED = True
PURSUIT_OFFLINE_TEXT = "你要追踪的玩家不在线"
PURSUIT_NO_ROOM_TEXT = "该玩家和你在同一小区,且不在房间中"
PURSUIT_IN_GAME_TEXT = "该玩家正在游戏中,请稍候再继续追踪"

# Message box types (the script's ui_msgBox) and handles used by GetLeaveWord.
MSGBOX_CLOSE = 3
MSGBOX_NEXT = 11
HANDLE_NONE = 0
HANDLE_NEXT_LEAVE_WORD = -3


@dataclasses.dataclass
class Friend:
    uin: int
    nickname: str = ""
    gender: int = 0
    identity: int = 0
    points: int = 0
    ext_item_ids: list[int] = dataclasses.field(default_factory=list)
    online: bool = False
    ext_points: int = 0


@dataclasses.dataclass
class LeaveWord:
    text: str
    application: friend.FriendApplication | None = None
    script: str = ""  # a queued script call, e.g. ShowkininviteDlg (QQTSection 0x10016084)


class FriendState:
    def __init__(self) -> None:
        self.reset()

    def reset(self) -> None:
        self.roster: list[Friend] = []
        self.maximum = 0
        self.leave_words: list[LeaveWord] = []
        self.detail: friend.FoundPlayer | None = None
        self.asked_names: dict[int, str] = {}
        self.pursuing = 0  # FIND_FRIEND sent for 好友追踪 (QQTSection +0x154)

    def find(self, uin: int) -> Friend | None:
        return next((entry for entry in self.roster if entry.uin == uin), None)

    def install(self, uins: list[int], maximum: int) -> None:
        known = {entry.uin: entry for entry in self.roster}
        self.roster = [known.get(uin) or Friend(uin) for uin in uins]
        self.maximum = maximum

    def update(self, update: friend.FriendUpdate) -> bool:
        entry = self.find(update.uin)
        if entry is None:
            return False
        entry.points, entry.ext_item_ids, entry.online = update.points, update.ext_item_ids, update.online
        # The original copies these only for an online friend and keeps the
        # rest in ./data/<uin>-Friends.dat; without that cache they are taken
        # from every update.
        entry.nickname, entry.gender, entry.identity = update.nickname, update.gender, update.identity
        entry.ext_points = update.ext_points
        self.roster.remove(entry)
        if update.online:
            self.roster.insert(0, entry)
        else:
            self.roster.append(entry)
        return True

    def valid_count(self) -> int:
        """QQTSection 0x10011154: min(maximum, current) once the list has a maximum."""
        return min(self.maximum, len(self.roster)) if self.maximum else len(self.roster)


def build_friend_natives(runtime: UIRuntime, session: Session,
                         dress_profile: Callable[[friend.FoundPlayer], None] | None = None) -> dict[str, Callable[..., Any]]:
    state = FriendState()
    lobby_count = runtime.namespace["GetPlayerCnt"]
    lobby_fetch = runtime.namespace["fetch_PlayerInfoInPlayerList"]

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def lobby_players() -> list[Any]:
        return [lobby_fetch(index, 0).m_PlayerInfo for index in range(lobby_count(0))]

    def lobby_player(uin: int) -> Any | None:
        return next((info for info in lobby_players() if info.m_dwPlayerUin == uin), None)

    def refresh() -> None:
        script("updatePlayer")

    def message_box(text: str, kind: int, handle: int) -> None:
        # Client.exe shows UI.SysMsgbox, then runs ui_msgBox(type) to set the buttons (0x0048B320).
        runtime.show_msgbox(text, "", kind, "UI.SysMsgbox", handle)
        script("ui_msgBox", kind)

    def push(word: LeaveWord) -> None:
        state.leave_words.append(word)
        script("startGlint")

    session.push_script_word = lambda code: push(LeaveWord("", script=code))
    session.push_leave_word = lambda text: push(LeaveWord(text))

    def request_list() -> None:
        session.send(friend.FRIEND_LIST_COMMAND, friend.build_friend_list(session.uin))

    def friend_info(entry: Friend) -> types.SimpleNamespace:
        """QQTSection 0x10010558: the 0x00A5 record, with the kin taken from
        the section player list."""
        game_info = GameInfo(0, 0, 0, 0, entry.points, 0, 0, 0, 0, 0, 0, 0, entry.ext_points)
        info = lobby_module.native_player(lobby_net.Player(entry.uin, entry.nickname, 0, entry.gender, 0, entry.identity,
                                                           game_info, entry.ext_item_ids, 0, "", b"\xff" * 8))
        cached = lobby_player(entry.uin)
        if cached is not None:
            info.m_dwKinIndex, info.m_nKinNameLen = cached.m_dwKinIndex, cached.m_nKinNameLen
            info.m_szKinName, info.m_stKinFlagID = cached.m_szKinName, cached.m_stKinFlagID
        return info

    def section_names(section_id: int) -> tuple[str, str, str]:
        hall = session.hall
        if hall is None:
            return "", "", ""
        for channel in hall.channels:
            for section in channel.sections:
                if section.id == section_id:
                    zone = next((location.name for location in hall.locations if location.id == section.location_id), "")
                    return channel.name, section.name, zone
        return "", "", ""

    def detail(found: friend.FoundPlayer) -> types.SimpleNamespace:
        """The struct FindFriendInfoRecv (Client.exe 0x0048EB20) keeps for
        GetPlayerDetail, filled as QQTSection 0x1001D126 does: location names
        only while the player is online; for an offline friend the name and
        gender come from the roster."""
        game = found.game_info
        online = found.status > friend.FIND_STATUS_OFFLINE
        channel, section, zone = section_names(found.section_id) if online else ("", "", "")
        name, gender = (found.nickname, found.gender) if online else ("", found.gender)
        entry = state.find(found.uin)
        if not online and entry is not None:
            name, gender = entry.nickname, entry.gender
        flag_index = int.from_bytes(found.kin_flag[4:8], "big", signed=True) if len(found.kin_flag) == 8 else -1
        return types.SimpleNamespace(
            m_dwFriendUin=found.uin, m_szPlayerName=name, m_cGender=gender, m_dwIdentity=found.identity,
            m_bStatus=found.status, m_szChannelName=channel, m_szSectionName=section, m_szZoneName=zone,
            m_szRoomName=found.room_name if online else "", m_sRoomUIID=found.room_id if online else 0,
            m_dwKinIndex=found.kin_index, m_nKinNameLen=len(found.kin_name.encode("gbk", "replace")),
            m_szKinName=found.kin_name, m_stKinFlagID=types.SimpleNamespace(m_iFlagID=flag_index), m_iHonor=found.honor,
            m_stGameInfo=types.SimpleNamespace(
                m_nWinNum=game.wins, m_nLossNum=game.losses, m_nEqualNum=game.draws, m_dwPoint=game.points,
                m_bRoleID=game.role_id, m_dwExtWinNum=game.extended_wins, m_dwExtLossNum=game.extended_losses,
                m_dwExtEqualNum=game.extended_draws, m_dwExtPoint=game.extended_points,
            ),
        )

    # Natives ----------------------------------------------------------------

    def DoFriendTask(text, _length, uin, task):
        uin, task = int(uin), int(task)
        if task == TASK_LIST:
            request_list()
        elif task == TASK_ADD:
            # RequestAddFriend (0x1000F51E) drops requests for oneself or a friend.
            if uin == session.uin or state.find(uin) is not None:
                return
            state.asked_names[uin] = GetPlayerNickNamebyUin(uin)
            session.send(friend.ADD_FRIEND_COMMAND, friend.build_add_friend(session.uin, uin, session.nickname, str(text)))
        elif task == TASK_REMOVE:
            if state.find(uin) is not None:
                session.send(friend.REMOVE_FRIEND_COMMAND, friend.build_remove_friend(session.uin, uin))
        elif task == TASK_PURSUIT:
            # XSection::DoFriendTask 0x1002C68D answers task 3 with this line
            # only: the final client switched pursuit off. Its unused
            # handler (CFriendHandler::RequestPursuit 0x1000F7BF, answers in
            # 0x10010D83) is kept below behind PURSUIT_ENABLED.
            if not PURSUIT_ENABLED:
                system_line(PURSUIT_CLOSED_TEXT)
            elif state.find(uin) is not None:
                state.pursuing = uin
                session.send(friend.FIND_FRIEND_COMMAND, friend.build_find_friend(session.uin, uin))
        elif task in (TASK_SHIELD, TASK_UNSHIELD):
            # SC_DoFriendTask sends "/m <uin>" or "/u <uin>" through the
            # slash commands 0x11 / 0x12 (QQTSection 0x10025027).
            shield(uin, task == TASK_SHIELD)
        elif task == TASK_ANSWER:
            # The original refuses with result 0x33; the server only accepts 0 or 1.
            result = friend.RESULT_SUCCESS if str(text) in ANSWER_YES else friend.RESULT_REJECTED
            session.send(friend.ANSWER_FRIEND_COMMAND, friend.build_answer_friend(result, uin, session.uin))
        else:
            log(f"[friend] unsupported friend task {task} for {uin}")

    def GetValidFriendsCount():
        return state.valid_count()

    def GetAttribute(uin, kind):
        kind = int(kind)
        if kind == ATTRIBUTE_FRIEND:
            return int(state.find(int(uin)) is not None)
        if kind == ATTRIBUTE_SHIELDED:
            return int(int(uin) in session.shielded)  # 0x1002CA01
        if kind == ATTRIBUTE_SELF_IDENTITY:
            return int(bool(session.profile and session.profile.identity & SELF_IDENTITY_BIT))
        return 0

    def GetPlayerCnt(mode=0):
        return len(state.roster) if int(mode) == MODE_FRIENDS else lobby_count(mode)

    def fetch_PlayerInfoInPlayerList(index, mode=0):
        if int(mode) != MODE_FRIENDS:
            return lobby_fetch(index, mode)
        if not 0 <= int(index) < len(state.roster):
            return types.SimpleNamespace(m_nPlayerStatus=0, m_PlayerInfo=types.SimpleNamespace(m_dwPlayerUin=0))
        entry = state.roster[int(index)]
        return types.SimpleNamespace(m_nPlayerStatus=int(entry.online), m_PlayerInfo=friend_info(entry))

    def getExtItem(index, slot, mode=0):
        if int(mode) == MODE_FRIENDS:
            items = state.roster[int(index)].ext_item_ids if 0 <= int(index) < len(state.roster) else []
        else:
            items = lobby_fetch(index, mode).m_PlayerInfo.m_dwExtItemID if 0 <= int(index) < lobby_count(mode) else []
        return items[int(slot)] if 0 <= int(slot) < len(items) else 0

    def GetPlayerNickNamebyUin(uin, _kind=0):
        """QQTSection 0x1002D7DF: the section player list, else the number."""
        cached = lobby_player(int(uin))
        return cached.m_szPlayerNickname if cached else str(int(uin))

    def GetPlayerInfoByUin(uin):
        cached = lobby_player(int(uin))
        return cached if cached else types.SimpleNamespace(m_dwPlayerUin=0, m_szPlayerNickname="", m_bGender=0)

    def FindFriend(uin):
        if int(uin) >= MIN_FIND_UIN:
            session.send(friend.FIND_FRIEND_COMMAND, friend.build_find_friend(session.uin, int(uin)))

    def GetPlayerDetail():
        # Client.exe keeps a zero-filled struct until a query answers; our
        # own marriage notice can ask for it first (ui_UpdateMyMarriageInfo).
        if state.detail is None:
            return detail(friend.FoundPlayer(0, 0, game_info=GameInfo(*([0] * len(dataclasses.fields(GameInfo))))))
        return state.detail

    def GetPVEData(points=0):
        # tagPVEDATA from config/pvedata.dat (uiSelRoom.py's info panel).
        level = pve.level_for(int(points))
        if level is None:
            return types.SimpleNamespace(m_unLevel=0, m_unHp=0, m_unBombNum=0, m_fBombReSpeed=0.0, m_unBombPow=0, m_unSpeed=0)
        return types.SimpleNamespace(m_unLevel=level.level, m_unHp=level.hp, m_unBombNum=level.bomb_num,
                                     m_fBombReSpeed=level.re_speed, m_unBombPow=level.bomb_pow, m_unSpeed=level.speed)

    def GetLeaveWordCount():
        return len(state.leave_words)

    def GetLeaveWord():
        if not state.leave_words:
            return
        word = state.leave_words.pop(0)
        remaining = len(state.leave_words)
        if word.script:
            script(word.script)
        elif word.application is not None:
            runtime.namespace["Win_SetText"](FRIEND_DIALOG + ".name", word.application.nickname)
            runtime.namespace["Win_SetText"](FRIEND_DIALOG + ".text", word.application.text)
            script("ReceivedRequestAddFriend", word.application.requester)
        elif remaining:
            message_box(word.text, MSGBOX_NEXT, HANDLE_NEXT_LEAVE_WORD)
        else:
            message_box(word.text, MSGBOX_CLOSE, HANDLE_NONE)
        script("startGlint" if remaining else "endGlint")

    # Server messages --------------------------------------------------------

    def on_friend_list(packet) -> None:
        roster = friend.parse_friend_list(packet.payload)
        if roster.result != friend.RESULT_SUCCESS:
            log(f"[friend] friend list rejected: {roster.result}")
            return
        state.install(roster.friends, roster.maximum)
        log(f"[friend] roster {roster.friends} (maximum {roster.maximum})")
        refresh()

    def on_update(packet) -> None:
        update = friend.parse_friend_update(packet.payload)
        log(f"[friend] update {update.uin} {update.nickname!r} online={update.online}")
        if state.update(update):
            refresh()

    def on_application(packet) -> None:
        application = friend.parse_application(packet.payload)
        log(f"[friend] application from {application.requester} ({application.nickname}): {application.text!r}")
        if getattr(session, "dont_add_me", False):
            return  # /DONTADDME: FUN_1000FAA9 drops it silently
        push(LeaveWord("", application))

    def on_remove(packet) -> None:
        result, uin = friend.parse_remove_result(packet.payload)
        entry = state.find(uin)
        if result == friend.RESULT_SUCCESS and entry is not None:
            state.roster.remove(entry)
            refresh()

    def on_answer(packet) -> None:
        answer = friend.parse_answer(packet.payload)
        log(f"[friend] answer result={answer.result} requester={answer.requester} responder={answer.responder} "
            f"notification={packet.inner_sequence == 0}")
        if answer.responder == session.uin:
            if answer.result == friend.RESULT_SUCCESS:
                request_list()
            return
        name = state.asked_names.pop(answer.responder, None) or GetPlayerNickNamebyUin(answer.responder)
        if packet.inner_sequence != 0:
            # Reply to our own application: the server rejects it when the target is offline.
            push(LeaveWord(OFFLINE_TEXT.format(uin=answer.responder)))
        elif answer.result == friend.RESULT_SUCCESS:
            push(LeaveWord(ACCEPTED_TEXT.format(name=name, uin=answer.responder)))
            request_list()
        else:
            push(LeaveWord(REFUSED_TEXT.format(name=name, uin=answer.responder)))

    def system_line(text: str) -> None:
        # The pursuit notices go to the chat area as a system line.
        lists = {"selRoom": LOBBY_CHAT, "room": ROOM_CHAT}
        widget = lists.get(getattr(runtime, "current_screen", ""))
        if widget is not None and runtime.find(widget) is not None:
            runtime.namespace["Win_SetColorText"](widget, text, CHANNEL_ITEMTEXT, *SYSTEM_COLOR)
        log(f"[friend] {text}")

    def shield(uin: int, on: bool) -> None:
        if uin <= MIN_SHIELD_UIN:
            system_line(SHIELD_BAD_UIN_TEXT)
        elif uin == session.uin:
            system_line(SHIELD_SELF_TEXT)
        elif on:
            system_line(SHIELD_TEXT.format(uin=uin))
            session.shielded.add(uin)
        else:
            system_line(UNSHIELD_TEXT.format(uin=uin))
            session.shielded.discard(uin)

    def pursue(found: friend.FoundPlayer) -> None:
        """0x10010D83 on the FIND_FRIEND answer: three notices, otherwise
        go to the friend (CQQTLogic vtbl+600 with server, section, room)."""
        here = getattr(session, "section", None)
        same_section = here is not None and (here.server_id, here.id) == (found.server_id, found.section_id)
        if found.status == friend.FIND_STATUS_OFFLINE:
            system_line(PURSUIT_OFFLINE_TEXT)
        elif found.status == friend.FIND_STATUS_ONLINE and same_section:
            system_line(PURSUIT_NO_ROOM_TEXT)
        elif found.status == friend.FIND_STATUS_IN_GAME:
            system_line(PURSUIT_IN_GAME_TEXT)
        elif same_section and found.room_id:
            script("EnterRoom", found.room_id)
        else:
            # Moving to another section first is not done yet.
            log(f"[friend] pursuit of {found.uin} needs section {found.server_id}/{found.section_id}, room {found.room_id}")

    def on_find(packet) -> None:
        show_found(friend.parse_find_friend(packet.payload))

    def show_found(found: friend.FoundPlayer) -> None:
        """The FIND_FRIEND answer handler (QQTSection 0x1001D126). The room
        seat's 查看 (room.py, 0x1000CF2B) hands it a reply built from the
        seat record too."""
        if found.result == friend.RESULT_SUCCESS and found.uin == state.pursuing:
            state.pursuing = 0
            pursue(found)
            return
        if found.result != friend.RESULT_SUCCESS:
            # QQTSection 0x1001D18A prints this as a coloured system line; this
            # server sends no reply at all when the lookup fails.
            log(f"[friend] {FIND_FAILED_TEXT} ({found.uin}, result {found.result})")
            script("InitPlayerModeInfo")
            return
        state.detail = detail(found)
        script("go2playerInfo", found.uin, 0)
        if dress_profile is not None:
            dress_profile(found)  # 0x0048EB20 dresses the dialog's avatar before the details
        script("ui_getPlayerDetail")

    session.handlers.update({
        friend.FRIEND_LIST_COMMAND: on_friend_list,
        friend.FRIEND_UPDATE_COMMAND: on_update,
        friend.ADD_FRIEND_COMMAND: on_application,
        friend.REMOVE_FRIEND_COMMAND: on_remove,
        friend.ANSWER_FRIEND_COMMAND: on_answer,
        friend.FIND_FRIEND_COMMAND: on_find,
    })
    session.on_entered.append(state.reset)
    session.on_disconnected.append(state.reset)
    session.show_found_player = show_found

    excluded = ("script", "show_found", "system_line", "shield", "pursue", "lobby_players", "lobby_player", "refresh", "message_box", "push", "request_list", "friend_info",
                "section_names", "detail")
    return {name: value for name, value in locals().items()
            if callable(value) and name not in excluded and not name.startswith("on_") and name[0] != "_"}
