"""Chat slash commands and the across-section message formats
(docs/slash-commands-re.md; QQTSection FUN_10024DCB parses and runs a line,
FUN_10026EFF reads what arrives).

A chat line whose first word starts with "/" and is not an emote code is a
command and is never sent as chat. The ordinary player's commands are here;
the referee and administrator ones (/it /ct /KICK /FORBID ...) need
identity bits an ordinary account lacks, so they print 输入无效命令 as the
original does for such players. /KINCHAT and /ki wait for the kin feature.

Whispers, room invitations and their answers all travel as 0x6C contents:
wisper_uin(<uin>)_content[<len>](<text>), in_uin(..)_serverid(..)_
sectionid(..)_roomid(..)_roomflag(..)_password[<len>](<text>) and
inresult_resultid(<n>).
"""

from __future__ import annotations

import re
import time
import types
from typing import Any, Callable

from .net import chat
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

LOBBY_CHAT = "UI.selRoom.chatArea.chatPanel.chatList"
ROOM_CHAT = "UI.room.chatArea.chatPanel.chatList"
GAME_CHAT = "UI.game.chatList"
CHANNEL_ITEMTEXT = 207
# 0xB8FF79 handed to the chat list as (r, g, b) by Client.exe 0x0048A890.
SYSTEM_COLOR = (0xB8, 0xFF, 0x79)
MIN_UIN = 10000  # 0x10025016
MEMBER_IDENTITY = 0x2000
INVITE_FLOOD_MS = 5000
INVALID = "输入无效命令"

# QQTSection 0x10063FB4: emote codes; a first word starting with one is chat.
EMOTES = tuple(f"/m{i:02d}" for i in range(24)) + (
    "/kdz", "/klz", "/kyl", "/kym", "/kyh", "/kxy", "/krh", "/kqq", "/kpx", "/kot", "/kku", "/kkx", "/khan", "/khx", "/khd", "/kgd",
    "/kfb", "/kfs", "/kdk", "/kcb", "/kbt", "/kbs", "/kbz", "/k23", "/jy", "/pz", "/se", "/fd", "/dy", "/ll", "/hx", "/bz", "/shui",
    "/dk", "/gg", "/fn", "/tp", "/cy", "/wx", "/ng", "/kuk", "/feid", "/zk", "/tu", "/tx", "/ka", "/baiy", "/am", "/jie", "/kun",
    "/jk", "/lh", "/hanx", "/db", "/fendou", "/zhm", "/yiw", "/xu", "/yun", "/zhem", "/shuai", "/kl", "/qiao", "/zj", "/shan",
    "/fad", "/aiq", "/tiao", "/zhao", "/mm", "/zt", "/maom", "/xg", "/yb", "/qianc", "/dp", "/bei", "/dg", "/shd", "/zhd", "/dao",
    "/zq", "/yy", "/bb", "/gf", "/fan", "/yw", "/mg", "/dx", "/wen", "/xin", "/xs", "/hy", "/lw", "/dh", "/sj", "/yj", "/ds", "/ty",
    "/yl", "/qiang", "/ruo", "/ws", "/shl", "/dd", "/mn", "/hl", "/mamao", "/qz", "/fw", "/oh", "/bj", "/qsh", "/xiq", "/xy",
    "/duoy", "/xr", "/xixing", "/nv", "/nan")
MEMBER_EMOTES = EMOTES[:24]  # 0x10064560

# FUN_1003A0AD's words (case-insensitive) -> command IDs.
COMMANDS = {"/it": 0x18, "/ct": 0x19, "/mute": 0xF, "/unmute": 0x10, "/refuse": 0x14, "/rf": 0x14, "/unrefuse": 0x15, "/urf": 0x15,
            "/m": 0x11, "/u": 0x12, "/r": 6, "/ar": 0xE, "/who": 2, "/in": 1, "/autojoin": 4, "/join": 3, "/go": 3, "/whoami": 0x13,
            "/w": 5, "/lodge": 7, "/kick": 9, "/forbid": 8, "/nologin": 0xD, "/kickoff": 10, "/sysmsg": 0xB, "/dismiss": 0xC,
            "/invisible": 0x1A, "/dontaddme": 0x16, "/canaddme": 0x17, "/kinchat": 0x1B, "/ki": 0x1C, "/c2c": 0x1D}
PRIVILEGED = {0x18, 0x19, 7, 8, 9, 10, 0xB, 0xC, 0xD, 0x1A}

INRESULT_TEXTS = {1: "抱歉，{name}[{uin}]不接受您的邀请。", 2: "{name}[{uin}]正在游戏中，不能接受您的邀请。", 3: "{name}[{uin}]进入房间失败。",
                  4: "{name}[{uin}]正在接受别人的邀请, 暂时不能接受您的邀请.", 5: "{name}[{uin}]在比赛频道中, 不能接受您的邀请."}
INRESULT_OTHER = "{name}[{uin}]不能进入游戏，邀请失败。"

_FIELD = re.compile(r"([a-z]+)\((-?\d+)\)")
_TEXT = re.compile(r"([a-z]+)\[(\d+)\]\(")


def gbk_length(text: str) -> int:
    return len(text.encode("gbk", "replace"))


def whisper_content(uin: int, text: str) -> str:
    return f"wisper_uin({uin})_content[{gbk_length(text)}]({text})"


def invite_content(uin: int, server_id: int, section_id: int, room_id: int, flag: int, password: str) -> str:
    return (f"in_uin({uin})_serverid({server_id})_sectionid({section_id})_roomid({room_id})_roomflag({flag})"
            f"_password[{gbk_length(password)}]({password})")


def parse_across(content: str) -> tuple[str, dict[str, Any]]:
    """Splits an across-section content into its kind (the text before the
    first "_", FUN_10039D67) and its fields; a field written key[len](text)
    takes len GBK bytes. Plain text comes back as kind ""."""
    kind, sep, rest = content.partition("_")
    if not sep or kind not in ("wisper", "in", "inresult"):
        return "", {"content": content}
    fields: dict[str, Any] = {}
    position = 0
    raw = rest.encode("gbk", "replace")
    while position < len(raw):
        text = raw[position:].decode("gbk", "replace")
        match = _TEXT.match(text)
        if match:
            length = int(match.group(2))
            start = position + len(text[:match.end()].encode("gbk", "replace"))
            fields[match.group(1)] = raw[start:start + length].decode("gbk", "replace")
            position = start + length + 1  # past ")"
        else:
            match = _FIELD.match(text)
            if not match:
                break
            fields[match.group(1)] = int(match.group(2))
            position += len(text[:match.end()].encode("gbk", "replace"))
        if raw[position:position + 1] == b"_":
            position += 1
    return kind, fields


class SlashState:
    def __init__(self) -> None:
        self.reply_uin, self.reply_name = 0, ""
        self.muted = False
        self.refuse_all = False
        self.refused: list[int] = []
        self.invited: dict[int, float] = {}  # uin -> when the next invitation may go
        self.invitation_open = False


def build_slash(runtime: UIRuntime, session: Session, whisper_target: dict[str, Any],
                send_whisper: Callable[[int, str], None]) -> types.SimpleNamespace:
    state = SlashState()
    session.dont_add_me = False

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def line(text: str) -> None:
        """A system line in the current channel (FUN_1000A6BD)."""
        widget = {"selRoom": LOBBY_CHAT, "room": ROOM_CHAT, "game": GAME_CHAT}.get(runtime.current_screen)
        if widget is not None and runtime.find(widget) is not None:
            runtime.namespace["Win_SetColorText"](widget, text, CHANNEL_ITEMTEXT, *SYSTEM_COLOR)
        log(f"[slash] {text}")

    def name_of(uin: int) -> str:
        return str(script("GetPlayerNickNamebyUin", uin, 0) or uin)

    def bad_uin(uin: int, text: str = "输入的玩家QQ号不正确") -> bool:
        if uin <= MIN_UIN:
            line(text)
            return True
        return False

    def run(word: str, args: list[str]) -> bool:
        """One command; False prints 输入无效命令 (FUN_10024DCB)."""
        command = COMMANDS.get(word.lower(), 0)
        number = int(args[0]) if args and args[0].lstrip("-").isdigit() else 0
        if command == 0 or command in PRIVILEGED:
            return False
        if command == 5:  # /W
            if len(args) == 1 and args[0].lower() == "off":
                whisper_target.update(uin=0, name="")
                script("UpdateUIWisper")
                return True
            if len(args) not in (1, 2) or bad_uin(number):
                return False
            if number == session.uin:
                line("不能和自己说悄悄话")
                return False
            whisper_target.update(uin=number, name=str(number))
            script("UpdateUIWisper")
            return True
        if command == 6:  # /R
            text = " ".join(args)
            if not state.reply_uin:
                line("没有回复对象")
                return False
            if not text or gbk_length(text) > 0x80:
                return False
            send_whisper(state.reply_uin, text)
            line(f"回复玩家{state.reply_name}[{state.reply_uin}]: {text}")
            return True
        if command in (0xF, 0x10):  # /mute /unmute
            state.muted = command == 0xF
            line("拒绝他人的悄悄话" if state.muted else "开放他人的悄悄话")
            return True
        if command in (0x14, 0x15):  # /refuse /unrefuse
            refuse = command == 0x14
            if len(args) != 1:
                state.refuse_all = refuse
                line("您拒绝了所有玩家的邀请！" if refuse else "您不再拒绝其他玩家对您的邀请！")
                return True
            if bad_uin(number, "输入的玩家帐号不正确"):
                return False
            if number == session.uin:
                line("拒绝自己的邀请无意义" if refuse else "取消拒绝自己的邀请无意义")
                return False
            if refuse:
                if number not in state.refused:
                    state.refused.append(number)
                line(f"您拒绝了玩家[{number}]的邀请！")
            elif number in state.refused:
                state.refused.remove(number)
                line(f"取消对玩家[{number}]邀请的拒绝")
            else:
                line(f"玩家[{number}]不在拒绝列表中")
            return True
        if command in (0x16, 0x17):  # /DONTADDME /CANADDME
            session.dont_add_me = command == 0x16
            line("开启 屏蔽添加好友请求功能" if session.dont_add_me else "关闭 屏蔽添加好友请求功能")
            return True
        if command in (0x11, 0x12):  # /M /U
            if len(args) not in (1, 2):
                return False
            script("DoFriendTask", "", 0, number, 5 if command == 0x11 else 7)
            return True
        if command in (2, 0x13):  # /WHO /whoami
            target = session.uin if command == 0x13 else number
            if command == 2 and (len(args) not in (1, 2) or target <= MIN_UIN):
                return False
            script("FindFriend", target)
            return True
        if command == 3:  # /JOIN /GO n [password]
            rooms = sorted(session.room_names)
            if session.room_view() is not None or not args or number == 0 or number > len(rooms):
                return False
            # The original copies the password in only after sending
            # (0x10025EB5 -> 0x10025ECC), so it never reaches the server.
            script("EnterRoom", rooms[number - 1], "")
            return True
        if command == 4:  # /AUTOJOIN
            if session.room_view() is not None:
                return False
            script("AutoJoinRoom")
            return True
        if command == 0xE:  # /AR
            on = not session.state.get(f"autoready_{session.uin}", False)
            session.save_state(**{f"autoready_{session.uin}": on})
            view = session.room_view()
            if view is not None and on and not view.owner and not view.ready:
                script("UserReady")
            line("设置自动准备" if on else "取消自动准备")
            return True
        if command == 1:  # /IN uin
            view = session.room_view()
            if view is None:
                line("只有在房间中才能邀请其他玩家")
                return True
            if len(args) not in (1, 2) or number <= MIN_UIN or number == session.uin:
                return False
            if number in view.uins:
                line(" 您要邀请的玩家已经在房间中。")
                return True
            now = time.monotonic() * 1000
            if now < state.invited.get(number, 0):
                line("不能频繁邀请同一玩家, 邀请失败, 请稍后再试")
                return True
            state.invited[number] = now + INVITE_FLOOD_MS
            section = session.section
            content = invite_content(session.uin, section.server_id if section else 0, section.id if section else 0,
                                     view.room_id, view.flag, view.password)
            session.send(chat.WHISPER_COMMAND, chat.build_whisper(session.uin, number, content))
            line("您的邀请已经发出。")
            return True
        if command == 0x1D:  # /c2c uin
            if len(args) not in (1, 2) or bad_uin(number):
                return False
            if number == session.uin:
                line("[温馨提示]:/yun，怎么和自己做交易！")
                return False
            script("C2CInviteNegotiate", number, 1)
            return True
        kin_state = getattr(session, "kin", None)
        if command == 0x1B:  # /KINCHAT on|off (FUN_1002DE18)
            if args and args[0].lower() == "off":
                whisper_target.update(uin=0, name="")
                if kin_state is not None:
                    kin_state.chat_name = ""
                script("UpdateUIWisper")
                return True
            if args and args[0].lower() == "on":
                whisper_target.update(uin=0, name="")
                if kin_state is None or kin_state.kin_index < 1:
                    line("你不在任何一个家族中,无法进入家族聊天")
                    return True
                name = kin_state.base.m_szName
                kin_state.chat_name = name if name and len(name.encode("gbk", "replace")) <= 0x27 else str(kin_state.kin_index)
                script("UpdateUIKinChat")
                return True
            return False
        if command == 0x1C:  # /ki uin
            if len(args) not in (1, 2) or bad_uin(number):
                return False
            script("DoKinTask", "", "", 0, number, 4)
            return True
        return False

    def handle(text: str) -> bool:
        """True when the line was a command or must not be sent."""
        words = text.split()
        if words and words[0].startswith("/") and not any(words[0].startswith(code) for code in EMOTES):
            if not run(words[0], words[1:]):
                line(INVALID)
            return True
        identity = session.profile.identity if session.profile else 0
        if not identity & MEMBER_IDENTITY and any(code in text for code in MEMBER_EMOTES):
            line("您现在还不是QQ堂会员，请加入会员后再使用会员表情")  # FUN_10024F39
            return True
        return False

    def answer_invite(uin: int, result: int) -> None:
        session.send(chat.WHISPER_COMMAND, chat.build_whisper(session.uin, uin, f"inresult_resultid({result})", 0xFFFF))

    def receive(uin: int, nickname: str, content: str) -> str | None:
        """FUN_10026EFF: the text to show as a whisper, or None when the
        message was handled (or dropped) here."""
        kind, fields = parse_across(content)
        if kind == "wisper":
            if state.muted or uin in session.shielded:
                return None
            state.reply_uin, state.reply_name = uin, nickname
            return fields.get("content", "")
        if kind == "in":
            inviter = int(fields.get("uin", uin))
            if state.invitation_open:
                answer_invite(inviter, 4)
            elif state.refuse_all or inviter in state.refused:
                answer_invite(inviter, 1)
            elif runtime.current_screen == "game":
                line(f"玩家{nickname}[{inviter}]邀请您一起游戏，您正在游戏不能接受。")
                answer_invite(inviter, 2)
            else:
                state.invitation_open = True

                def answered(ok: bool) -> None:
                    state.invitation_open = False
                    if ok:
                        script("EnterRoom", int(fields.get("roomid", 0)), str(fields.get("password", "")))
                    else:
                        answer_invite(inviter, 1)

                runtime.show_msgbox(f"玩家{nickname}[{inviter}]邀请您一起游戏，您是否同意？", "", 1, "UI.SysMsgbox", answered)
            return None
        if kind == "inresult":
            template = INRESULT_TEXTS.get(int(fields.get("resultid", 0)), INRESULT_OTHER)
            line(template.format(name=nickname or name_of(uin), uin=uin))
            return None
        state.reply_uin, state.reply_name = uin, nickname
        return content  # plain text from an older client

    return types.SimpleNamespace(handle=handle, receive=receive, state=state)
