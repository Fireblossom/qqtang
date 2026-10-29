"""Lobby chat natives (uiSelRoom chat area): public lobby chat, small-bugle
broadcasts and whispers.

The lobby chat list shows public lines and whispers; the whisper tab shows
whispers only. Bugle messages go to the scripts' NotifyBroadcast, which runs
the scrolling banner on the lobby, room and game screens. Lobby chat that
arrives while the player is in a room is not shown, as in the original.
"""

from __future__ import annotations

from typing import Any, Callable

from .net import chat
from .runtime import UIRuntime
from .script import call_script, log
from .session import Session

LOBBY_CHAT = "UI.selRoom.chatArea.chatPanel.chatList"
LOBBY_WHISPERS = "UI.selRoom.chatArea.wisperPanel.chatList"
ROOM_CHAT = "UI.room.chatArea.chatPanel.chatList"
ROOM_WHISPERS = "UI.room.chatArea.wisperPanel.chatList"
CHANNEL_ITEMTEXT = 207  # uiConst value_channel_itemtext
CHAT_COLOR = (255, 255, 255)
WHISPER_COLOR = (255, 150, 255)
SYSTEM_COLOR = (255, 255, 0)


def build_chat_natives(runtime: UIRuntime, session: Session) -> dict[str, Callable[..., Any]]:
    whisper_target: dict[str, Any] = {"uin": 0, "name": ""}
    pending_whispers: list[tuple[str, str]] = []  # (target name, text) awaiting the reply

    def script(name: str, *args: Any) -> Any:
        function = runtime.namespace.get(name)
        return call_script(function, *args) if callable(function) else None

    def show(lists: tuple[str, ...], text: str, color: tuple[int, int, int]) -> None:
        for widget in lists:
            if runtime.find(widget) is not None:
                runtime.namespace["Win_SetColorText"](widget, text, CHANNEL_ITEMTEXT, *color)

    def show_whisper(text: str) -> None:
        screen = runtime.current_screen
        if screen == "selRoom":
            show((LOBBY_CHAT, LOBBY_WHISPERS), text, WHISPER_COLOR)
        elif screen == "room":
            show((ROOM_CHAT, ROOM_WHISPERS), text, WHISPER_COLOR)

    def bugle_count() -> int:
        items = session.profile.items if session.profile else []
        return sum(item.quantity for item in items if item.item_id == chat.SMALL_BUGLE_ITEM_ID)

    def use_bugle() -> None:
        for item in session.profile.items if session.profile else []:
            if item.item_id == chat.SMALL_BUGLE_ITEM_ID and item.quantity > 0:
                item.quantity -= 1
                return

    # Natives ----------------------------------------------------------------

    def SectionChat(mode, text):
        """mode 0: lobby chat (or a whisper when a target is set), 1: bugle.
        Slash commands other than "/w off" (kin chat, invitations, ...) are
        not supported yet."""
        text = str(text).strip()
        if not text:
            return
        if text.startswith("/"):
            if text.lower() == "/w off":
                whisper_target.update(uin=0, name="")
            else:
                log(f"[chat] unsupported chat command {text!r}")
            return
        if int(mode) == 1:
            if bugle_count() > 0 and session.send(chat.SECTION_CHAT_COMMAND, chat.build_section_chat(session.uin, text, bugle=True)):
                use_bugle()
            return
        if whisper_target["uin"] and whisper_target["uin"] != session.uin:
            pending_whispers.append((whisper_target["name"], text))
            session.send(chat.WHISPER_COMMAND, chat.build_whisper(session.uin, whisper_target["uin"], text))
            return
        session.send(chat.SECTION_CHAT_COMMAND, chat.build_section_chat(session.uin, text))

    def SetWisperPlayer(uin, name):
        whisper_target.update(uin=int(uin), name=str(name))

    def GetWisperName(kind=0):
        """0: the whisper target ("0" for none, which shows the lobby name);
        1: the kin chat channel, not supported."""
        if int(kind) == 0:
            return whisper_target["name"] if whisper_target["uin"] else "0"
        return ""

    def GetBugleNumber():
        return bugle_count()

    # Server messages --------------------------------------------------------

    def on_section_result(packet) -> None:
        result = chat.parse_result(packet.payload)
        if result != 0:
            log(f"[chat] lobby chat rejected: 0x{result:04X}")
            if runtime.current_screen == "selRoom":
                show((LOBBY_CHAT,), "消息发送失败", SYSTEM_COLOR)

    def on_section_message(packet) -> None:
        message = chat.parse_section_message(packet.payload)
        log(f"[chat] {'bugle' if message.bugle else 'lobby'} {message.nickname}: {message.text} (screen {runtime.current_screen})")
        if message.bugle:
            script("NotifyBroadcast", message.nickname + chat.SAID, message.uin, message.text)
        elif runtime.current_screen == "selRoom":
            show((LOBBY_CHAT,), message.nickname + chat.SAID + message.text, CHAT_COLOR)

    def on_whisper_result(packet) -> None:
        name, text = pending_whispers.pop(0) if pending_whispers else ("", "")
        if chat.parse_result(packet.payload) != 0:
            show_whisper(f"无法对{name}说悄悄话")
        elif text:
            show_whisper(f"你悄悄对{name}说:{text}")

    def on_whisper(packet) -> None:
        whisper = chat.parse_whisper(packet.payload)
        show_whisper(f"{whisper.nickname}悄悄对你说:{whisper.text}")

    session.handlers.update({
        chat.SECTION_CHAT_COMMAND: on_section_result,
        chat.SECTION_CHAT_NOTIFY_COMMAND: on_section_message,
        chat.WHISPER_COMMAND: on_whisper_result,
        chat.WHISPER_NOTIFY_COMMAND: on_whisper,
    })
    session.on_disconnected.append(lambda: (whisper_target.update(uin=0, name=""), pending_whispers.clear()))

    excluded = ("script", "show", "show_whisper", "bugle_count", "use_bugle")
    return {name: value for name, value in locals().items()
            if callable(value) and name[0].isupper() and name not in excluded}
