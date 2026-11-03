"""Kin (家族) natives and server messages, as QQTSection's CKinHandler does
them (docs/kin-re.md). Every query reads the local cache; replies update
it and then run the original's script refreshes.

Registered after friend.py: GetPlayerCnt, fetch_PlayerInfoInPlayerList and
getExtItem gain their kin modes 2 and 3.
"""

from __future__ import annotations

import time
import types
from typing import Any, Callable

from . import lobby as lobby_module
from .net import kin
from .net import lobby as lobby_net
from .net.player import GameInfo
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

MODE_KIN, MODE_KIN_ONLINE = 2, 3
OWNER_GRADE = 12
KIN_COLOR = (0x18, 0xFF, 0x00)  # 0x18FF00, bytes 2, 1, 0 (Client.exe 0x0048A890)
CHANNEL_ITEMTEXT = 207
CHANNELS = {"selRoom": "UI.selRoom.chatArea.chatPanel.chatList", "room": "UI.room.chatArea.chatPanel.chatList", "game": "UI.game.chatList"}
KIN_PANELS = {"selRoom": "UI.selRoom.chatArea.kinchatPanel.chatList", "room": "UI.room.chatArea.kinchatPanel.chatList"}
MEMBER_CACHE = 200


class KinState:
    def __init__(self) -> None:
        self.reset()

    def reset(self) -> None:
        self.kin_index = 0
        self.base = kin.empty_base()  # K+0x324C, GetInviteKinInfo(1)
        self.other = kin.empty_base()  # K+0x3542, GetInviteKinInfo(0)
        self.members: list[dict] = []
        self.titles: list[tuple[int, str]] = []
        self.dismiss_pending = False  # K+4
        self.leaving = False  # K+0x39AC
        self.fresh = False  # K+0x39A0: the next FetchKinBase sends time 0
        self.list_update = 0  # K+0x3998
        self.last_fetch = -1
        self.top: dict | None = None
        self.order_time = 0
        self.chat_name = ""  # /KINCHAT on
        self.invitation = kin.empty_base()  # the inviting kin, kept apart from other (K+0x3542)


def build_kin_natives(runtime: UIRuntime, session: Session) -> dict[str, Callable[..., Any]]:
    state = KinState()
    lobby_count = runtime.namespace["GetPlayerCnt"]
    lobby_fetch = runtime.namespace["fetch_PlayerInfoInPlayerList"]
    lobby_ext = runtime.namespace["getExtItem"]
    lobby_info = runtime.namespace["GetPlayerInfoByUin"]

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def line(text: str, both: bool = True) -> None:
        """A kin line: the current channel and, when both, the kin chat panel
        (place 0x1F), colour 0x18FF00 (docs/kin-re.md §6)."""
        screen = runtime.current_screen
        for widget in (CHANNELS.get(screen), KIN_PANELS.get(screen) if both else None):
            if widget is not None and runtime.find(widget) is not None:
                runtime.namespace["Win_SetColorText"](widget, text, CHANNEL_ITEMTEXT, *KIN_COLOR)
        log(f"[kin] {text}")

    def send(command: int, payload: bytes) -> None:
        session.send(command, payload)

    def now() -> int:
        return int(time.time()) & 0xFFFFFFFF

    def member(uin: int) -> dict | None:
        return next((entry for entry in state.members if entry["uin"] == uin), None)

    def grade_of(entry: dict | None) -> int:
        if entry is None:
            return 0
        return (entry["status"] >> 24) & 0xF or entry.get("grade", 0)

    def my_grade() -> int:
        return grade_of(member(session.uin))

    def owner() -> bool:
        return state.base.m_dwUin == session.uin and state.kin_index != 0

    def sort_members() -> None:
        # 0x10017041: online first, then by grade, high to low.
        state.members.sort(key=lambda entry: (not entry["status"] & 1, -grade_of(entry)))

    def refresh() -> None:
        script("ui_updateMemberList")
        script("updatePlayer")

    def fetch_base(kin_index: int) -> None:
        # 0x100137C2: one request a second at most.
        second = int(time.time())
        if second == state.last_fetch:
            return
        state.last_fetch = second
        stamp = 0 if state.fresh else state.base.base_update
        send(kin.FETCH_BASE_COMMAND, kin.build_words(session.uin, stamp, kin_index, int(kin_index == state.kin_index and kin_index != 0)))

    # DoKinTask --------------------------------------------------------------

    def DoKinTask(p1, p2, _length, n, mode):
        p1, p2, n, mode = str(p1), str(p2), int(n), int(mode)
        if mode == 0:  # create, 0x10012E90
            if not p1:
                line("不能创建无名家族", both=False)
            elif not p2:
                line("家族宣言不能为空", both=False)
            else:
                send(kin.CREATE_COMMAND, kin.build_create(session.uin, p1, p2, n))
        elif mode == 1:
            fetch_base(n)
        elif mode == 2:  # promote ('Y') or demote, 0x10015021
            target = member(n)
            index = next((k for k in range(len(state.titles) - 1, 0, -1) if state.titles[k][1] == p2), 0)
            if index == 0 and p1 not in ("Y", "y"):
                return  # 原版 bug，已修正: an unknown title used to demote to grade 10
            if p1 in ("Y", "y"):
                if index == 0:
                    return
                grade = state.titles[index - 1][0]
            else:
                if index >= len(state.titles) - 1:
                    return
                grade = state.titles[index + 1][0]
            mine = my_grade()
            if not (mine > grade and grade_of(target) < mine):
                line("您的权限不够,无法调整职位")
                return
            send(kin.ASSIGN_AUTHORITY_COMMAND, kin.build_words(state.kin_index, session.uin, now(), n, grade))
        elif mode == 3:  # badge, 0x100148A3
            badge = int(p2) if p2.lstrip("-").isdigit() else 0
            if badge and badge != state.base.m_stKinFlagID.m_iFlagID:
                send(kin.SET_BADGE_COMMAND, kin.build_words(state.kin_index, session.uin, now(), badge, -1))
        elif mode == 4:  # invite, 0x10014141
            if n == session.uin or my_grade() <= 9:
                return
            send(kin.OPERATE_COMMAND, kin.build_words(session.uin, now(), state.kin_index, n, 0, 0))
            line(f"【家族信息】您向玩家[{n}]的邀请已经发出。")
        elif mode == 5:  # kick, 0x10014A2C
            if not owner():
                line("你不是族长,无权开除成员.")
            elif n == state.base.m_dwUin:
                line("不能开除族长.")
            else:
                send(kin.KICK_COMMAND, kin.build_words(session.uin, now(), n, state.kin_index))
        elif mode == 6:  # exit, 0x10014C4F
            if state.leaving:
                line("你已经处于离开状态")
            elif owner():
                line("族长不能退出家族.")
            else:
                send(kin.EXIT_COMMAND, kin.build_words(session.uin, now(), state.kin_index))
        elif mode == 7:  # dismiss or cancel, 0x10014E54
            if not owner():
                line("你不是族长,无权解散家族.")
            else:
                send(kin.DISMISS_COMMAND, kin.build_words(session.uin, now(), state.kin_index, 2 if state.dismiss_pending else 1))
        elif mode == 9:  # position names, 0x10014495
            names = p2.split("\x07")[:5]
            wire = kin.encode_titles(names)
            if owner() and len(names) == 5 and wire != state.base.title_wire:
                send(kin.SET_AUTHORITY_COMMAND, kin.build_text(state.kin_index, session.uin, now(), wire))
        elif mode == 11:  # answer an invitation, 0x10015516
            para = 0 if p2 in ("Y", "y") else kin.REJECTED
            # The first field is the inviting kin's owner (K+0x3546), not the
            # inviter. The original then falls into mode 12 and sends "Y"/"N"
            # as kin chat with no kin; the server refuses it, so it is skipped.
            # 原版 bug，已修正: the answer read K+0x3542, which viewing another
            # kin overwrites; the inviting kin is kept apart.
            send(kin.SERVER_OPERATE_COMMAND, kin.build_words(state.invitation.m_dwUin, now(), state.invitation.m_dwKinIndex, session.uin, para, 0))
        elif mode == 12:
            send(kin.CHAT_COMMAND, kin.build_chat(session.uin, state.kin_index, p2))
        elif mode == 13:  # declaration, 0x100171FF
            if owner() and p2 != state.base.m_szContent:
                send(kin.SET_DECLARATION_COMMAND, kin.build_text(session.uin, now(), state.kin_index, p2.encode("gbk", "replace")[:kin.DECLARATION_MAX]))
        elif mode == 14:  # notification, 0x10017445
            if owner() and p2 != state.base.m_szNotification:
                send(kin.SET_NOTIFICATION_COMMAND, kin.build_text(state.kin_index, session.uin, now(), p2.encode("gbk", "replace")[:kin.DECLARATION_MAX]))
        elif mode == 15:  # rankings, 0x10017691
            send(kin.FETCH_TOP_COMMAND, kin.build_words(session.uin, now(), state.order_time, state.kin_index, 0, 0))

    # Queries ----------------------------------------------------------------

    def GetKinParam(i):
        return {0: state.kin_index, 1: state.base.m_iLastHonor}.get(int(i), 0)

    def GetInviteKinInfo(i):
        return {0: state.other, 1: state.base}.get(int(i), kin.empty_base())

    def GetKinPosition(i):
        i = int(i)
        return state.titles[i][1] if 0 <= i < len(state.titles) else ""

    def GetKinPositionByUin(uin):
        grade = grade_of(member(int(uin)))
        return next((name for title_grade, name in kin.decode_titles(state.base.title_wire) if title_grade == grade), "") if grade else ""

    def GetKinMemberHonor(uin, mode):
        entry = member(int(uin))
        if entry is None:
            return 0
        mode = int(mode)
        if mode == 1:
            return grade_of(entry)
        if mode == 2:
            return int(state.dismiss_pending) if int(uin) == state.base.m_dwUin else 0
        return entry["honor"]

    def player_record(entry: dict) -> tuple[Any, int]:
        cached = lobby_info(entry["uin"])
        if getattr(cached, "m_dwPlayerUin", 0):
            return cached, 1  # in the section's player list
        game = GameInfo(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
        info = lobby_module.native_player(lobby_net.Player(entry["uin"], entry["nickname"], 0, 0, 0, 0, game, [], 0, "", b"\xff" * 8))
        info.m_dwKinIndex = state.kin_index
        info.m_szKinName = state.base.m_szName
        info.m_nKinNameLen = len(state.base.m_szName.encode("gbk", "replace"))
        info.m_stKinFlagID = state.base.m_stKinFlagID
        return info, 0

    def GetPlayerCnt(mode=0):
        mode = int(mode)
        if mode == MODE_KIN:
            return state.base.m_wMemberNum
        if mode == MODE_KIN_ONLINE:
            return sum(1 for entry in state.members if entry["status"] & 1)
        return lobby_count(mode)

    def fetch_PlayerInfoInPlayerList(index, mode=0):
        mode, index = int(mode), int(index)
        if mode not in (MODE_KIN, MODE_KIN_ONLINE):
            return lobby_fetch(index, mode)
        if not 0 <= index < len(state.members):
            # The original does not check the index; an all-zero record.
            game = GameInfo(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
            empty = lobby_module.native_player(lobby_net.Player(0, "", 0, 0, 0, 0, game, [], 0, "", b"\xff" * 8))
            return types.SimpleNamespace(m_nPlayerStatus=0, m_PlayerInfo=empty)
        info, status = player_record(state.members[index])
        return types.SimpleNamespace(m_PlayerInfo=info, m_nPlayerStatus=status)

    def getExtItem(index, slot, mode=0):
        if int(mode) == MODE_KIN:
            return 0
        return lobby_ext(index, slot, mode)

    def fetch_topkininfolist(tel, rep, i):
        """0x100179C2: ten rows a tab, then the own kin's row."""
        tel, rep, i = int(tel), int(rep), int(i)
        empty = types.SimpleNamespace(m_dwKinIndex=0, m_szName="", m_iValue=0, m_nOrder="", m_nAscend=0)
        if i >= 12 or state.top is None:
            return empty
        records, group = state.top["records"], state.top["group_members"]

        def order_text(order: int) -> str:
            if not group:  # the original divides by zero here
                return str(order)
            letter, number = divmod(order, group)
            if number == 0:
                letter, number = letter - 1, 100
            return f"{chr(ord('A') + letter)}{number}" if 0 <= letter < 26 else f"Z+{number:3d}"

        def ascend(order: int, before: int) -> int:
            return 1 if before == 0 or order < before else 0 if order == before else 2

        if i < 10:
            # [telecom honor][telecom activity][netcom honor][netcom activity]
            record = records[i + {(1, 1): 0, (1, 0): 10, (0, 1): 20, (0, 0): 30}[(int(tel == 1), int(rep == 1))]]
            return types.SimpleNamespace(m_dwKinIndex=record["kin"], m_szName=record["name"], m_iValue=record["value"],
                                         m_nOrder=order_text(record["order"]), m_nAscend=ascend(record["order"], record["before"]))
        # 原版 bug，已修正: 0x100179C2 compares tel (1/0) with the section
        # (1/2), so a netcom kin never matched; compare like with like.
        if not state.kin_index or (tel == 1) != (state.base.m_iKinSection == 1):
            return empty
        if i == 10:
            same = records[40 if rep else 41] if len(records) > 41 else None
            if same and same["kin"]:
                return types.SimpleNamespace(m_dwKinIndex=same["kin"], m_szName=same["name"], m_iValue=same["value"],
                                             m_nOrder=order_text(same["order"]), m_nAscend=ascend(same["order"], same["before"]))
        base = state.base
        value, order, before = ((base.m_iLastHonor, base.last_honor_order, base.before_honor_order) if rep
                                else (base.last_active_point, base.last_active_order, base.before_active_order))
        return types.SimpleNamespace(m_dwKinIndex=base.m_dwKinIndex, m_szName=base.m_szName, m_iValue=value,
                                     m_nOrder=order_text(order) if order else "Z++", m_nAscend=ascend(order, before))

    # Server messages --------------------------------------------------------

    def reader(packet) -> kin.Reader:
        return kin.Reader(packet.payload)

    def own(base: types.SimpleNamespace) -> None:
        state.base = base
        state.titles = kin.decode_titles(base.title_wire)

    def on_create(packet) -> None:
        r = reader(packet)
        uin, kin_index, error = r.u32(), r.u32(), r.u32()
        reason = r.text16() if r.left() >= 2 else ""
        if error == 0:
            state.kin_index, state.base.m_dwKinIndex, state.base.m_dwUin = kin_index, kin_index, uin
            state.base.m_wMemberNum, state.fresh = 1, True
            fetch_base(kin_index)
        if reason:
            runtime.show_msgbox(reason, "", 0, "UI.SysMsgbox", 0)
            line(reason)

    def on_base(packet) -> None:
        r = reader(packet)
        r.u32()
        base = kin.read_base(r)
        if base.m_dwKinIndex and base.m_dwKinIndex == state.kin_index:
            state.dismiss_pending = state.dismiss_pending or base.m_dwDismissTime != 0
            own(base)
            if state.list_update < base.list_update:
                send(kin.FETCH_MEMBERS_COMMAND, kin.build_words(session.uin, now(), state.kin_index, state.list_update))
        else:
            state.other = base
            script("ShowkinInfoDlg")

    def on_members(packet) -> None:
        r = reader(packet)
        r.u32()
        list_update, result, members = kin.read_members(r)
        if result == 0:
            state.list_update, state.fresh = list_update, False
            state.members = members[:MEMBER_CACHE]
            sort_members()
        refresh()

    def on_operate(packet) -> None:
        r = reader(packet)
        _uin, _time, _kin, dst, para, _reserve = (r.u32() for _ in range(6))
        reason = r.text16() if r.left() >= 2 else ""
        if para == 0:
            state.fresh = True
            if member(dst) is None:
                state.members.append({"uin": dst, "nickname": str(dst), "status": 0, "grade": 0, "honor": 0, "active": 0})
        if reason:
            line(reason, both=False)
        if para == 0:
            refresh()

    def on_invited(packet) -> None:
        r = reader(packet)
        for _ in range(6):
            r.u32()
        state.other = kin.read_base(r)
        state.invitation = state.other
        if state.base.m_dwKinIndex == 0:
            push = getattr(session, "push_script_word", None)
            if push is not None:
                push("ShowkininviteDlg")
        else:
            send(kin.SERVER_OPERATE_COMMAND, kin.build_words(state.other.m_dwUin, now(), state.other.m_dwKinIndex, session.uin, kin.REJECTED, 0))

    def on_joined(packet) -> None:
        r = reader(packet)
        _uin, _time, kin_index, dst, para, _reserve = (r.u32() for _ in range(6))
        base = kin.read_base(r)
        if para == 0:
            state.fresh, state.kin_index = True, kin_index
            if member(dst) is None:
                state.members.append({"uin": dst, "nickname": str(dst), "status": 1, "grade": 0, "honor": 0, "active": 0})
            state.base = base  # the position table is not rebuilt (as the original)
            line(f"【家族公告】:热烈欢迎[{dst}]加入{base.m_szName}家族")
        else:
            line("未能加入家族。", both=False)

    def on_chat(packet) -> None:
        r = reader(packet)
        uin, _kin = r.u32(), r.u32()
        text = r.raw(r.u32()).decode("gbk", "replace")
        entry = member(uin)
        name = entry["nickname"] if entry else str(uin)
        line(f"{name}[{uin}]说:{text}")

    def on_kicked(packet) -> None:
        r = reader(packet)
        r.u16()
        for _ in range(3):
            r.u32()
        reason = r.text16() if r.left() >= 2 else ""
        if reason:
            line(reason)
        script("ui_updateMemberList")

    def on_exit(packet) -> None:
        r = reader(packet)
        result = r.u16()
        r.u32()
        r.u32()
        reason = r.text16() if r.left() >= 2 else ""
        if result == 0:
            state.leaving = True
        if reason:  # with none the original prints an uninitialised buffer
            line(reason)

    def on_dismiss(packet) -> None:
        r = reader(packet)
        result = r.u16()
        r.u32()
        r.u32()
        operation = r.u32()
        reason = r.text16() if r.left() >= 2 else ""
        if result == 0:
            state.dismiss_pending = {1: True, 2: False}.get(operation, state.dismiss_pending)
        if reason:
            line(reason)

    def on_event(packet) -> None:
        r = reader(packet)
        event, kin_index, uin = r.u32(), r.u32(), r.u32()
        nickname = r.text16()
        attach = r.u32()
        r.text16()
        description = r.text16()
        if kin_index != state.kin_index:
            return
        line(f"【家族公告】:{description}")
        if event == 1 and member(uin) is None:
            state.members.append({"uin": uin, "nickname": nickname, "status": 0, "grade": 0, "honor": 0, "active": 0})
        elif event == 5:
            state.kin_index, state.members, state.dismiss_pending = 0, [], False
            state.base = kin.empty_base()
        elif event == 6:
            state.base.m_stKinFlagID = types.SimpleNamespace(m_dwIndex=attach, m_iFlagID=uin)
        elif event == 8:
            if attach == session.uin:
                state.base, state.kin_index = kin.empty_base(), 0
            else:
                state.members = [entry for entry in state.members if entry["uin"] != attach]
        elif event == 9:
            state.base.m_szNotification = description
        refresh()

    def on_authority(packet) -> None:
        r = reader(packet)
        result = r.u16()
        _kin, _uin, dst, authority = (r.u32() for _ in range(4))
        reason = r.text16() if r.left() >= 2 else ""
        if result == 0:
            entry = member(dst)
            if entry is not None:
                entry["status"] = (entry["status"] & 0x00FFFFFF) | (authority << 24)
                entry["grade"] = authority
        if reason:
            line(reason)
        if result == 0:
            refresh()

    def on_titles(packet) -> None:
        r = reader(packet)
        result = r.u16()
        r.u32()
        wire = r.raw(r.u16())
        reason = r.text16() if r.left() >= 2 else ""
        if result == 0:
            state.base.title_wire, state.base.m_wTitleLen = wire, len(wire)
            state.titles = kin.decode_titles(wire)
        if reason:
            line(reason)
        if result == 0:
            script("updateKinManagerDlg")

    def on_badge(packet) -> None:
        r = reader(packet)
        result = r.u16()
        _kin, _uin, badge, defined = (r.u32() for _ in range(4))
        if result == 0:
            state.base.m_stKinFlagID = types.SimpleNamespace(m_dwIndex=defined, m_iFlagID=badge)
            script("updateKinManagerDlg")

    def on_top(packet) -> None:
        r = reader(packet)
        if r.u16() == 0:
            state.top = kin.read_top(r)
            state.order_time = state.top["order_time"]
            script("ui_updateTopkinList")

    def on_declaration(packet) -> None:
        r = reader(packet)
        result = r.u16()
        uin, _kin = r.u32(), r.u32()
        content = r.text16()
        if result == 0 and uin == session.uin:
            state.base.m_szContent = content
            state.base.m_wContentLen = len(content.encode("gbk", "replace"))
            send(kin.CHAT_COMMAND, kin.build_chat(session.uin, state.kin_index, "家族宣言已更新。"))
            script("updateKinManagerDlg")

    def on_notification(packet) -> None:
        r = reader(packet)
        result = r.u16()
        r.u32()
        r.u32()
        r.text16()
        reason = r.text16() if r.left() >= 2 else ""
        line("成功设置家族公告" if result == 0 else f"设置家族公告失败 {reason}")

    session.handlers.update({
        kin.CREATE_COMMAND: on_create, kin.FETCH_BASE_COMMAND: on_base, kin.FETCH_MEMBERS_COMMAND: on_members,
        kin.OPERATE_COMMAND: on_operate, kin.SERVER_OPERATE_COMMAND: on_invited, kin.SERVER_OPERATE_NOTIFY_COMMAND: on_joined,
        kin.CHAT_NOTIFY_COMMAND: on_chat, kin.KICK_COMMAND: on_kicked, kin.EXIT_COMMAND: on_exit, kin.DISMISS_COMMAND: on_dismiss,
        kin.EVENT_NOTIFY_COMMAND: on_event, kin.ASSIGN_AUTHORITY_COMMAND: on_authority, kin.SET_AUTHORITY_COMMAND: on_titles,
        kin.SET_BADGE_COMMAND: on_badge, kin.FETCH_TOP_COMMAND: on_top, kin.SET_DECLARATION_COMMAND: on_declaration,
        kin.SET_NOTIFICATION_COMMAND: on_notification, kin.CHAT_COMMAND: lambda _packet: None,
    })
    session.on_entered.append(state.reset)
    session.on_disconnected.append(state.reset)
    session.kin = state

    return {"DoKinTask": DoKinTask, "GetKinParam": GetKinParam, "GetInviteKinInfo": GetInviteKinInfo, "GetKinPosition": GetKinPosition,
            "GetKinPositionByUin": GetKinPositionByUin, "GetKinMemberHonor": GetKinMemberHonor, "GetPlayerCnt": GetPlayerCnt,
            "fetch_PlayerInfoInPlayerList": fetch_PlayerInfoInPlayerList, "getExtItem": getExtItem,
            "fetch_topkininfolist": fetch_topkininfolist}
