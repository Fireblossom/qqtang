"""Marriage and wedding messages (docs/wedding-re.md §2), laid out as
internal/protocol/game/marriage.go encodes them. Counted strings go compact;
nicknames are 20-byte GBK slots."""

from __future__ import annotations

import struct
from dataclasses import dataclass

SPARK_COMMAND = 0x0104
SPARK_NOTIFY_COMMAND = 0x0105
ANSWER_SPARK_COMMAND = 0x0106
INFO_COMMAND = 0x0107
LOVE_WORD_COMMAND = 0x0108
DIVORCE_COMMAND = 0x0109
START_WEDDING_COMMAND = 0x010A
WEDDING_CONFIRM_COMMAND = 0x010B
ANSWER_WEDDING_COMMAND = 0x010C
CHANGE_MODE_COMMAND = 0x010D
MODE_NOTIFY_COMMAND = 0x010E

NICK_SLOT = 20
TEXT_MAX = 200
RESULT_INDECENT = 44  # RESPONSE_MODIFY_LOVEWORD: the love word was refused


def gbk(text: str, limit: int = TEXT_MAX) -> bytes:
    """GBK bytes cut before the character that would pass limit (the
    original strncpy's the script's GBK str into a 200-byte field)."""
    out = b""
    for char in str(text):
        encoded = char.encode("gbk", "replace")
        if len(out) + len(encoded) > limit:
            break
        out += encoded
    return out


def _text(data: bytes) -> str:
    return data.split(b"\0", 1)[0].decode("gbk", "replace")


class _Reader:
    def __init__(self, data: bytes) -> None:
        self.data, self.offset = data, 0

    def unpack(self, fmt: str) -> tuple:
        values = struct.unpack_from(">" + fmt, self.data, self.offset)
        self.offset += struct.calcsize(">" + fmt)
        return values

    def take(self, size: int) -> bytes:
        chunk = self.data[self.offset:self.offset + size]
        self.offset += size
        return chunk

    def text8(self) -> str:
        (length,) = self.unpack("B")
        return _text(self.take(length))

    def nick(self) -> str:
        return _text(self.take(NICK_SLOT))


@dataclass
class MarriageInfo:
    """RESPONSE_MARRIAGE_INFO without ResultID."""
    target_uin: int = 0
    marriage_uin: int = 0
    loyalty: int = 0
    level: int = 0
    level_value: int = 0
    age: int = 0
    love_word: str = ""
    nickname: str = ""
    spouse_nickname: str = ""
    ring_id: int = 0


# Requests ------------------------------------------------------------------

def build_spark(uin: int, target: int, word: str) -> bytes:
    text = gbk(word)
    return struct.pack(">IIB", uin, target, len(text)) + text


def build_answer_spark(uin: int, sparker: int, result: int) -> bytes:
    return struct.pack(">IIH", uin, sparker, result)


def build_info(uin: int, target: int) -> bytes:
    return struct.pack(">II", uin, target)


def build_love_word(uin: int, spouse: int, word: str) -> bytes:
    text = gbk(word)
    return struct.pack(">IIH", uin, spouse, len(text)) + text


def build_pair(uin: int, target: int) -> bytes:
    """REQUEST_DIVORCE."""
    return struct.pack(">II", uin, target)


def build_start_wedding(uin: int, target: int, mode: int) -> bytes:
    return struct.pack(">IIH", uin, target, mode)


def build_answer_wedding(uin: int, target: int, result: int) -> bytes:
    return struct.pack(">IIH", uin, target, result)


def build_change_mode(uin: int, mode: int) -> bytes:
    return struct.pack(">IH", uin, mode)


# Replies -------------------------------------------------------------------

def parse_spark_result(data: bytes) -> tuple[int, int, int, str]:
    """0x104 / 0x106: (result, uin, target, word)."""
    reader = _Reader(data)
    result, uin, target = reader.unpack("HII")
    return result, uin, target, reader.text8()


def parse_spark_notify(data: bytes) -> tuple[int, int, str, str]:
    """0x105: (sparker, target, sparker nickname, word)."""
    reader = _Reader(data)
    uin, target = reader.unpack("II")
    return uin, target, reader.nick(), reader.text8()


def parse_info(data: bytes) -> tuple[int, MarriageInfo]:
    reader = _Reader(data)
    result, target, spouse, loyalty, level, level_value, age, length = reader.unpack("HIIIHIIH")
    word = _text(reader.take(length))
    nickname, spouse_nickname = reader.nick(), reader.nick()
    (ring,) = reader.unpack("I")
    return result, MarriageInfo(target, spouse, loyalty, level, level_value, age, word, nickname, spouse_nickname, ring)


def parse_love_word_result(data: bytes) -> int:
    return struct.unpack_from(">IIH", data, 0)[2]


def parse_divorce(data: bytes) -> tuple[int, int, int]:
    """(uin, target, result)."""
    return struct.unpack_from(">IIH", data, 0)


def parse_start_wedding(data: bytes) -> tuple[int, int, str, str, int, str, int]:
    """0x10A: (uin, target, nickname, target nickname, result, word, mode)."""
    reader = _Reader(data)
    uin, target = reader.unpack("II")
    nickname, target_nickname = reader.nick(), reader.nick()
    (result,) = reader.unpack("H")
    word = reader.text8()
    (mode,) = reader.unpack("H")
    return uin, target, nickname, target_nickname, result, word, mode


def parse_wedding_confirm(data: bytes) -> tuple[int, int, str, str, str]:
    reader = _Reader(data)
    uin, target = reader.unpack("II")
    nickname, target_nickname = reader.nick(), reader.nick()
    return uin, target, nickname, target_nickname, reader.text8()


def parse_answer_wedding(data: bytes) -> tuple[int, int, str, str, int, str]:
    reader = _Reader(data)
    uin, target = reader.unpack("II")
    nickname, target_nickname = reader.nick(), reader.nick()
    (result,) = reader.unpack("H")
    return uin, target, nickname, target_nickname, result, reader.text8()


def parse_change_mode(data: bytes) -> tuple[int, int, str]:
    """0x10D: (uin, result, word)."""
    reader = _Reader(data)
    uin, result = reader.unpack("IH")
    return uin, result, reader.text8()


def parse_mode_notify(data: bytes) -> tuple[int, int]:
    return struct.unpack_from(">IH", data, 0)
