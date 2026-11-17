"""REQUEST_LOGIN (0x0064) and RESPONSE_LOGIN schema 0x07D5; REQUEST_LOGOUT
(0x0065, schema 0x03EE).

Layouts follow internal/protocol/game/login.go and login_response.go; all
integers are big-endian.
"""

from __future__ import annotations

import dataclasses
import struct
import time

NICKNAME_SLOT = 20
GAME_INFO_SIZE = 47
ITEM_INFO_SIZE = 18
PATTERN_POINT_SIZE = 11


@dataclasses.dataclass
class GameInfo:
    wins: int
    losses: int
    draws: int
    organization_id: int
    points: int
    money: int
    degree: int
    role_id: int
    pet_id: int
    extended_wins: int
    extended_losses: int
    extended_draws: int
    extended_points: int


@dataclasses.dataclass
class ItemInfo:
    item_id: int
    quantity: int
    status: int
    role_id: int
    effect: int
    color: int
    buy_time: int
    available_period: int


@dataclasses.dataclass
class LoginResponse:
    result: int
    player_id: int
    uin: int
    identity: int
    section_id: int
    key_game_data: bytes
    room_count: int
    minimum_room_id: int
    game_info: GameInfo
    items: list[ItemInfo]
    section_mode: int
    extra_section_mode_info: int
    section_identity: int
    reason: bytes
    honor: int
    task_count: int
    task_grade: int
    task_game_count: int
    task_game_finished: int
    patterns: list[tuple[int, int, int, int]]


def build_login_request(uin: int, nickname: str, gender: int, section_id: int, role_id: int, client_version: int) -> bytes:
    name = nickname.encode("gbk", "replace")[:NICKNAME_SLOT - 1].ljust(NICKNAME_SLOT, b"\0")
    payload = struct.pack(">II", uin, int(time.time()) & 0xFFFFFFFF) + name
    payload += struct.pack(">BBHHIBIH", gender, 0, 0, section_id, 0, role_id, client_version, 0)
    # No CONFIG_FILE_INFO entries; ClientType and CSVersion follow the empty list.
    payload += struct.pack(">II", 0, 0)
    return payload


LOGOUT_COMMAND = 0x0065


def build_logout_request(uin: int, now: int | None = None) -> bytes:
    """REQUEST_LOGOUT as QQTSection 0x10019E0D fills it: u32 UIN, u32 time,
    u8 statistic item count and 5-byte items (internal/protocol/game/
    logout.go). The items come from a statistics service only when its flag
    (+0xC610) is set; the host has none and sends zero."""
    return struct.pack(">IIB", uin, (int(time.time()) if now is None else now) & 0xFFFFFFFF, 0)


class Reader:
    def __init__(self, data: bytes):
        self.data, self.offset = data, 0

    def take(self, size: int) -> bytes:
        if self.offset + size > len(self.data):
            raise ValueError(f"payload truncated at {self.offset}+{size} of {len(self.data)}")
        value = self.data[self.offset:self.offset + size]
        self.offset += size
        return value

    def unpack(self, fmt: str) -> tuple:
        return struct.unpack(">" + fmt, self.take(struct.calcsize(">" + fmt)))

    def remaining(self) -> int:
        return len(self.data) - self.offset


def parse_login_response(payload: bytes) -> LoginResponse:
    reader = Reader(payload)
    result, player_id, uin, identity, section_id, key_length = reader.unpack("HHIIHB")
    key = reader.take(32)[:key_length]
    room_count, minimum_room_id = reader.unpack("HH")
    game_info = GameInfo(*struct.unpack(">6IHBI4I", reader.take(GAME_INFO_SIZE)))
    (item_count,) = reader.unpack("H")
    items = [ItemInfo(*struct.unpack(">HIBBBBII", reader.take(ITEM_INFO_SIZE))) for _ in range(item_count)]
    section_mode, extra_section_mode_info, section_identity, reason_length = reader.unpack("BIIB")
    reason = reader.take(reason_length)
    honor, task_count, task_grade, task_game_count, task_game_finished, pattern_count = reader.unpack("IHHHHB")
    patterns = [struct.unpack(">BIHI", reader.take(PATTERN_POINT_SIZE)) for _ in range(pattern_count)]
    if reader.offset != len(payload):
        raise ValueError(f"login response has {len(payload) - reader.offset} trailing bytes")
    return LoginResponse(result, player_id, uin, identity, section_id, key, room_count, minimum_room_id, game_info, items,
                         section_mode, extra_section_mode_info, section_identity, reason, honor, task_count, task_grade,
                         task_game_count, task_game_finished, patterns)
