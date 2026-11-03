"""Lobby lists: REQUEST_ROOM 0x0066 / PUSHROOMINFO 0x008C and REQUEST_PLAYER 0x006A.

Layouts follow internal/protocol/game/room.go and player.go; integers are
big-endian and names are GBK.
"""

from __future__ import annotations

import dataclasses
import struct
import time

from .player import GAME_INFO_SIZE, PATTERN_POINT_SIZE, GameInfo

ROOM_LIST_COMMAND = 0x0066
PLAYER_LIST_COMMAND = 0x006A
ROOM_PUSH_COMMAND = 0x008C

# QQTSection 0x1003D04E: operation 1 lists every room ("所有房间"), 2 the
# map filter at +0x514C (0 every map, the default refresh every five seconds).
ROOM_LIST_ALL_ROOMS = 1
ROOM_LIST_REFRESH = 2
ROOM_FILTER_ALL_ROOMS = 0

ROOM_FLAG_PREPARING = 1 << 0
ROOM_FLAG_PASSWORD = 1 << 1
ROOM_FLAG_FREE_RULE = 1 << 2
ROOM_FLAG_VIP = 1 << 3


@dataclasses.dataclass
class Room:
    name: str
    room_id: int
    flag: int
    map_id: int
    players: int
    capacity: int
    game_type: int
    continue_id: int


@dataclasses.dataclass
class Player:
    uin: int
    nickname: str
    player_id: int
    gender: int
    icon_id: int
    identity: int
    game_info: GameInfo
    ext_item_ids: list[int]
    kin_index: int
    kin_name: str
    kin_flag: bytes
    honor: int = 0


def build_room_list_request(uin: int, start_room_id: int, number: int, game_type: int,
                            game_mode: int = ROOM_FILTER_ALL_ROOMS, now: int | None = None,
                            operation: int = ROOM_LIST_REFRESH) -> bytes:
    stamp = int(time.time()) if now is None else now
    return struct.pack(">IIBHHBB", uin, stamp & 0xFFFFFFFF, operation, start_room_id, number, game_mode, game_type)


def build_player_list_request(uin: int, start_player_id: int = 0, number: int = 30) -> bytes:
    return struct.pack(">IIHH", uin, int(time.time()) & 0xFFFFFFFF, start_player_id, number)


def _parse_room(data: bytes, offset: int) -> tuple[Room, int]:
    name_length = data[offset]
    name = data[offset + 1:offset + 1 + name_length].split(b"\0", 1)[0].decode("gbk", "replace")
    offset += 1 + name_length
    room_id, flag, map_id, population, game_type, continue_id = struct.unpack_from(">HBHBBI", data, offset)
    # NumOfPlayer packs the current count in the high nibble and the capacity in the low nibble.
    return Room(name, room_id, flag, map_id, population >> 4, population & 0x0F, game_type, continue_id), offset + 11


def parse_room_list(payload: bytes) -> list[Room]:
    result, _operation, count = struct.unpack_from(">HBH", payload, 0)
    if result != 0:
        raise ValueError(f"room list result {result}")
    offset, rooms = 5, []
    for _ in range(count):
        room, offset = _parse_room(payload, offset)
        rooms.append(room)
    return rooms


def parse_room_push(payload: bytes) -> list[Room]:
    (count,) = struct.unpack_from(">H", payload, 0)
    offset, rooms = 2, []
    for _ in range(count):
        room, offset = _parse_room(payload, offset)
        rooms.append(room)
    return rooms


def parse_player_list(payload: bytes) -> list[Player]:
    count = payload[2]
    offset, players = 3, []
    for _ in range(count):
        uin, = struct.unpack_from(">I", payload, offset)
        nickname = payload[offset + 4:offset + 24].split(b"\0", 1)[0].decode("gbk", "replace")
        player_id, gender, icon_id, identity = struct.unpack_from(">HBBI", payload, offset + 24)
        offset += 32
        game_info = GameInfo(*struct.unpack_from(">6IHBI4I", payload, offset))
        offset += GAME_INFO_SIZE
        ext_count = payload[offset]
        ext_item_ids = list(struct.unpack_from(f">{ext_count}I", payload, offset + 1))
        offset += 1 + 4 * ext_count
        kin_index, kin_name_length = struct.unpack_from(">IH", payload, offset)
        offset += 6
        kin_name = payload[offset:offset + kin_name_length].decode("gbk", "replace")
        offset += kin_name_length
        kin_flag = payload[offset:offset + 8]
        offset += 8
        players.append(Player(uin, nickname, player_id, gender, icon_id, identity, game_info, ext_item_ids, kin_index, kin_name, kin_flag))
    for player in players:
        player.honor, _ = struct.unpack_from(">II", payload, offset)
        offset += 8
    for _ in players:
        offset += 1 + PATTERN_POINT_SIZE * payload[offset]
    if offset != len(payload):
        raise ValueError(f"player list has {len(payload) - offset} trailing bytes")
    return players
