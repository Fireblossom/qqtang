"""Lobby (section) chat, the small bugle and whispers (internal/protocol/game/chat.go).

REQUEST_SECTION_CHAT 0x006E: u32 UIN, u32 time, u16 destination PlayerID,
u32 destination UIN, u16 length, GBK text. The destination fields select the
channel: PlayerID 0xFFFF with UIN 0 is public lobby chat, with UIN 1 a small
bugle broadcast (consumes item 294). The reply is a u16 result.

NOTIFY_SECTION_MSG 0x0092: u16 source PlayerID, u16 destination PlayerID, u16
length, "<nickname>说:<text>", then u32 UIN, u32 identity (bit 14 marks a
bugle), u32 effect, u32 points and an 8-byte kin flag.

Whispers use REQUEST_CHAT_ACROSS_SECTION 0x006C: u32 UIN, u32 time, u32
service ID, u16 destination PlayerID, u32 destination UIN, u16 length, text.
The reply (u16 result, u32 destination UIN) is the sender's confirmation; the
destination gets 0x0095: u32 source UIN, u16 length, text, 32-byte nickname.
"""

from __future__ import annotations

import dataclasses
import struct
import time

SECTION_CHAT_COMMAND = 0x006E
SECTION_CHAT_NOTIFY_COMMAND = 0x0092
WHISPER_COMMAND = 0x006C
WHISPER_NOTIFY_COMMAND = 0x0095

BROADCAST_PLAYER_ID = 0xFFFF
PUBLIC_UIN = 0
BUGLE_UIN = 1
BUGLE_IDENTITY = 0x4000
SMALL_BUGLE_ITEM_ID = 294
CHAT_MAX = 500
SAID = "说:"


@dataclasses.dataclass
class SectionMessage:
    source_player_id: int
    nickname: str
    text: str
    uin: int
    bugle: bool


@dataclasses.dataclass
class Whisper:
    source_uin: int
    nickname: str
    text: str


def _encode(text: str) -> bytes:
    return text.encode("gbk", "replace")[:CHAT_MAX]


def _decode(data: bytes) -> str:
    return data.split(b"\0", 1)[0].decode("gbk", "replace")


def _stamp(uin: int, now: int | None) -> bytes:
    return struct.pack(">II", uin, (int(time.time()) if now is None else now) & 0xFFFFFFFF)


def build_section_chat(uin: int, text: str, bugle: bool = False, now: int | None = None) -> bytes:
    content = _encode(text)
    return _stamp(uin, now) + struct.pack(">HIH", BROADCAST_PLAYER_ID, BUGLE_UIN if bugle else PUBLIC_UIN, len(content)) + content


def build_whisper(uin: int, destination_uin: int, text: str, destination_player_id: int = 0, now: int | None = None) -> bytes:
    content = _encode(text)
    return _stamp(uin, now) + struct.pack(">IHIH", 0, destination_player_id, destination_uin, len(content)) + content


def parse_result(payload: bytes) -> int:
    return struct.unpack_from(">H", payload, 0)[0]


def parse_section_message(payload: bytes) -> SectionMessage:
    source, _destination, length = struct.unpack_from(">HHH", payload, 0)
    message = _decode(payload[6:6 + length])
    nickname, _, text = message.partition(SAID)
    uin, identity = struct.unpack_from(">II", payload, 6 + length) if len(payload) >= 14 + length else (0, 0)
    return SectionMessage(source, nickname, text, uin, bool(identity & BUGLE_IDENTITY))


def parse_whisper(payload: bytes) -> Whisper:
    uin, length = struct.unpack_from(">IH", payload, 0)
    return Whisper(uin, _decode(payload[6 + length:6 + length + 32]), _decode(payload[6:6 + length]))
