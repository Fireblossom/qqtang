"""Marriage and wedding (婚姻、婚礼) natives and server messages, as
QQTSection does them (docs/wedding-re.md). Two marriage records are cached,
our own and the last one looked up; replies run the original's script
calls, passing strings through the hidden UI.TextBuffer labels.

Registered after friend.py, whose GetMyMarriageDetail / GetMarriageDetail
move here; sends 0x107 before each FindFriend.
SetWeddingEffect and the couple effects live with the room scene
(room.py, roomwalk.py).
"""

from __future__ import annotations

import types
from typing import Any, Callable

from .net import marriage
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

INGAME_SCREEN = "game"  # S+0xC614 == 3: results go to the envelope queue
SPARK_FAIL_SOUND = "sparkFail.wav"


def info_view(info: marriage.MarriageInfo) -> types.SimpleNamespace:
    """RESPONSE_MARRIAGE_INFO as the scripts read it (0x0050B010)."""
    word = marriage.gbk(info.love_word)
    return types.SimpleNamespace(
        m_dwTargetUin=info.target_uin, m_dwMarriageUin=info.marriage_uin, m_dwMarriageLoyalty=info.loyalty,
        m_wMarriageLevel=info.level, m_dwLevelValue=info.level_value, m_dwMarriageAge=info.age,
        m_nLoveWordLength=len(word), m_szLoveWord=info.love_word, m_szSpouseNickname=info.spouse_nickname,
        m_dwRingID=info.ring_id)


class MarriageState:
    def __init__(self) -> None:
        self.reset()

    def reset(self) -> None:
        self.mine = marriage.MarriageInfo()  # S+0x40, Client.exe 0x7FF5E0
        self.other = marriage.MarriageInfo()  # 0x7FE078
        self.divorcing = False  # S+0xC544


def build_marriage_natives(runtime: UIRuntime, session: Session) -> dict[str, Callable[..., Any]]:
    state = MarriageState()
    session.marriage = state
    friend_find = runtime.namespace["FindFriend"]

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def buffer(widget: str, text: str) -> None:
        runtime.namespace["Win_SetText"](widget, text)

    def sound(name: str) -> None:
        play = runtime.namespace.get("PlaySound")
        if callable(play):
            play(name, 1)

    def message_box(text: str) -> None:
        runtime.show_msgbox(text, "", 3, "UI.SysMsgbox", 0)
        script("ui_msgBox", 3)

    def in_game() -> bool:
        return runtime.current_screen == INGAME_SCREEN

    def leave_word(text: str) -> None:
        push = getattr(session, "push_leave_word", None)
        if callable(push):
            push(text)
        else:
            log(f"[marriage] {text}")

    def notice(text: str, play: str | None = None) -> None:
        """A result box, or an envelope line during a match (no sound)."""
        if in_game():
            leave_word(text)
            return
        message_box(text)
        if play:
            sound(play)

    def request_info(target: int) -> None:
        session.send(marriage.INFO_COMMAND, marriage.build_info(session.uin, target))

    def on_login() -> None:
        # QQTSection 0x1001B49D: our own record right after login.
        state.reset()
        request_info(session.uin)

    session.on_entered.append(on_login)
    session.on_disconnected.append(state.reset)

    # Server messages --------------------------------------------------------

    def on_spark_result(packet) -> None:
        """0x104 / 0x106 (0x100327EF, 0x10032CC7): ResultID is not read and
        sparkFail.wav plays even on success."""
        result, _uin, _target, word = marriage.parse_spark_result(packet.payload)
        log(f"[marriage] spark result {result}: {word}")
        notice(word, SPARK_FAIL_SOUND)

    def on_spark(packet) -> None:
        uin, _target, nickname, word = marriage.parse_spark_notify(packet.payload)
        buffer("UI.TextBuffer", word)
        buffer("UI.TextBuffer2", nickname)
        script("ReceiveSpark", uin)
        sound("sparkCome.wav")

    def on_info(packet) -> None:
        _result, info = marriage.parse_info(packet.payload)
        log(f"[marriage] info {info.target_uin}: spouse {info.marriage_uin} level {info.level} ring {info.ring_id}")
        if info.target_uin == session.uin:
            state.mine = info
            script("ui_UpdateMyMarriageInfo")
        else:
            state.other = info

    def on_love_word(packet) -> None:
        result = marriage.parse_love_word_result(packet.payload)
        if result == 0:
            message_box("修改爱情宣言成功！")
            script("UpdateLoveword")
        elif result == marriage.RESULT_INDECENT:
            message_box("请不要在爱情宣言中使用非法或不文明词句，谢谢！")
        else:
            message_box("修改爱情宣言失败！")

    def on_divorce(packet) -> None:
        """0x10033305."""
        _uin, _target, result = marriage.parse_divorce(packet.payload)
        if state.divorcing:
            notice("离婚成功" if result == 0 else "离婚失败", "divorceOK.wav" if result == 0 else None)
            if result == 0:
                request_info(session.uin)
        elif result == 0:
            leave_word(f"{state.mine.spouse_nickname}与你离婚了！")
            request_info(session.uin)
        state.divorcing = False

    def on_start_wedding(packet) -> None:
        """0x10033AAD: SetWeddingStart whatever the result."""
        uin, target, nickname, target_nickname, result, _word, mode = marriage.parse_start_wedding(packet.payload)
        log(f"[marriage] wedding start {uin}->{target} mode {mode} result {result}")
        buffer("UI.TextBuffer", nickname)
        buffer("UI.TextBuffer2", target_nickname)
        script("SetWeddingStart", mode, uin, target)

    def on_wedding_confirm(packet) -> None:
        *_, word = marriage.parse_wedding_confirm(packet.payload)
        buffer("UI.TextBuffer", word)
        script("ReceiveMarriageConfirm")

    def on_answer_wedding(packet) -> None:
        """0x10033F77: the couple get the congratulations box, guests the
        priest's questions."""
        uin, target, nickname, target_nickname, result, word = marriage.parse_answer_wedding(packet.payload)
        log(f"[marriage] wedding answer {uin}->{target} result {result}: {word}")
        buffer("UI.TextBuffer", nickname)
        buffer("UI.TextBuffer2", target_nickname)
        couple = session.uin in (uin, target)
        if result == 0:
            if couple:
                buffer("UI.TextBuffer3", word)
                script("MyWeddingOver")
                request_info(session.uin)
            else:
                script("WeddingOver", uin, target)
        elif couple:
            message_box(word)
            sound(SPARK_FAIL_SOUND)
            script("RestartWedding")

    def on_change_mode(packet) -> None:
        _uin, result, word = marriage.parse_change_mode(packet.payload)
        if result != 0:
            message_box(word)

    def on_mode(packet) -> None:
        _uin, mode = marriage.parse_mode_notify(packet.payload)
        script("UpdateWeddingMode", mode)

    session.handlers.update({
        marriage.SPARK_COMMAND: on_spark_result,
        marriage.SPARK_NOTIFY_COMMAND: on_spark,
        marriage.ANSWER_SPARK_COMMAND: on_spark_result,
        marriage.INFO_COMMAND: on_info,
        marriage.LOVE_WORD_COMMAND: on_love_word,
        marriage.DIVORCE_COMMAND: on_divorce,
        marriage.START_WEDDING_COMMAND: on_start_wedding,
        marriage.WEDDING_CONFIRM_COMMAND: on_wedding_confirm,
        marriage.ANSWER_WEDDING_COMMAND: on_answer_wedding,
        marriage.CHANGE_MODE_COMMAND: on_change_mode,
        marriage.MODE_NOTIFY_COMMAND: on_mode,
    })

    # Natives ----------------------------------------------------------------
    # The scripts pass qqt_py2_len(txt), a GBK byte count; the text is
    # re-encoded here instead.

    def Spark(_length, text, uin):
        session.send(marriage.SPARK_COMMAND, marriage.build_spark(session.uin, int(uin), str(text)))

    def AnswerSpark(result, uin):
        session.send(marriage.ANSWER_SPARK_COMMAND, marriage.build_answer_spark(session.uin, int(uin), int(result)))

    def SaveLoveWord(_length, text):
        session.send(marriage.LOVE_WORD_COMMAND, marriage.build_love_word(session.uin, state.mine.marriage_uin, str(text)))

    def Divorce():
        if session.send(marriage.DIVORCE_COMMAND, marriage.build_pair(session.uin, state.mine.marriage_uin)):
            state.divorcing = True

    def RequestChangeWedMode(mode):
        session.send(marriage.CHANGE_MODE_COMMAND, marriage.build_change_mode(session.uin, int(mode)))

    def StartWedding(uin, mode):
        session.send(marriage.START_WEDDING_COMMAND, marriage.build_start_wedding(session.uin, int(uin), int(mode)))

    def AnswerWedding(result, uin):
        session.send(marriage.ANSWER_WEDDING_COMMAND, marriage.build_answer_wedding(session.uin, int(uin), int(result)))

    def GetMyMarriageDetail():
        return info_view(state.mine)

    def GetMarriageDetail():
        return info_view(state.other)

    def GetSeatIDByUin(uin):
        """0x1000A9D3: the seat (0..7) of a room member, else -1."""
        room = session.room_view()
        if room is None:
            return -1
        return getattr(room, "seats", {}).get(int(uin), -1)

    def FindFriend(uin):
        # QQTSection 0x10020917 asks for the marriage record before 0x67.
        request_info(int(uin))
        return friend_find(uin)

    return {name: value for name, value in locals().items() if callable(value) and name[0].isupper()}
