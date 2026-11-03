"""Room entry: REQUEST_CREATE_ROOM 0x006B, REQUEST_ENTER_ROOM 0x0068,
REQUEST_JOIN_ROOM 0x008A (quick join) and NOTIFY_ENTER_ROOM_OLD 0x008D.

Layouts follow internal/protocol/game/create_room.go, enter_room.go and
join_room.go; integers are big-endian and text is GBK.
"""

from __future__ import annotations

import dataclasses
import struct
import time

from .player import GAME_INFO_SIZE, ITEM_INFO_SIZE, NICKNAME_SLOT, PATTERN_POINT_SIZE, GameInfo, ItemInfo, Reader

CREATE_ROOM_COMMAND = 0x006B
ENTER_ROOM_COMMAND = 0x0068
JOIN_ROOM_COMMAND = 0x008A
ENTER_ROOM_NOTIFY_COMMAND = 0x008D

ROOM_NAME_SLOT = 20
PASSWORD_SLOT = 16
MAX_SEATS = 8
PET_INFO_MINIMUM = 38  # PET_BASE_INFO up to SkillCount; one byte per skill follows
ATTACH_SIZE = 12

# RoomPropertyFlag (CreateRoom / sc_modifyRoom "flag" argument).
PROPERTY_PASSWORD = 1 << 0
PROPERTY_FREE_RULE = 1 << 1

# RESPONSE_ENTER_ROOM_OLD seat status. The script compares ord(seatStatus)
# with 1 (open, shows nothing) and 2 (occupied); 0 is a closed seat.
SEAT_LOCKED, SEAT_OPEN, SEAT_OCCUPIED = 0, 1, 2

ENTER_ROOM_RESULTS = {
    9: "房间不存在",
    13: "房间已满",
    14: "密码错误",
    19: "游戏已经开始",
}


@dataclasses.dataclass
class RoomPlayer:
    uin: int
    nickname: str
    player_id: int
    team: int
    seat: int  # 1..8
    status: int  # 0 not ready, 1 ready
    gender: int
    icon_id: int
    identity: int
    game_info: GameInfo
    items: list[ItemInfo]
    kin_index: int
    kin_name: str
    kin_flag: bytes
    pet: bytes
    honor: int = 0
    spouse_uin: int = 0


@dataclasses.dataclass
class RoomSnapshot:
    result: int
    room_id: int
    room_name: str = ""
    local_team: int = 0
    local_seat: int = 0
    map_id: int = 0
    room_flag: int = 0
    owner_id: int = 0
    seat_status: list[int] = dataclasses.field(default_factory=lambda: [SEAT_LOCKED] * MAX_SEATS)
    players: list[RoomPlayer] = dataclasses.field(default_factory=list)
    game_type: int = 0
    background_id: int = 0
    wedding_mode_id: int = 0


def _slot(text: str, size: int) -> bytes:
    """GBK text in a fixed zero-padded slot, cut on a character boundary."""
    encoded = b""
    for char in text:
        piece = char.encode("gbk", "replace")
        if len(encoded) + len(piece) > size - 1:
            break
        encoded += piece
    return encoded.ljust(size, b"\0")


def _text(data: bytes) -> str:
    return data.split(b"\0", 1)[0].decode("gbk", "replace")


def build_create_room_request(uin: int, name: str, flag: int, password: str, game_type: int,
                              continue_id: int = 0, now: int | None = None) -> bytes:
    if password:
        flag |= PROPERTY_PASSWORD
    else:
        flag &= ~PROPERTY_PASSWORD
    stamp = int(time.time()) if now is None else now
    return (struct.pack(">II", uin, stamp & 0xFFFFFFFF) + _slot(name, ROOM_NAME_SLOT) + bytes([flag & 0x03])
            + _slot(password, PASSWORD_SLOT) + struct.pack(">BI", game_type, continue_id))


def parse_create_room_response(payload: bytes) -> tuple[int, int]:
    """Return (result, room ID); result 0 is success."""
    return struct.unpack_from(">HH", payload, 0)


def build_enter_room_request(uin: int, room_id: int, role_id: int, password: str, now: int | None = None) -> bytes:
    stamp = int(time.time()) if now is None else now
    return struct.pack(">IIHB", uin, stamp & 0xFFFFFFFF, room_id, role_id) + _slot(password, PASSWORD_SLOT)


def build_join_room_request(uin: int, game_type: int, now: int | None = None) -> bytes:
    stamp = int(time.time()) if now is None else now
    return struct.pack(">IIB", uin, stamp & 0xFFFFFFFF, game_type)


def read_room_player(reader: Reader) -> RoomPlayer:
    """PLAYER_INFO_IN_ROOM_OLD in its compact network form."""
    (uin,) = reader.unpack("I")
    nickname = _text(reader.take(NICKNAME_SLOT))
    player_id, team_and_seat, status, gender, icon_id, identity = reader.unpack("HBBBBI")
    game_info = GameInfo(*struct.unpack(">6IHBI4I", reader.take(GAME_INFO_SIZE)))
    (item_count,) = reader.unpack("H")
    items = [ItemInfo(*struct.unpack(">HIBBBBII", reader.take(ITEM_INFO_SIZE))) for _ in range(item_count)]
    kin_index, kin_name_length = reader.unpack("IH")
    kin_name = reader.take(kin_name_length).decode("gbk", "replace")
    kin_flag = reader.take(8)
    pet = reader.take(PET_INFO_MINIMUM)
    pet += reader.take(struct.unpack_from(">I", pet, 34)[0])
    return RoomPlayer(uin, nickname, player_id, team_and_seat >> 4, team_and_seat & 0x0F, status, gender, icon_id,
                      identity, game_info, items, kin_index, kin_name, kin_flag, pet)


def _skip_patterns(reader: Reader) -> None:
    (count,) = reader.unpack("B")
    reader.take(PATTERN_POINT_SIZE * count)


def parse_enter_room_response(payload: bytes, join: bool = False) -> RoomSnapshot:
    """RESPONSE_ENTER_ROOM_OLD, or RESPONSE_JOIN_ROOM_OLD when join is set
    (a 20-byte room name after RoomID and no WeddingModeID)."""
    reader = Reader(payload)
    result, room_id = reader.unpack("HH")
    room = RoomSnapshot(result, room_id)
    if join:
        room.room_name = _text(reader.take(ROOM_NAME_SLOT))
    if result != 0:
        return room
    team_and_seat, room.map_id, room.room_flag, room.owner_id, count = reader.unpack("BHBHB")
    room.local_team, room.local_seat = team_and_seat >> 4, team_and_seat & 0x0F
    room.seat_status = list(reader.take(MAX_SEATS))
    room.players = [read_room_player(reader) for _ in range(count)]
    for member in room.players:
        member.honor = reader.unpack("III")[0]
    room.game_type, room.background_id = reader.unpack("BI")
    for member in room.players:
        (member.spouse_uin,) = reader.unpack("I")
    if not join:
        (room.wedding_mode_id,) = reader.unpack("H")
    for _ in room.players:
        _skip_patterns(reader)
    if reader.remaining():
        raise ValueError(f"enter-room response has {reader.remaining()} trailing bytes")
    return room


def parse_enter_room_notify(payload: bytes) -> tuple[int, RoomPlayer]:
    """NOTIFY_ENTER_ROOM_OLD: the joining player, then RoomID, attach and spouse."""
    reader = Reader(payload)
    member = read_room_player(reader)
    room_id, member.honor, _, _, member.spouse_uin = reader.unpack("HIIII")
    return room_id, member


# In-room requests and notifications (leave_room.go, change_role.go,
# change_team.go, ready.go, modify_room_info.go, modify_room.go,
# seat_status.go, chat.go, kick_off_player.go, start_game.go). Rejected
# requests get no reply except KickOffPlayer and StartGame, which carry a
# result code.

LEAVE_ROOM_COMMAND = 0x0069
ROOM_CHAT_COMMAND = 0x006D
KICK_OFF_PLAYER_COMMAND = 0x006F
CHANGE_TEAM_COMMAND = 0x0070
CHANGE_ROLE_COMMAND = 0x0071
READY_COMMAND = 0x0072
CANCEL_READY_COMMAND = 0x0073
KICK_OFF_NOTIFY_COMMAND = 0x0074
MODIFY_ROOM_INFO_COMMAND = 0x0078
CHANGE_ROOM_TYPE_COMMAND = 0x00D0
CHANGE_ROOM_TYPE_NOTIFY_COMMAND = 0x00D1
MODIFY_ROOM_INFO_NOTIFY_COMMAND = 0x0079
CHANGE_TEAM_NOTIFY_COMMAND = 0x007A
CHANGE_ROLE_NOTIFY_COMMAND = 0x007B
READY_NOTIFY_COMMAND = 0x007C
SET_SEAT_STATUS_COMMAND = 0x007F
SET_SEAT_STATUS_NOTIFY_COMMAND = 0x0080
START_GAME_COMMAND = 0x0082
GAME_BEGIN_NOTIFY_COMMAND = 0x0089
LEAVE_ROOM_NOTIFY_COMMAND = 0x008E
ROOM_CHAT_NOTIFY_COMMAND = 0x0093
MODIFY_ROOM_COMMAND = 0x0098
MODIFY_ROOM_NOTIFY_COMMAND = 0x0099
PLAYER_PETS_COMMAND = 0x00D4  # sent before create/enter results; pet.py keeps the cache

# SET_SEAT_STATUS uses its own numbering (1 open, 2 locked).
SET_SEAT_OPEN, SET_SEAT_LOCKED = 1, 2
CHAT_SYSTEM_SOURCE = 0xFFFF
CHAT_MAX = 500

# MODIFY_ROOM change mask.
MODIFY_NAME = 1 << 0
MODIFY_PASSWORD_PRESENT = 1 << 1
MODIFY_PASSWORD_CHANGED = 1 << 2
MODIFY_PASSWORD_ENABLED = 1 << 3

START_GAME_RESULTS = {
    0xF001: "人数不足，无法开始游戏",
    0xF002: "还有玩家没有准备",
    0xF003: "队伍分配不合法",
    0xF004: "缺少必需的道具",
}


def _stamp(uin: int, now: int | None) -> bytes:
    return struct.pack(">II", uin, (int(time.time()) if now is None else now) & 0xFFFFFFFF)


# Chat-room backgrounds (internal/protocol/game/chat_background.go), route 4.
BACKGROUND_LIST_COMMAND = 0x00FF
USE_BACKGROUND_COMMAND = 0x0100
USE_BACKGROUND_NOTIFY_COMMAND = 0x0101
BACKGROUND_ROUTE = 4


def build_background_request(uin: int, value: int = 0, now: int | None = None) -> bytes:
    """BackgroundList (value 0, reserved) and UseBackground (value = ID)."""
    return _stamp(uin, now) + struct.pack(">I", value)


def parse_background_list(payload: bytes) -> list[int]:
    (count,) = struct.unpack_from(">I", payload, 8)
    return list(struct.unpack_from(f">{count}I", payload, 12))


def parse_background_notify(payload: bytes) -> tuple[int, int]:
    uin, _time, background = struct.unpack_from(">III", payload, 0)
    return uin, background


def build_simple_request(uin: int, now: int | None = None) -> bytes:
    """LeaveRoom, Ready, CancelReady and StartGame carry only UIN and time."""
    return _stamp(uin, now)


def build_change_role_request(uin: int, role_id: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">H", role_id)


def build_change_team_request(uin: int, team: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">H", team)


def build_modify_room_info_request(uin: int, map_id: int, game_type: int, continue_id: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">HBI", map_id, game_type, continue_id)


def build_change_room_type_request(uin: int, game_type: int, continue_id: int = 0, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">BI", game_type, continue_id)


def parse_change_room_type_notify(payload: bytes) -> tuple[int, int]:
    """(game type, continue ID)."""
    return struct.unpack_from(">BI", payload, 0)


def build_modify_room_request(uin: int, changes: int, name: str, flag: int, password: str, game_type: int,
                              continue_id: int = 0, now: int | None = None) -> bytes:
    return (_stamp(uin, now) + struct.pack(">I", changes) + _slot(name, ROOM_NAME_SLOT) + bytes([flag & 0x03])
            + _slot(password, PASSWORD_SLOT) + struct.pack(">BI", game_type, continue_id))


def build_set_seat_status_request(uin: int, seat: int, status: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">BB", seat, status)


def build_kick_request(uin: int, player_id: int, player_uin: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">HI", player_id, player_uin)


def build_room_chat_request(uin: int, text: str, destination: int = 0, now: int | None = None) -> bytes:
    content = text.encode("gbk", "replace")[:CHAT_MAX]
    return _stamp(uin, now) + struct.pack(">HH", destination, len(content)) + content


def parse_result(payload: bytes) -> int:
    return struct.unpack_from(">H", payload, 0)[0]


def parse_leave_notify(payload: bytes) -> tuple[int, int, int]:
    """(leaving PlayerID, new owner PlayerID, map ID)."""
    player_id, owner_id, _arbitrator, map_id = struct.unpack_from(">HHHH", payload, 0)
    return player_id, owner_id, map_id


def parse_member_notify(payload: bytes) -> tuple[int, int, int]:
    """ChangeRole, ChangeTeam and ReadyState notifications: (UIN, PlayerID, value)."""
    return struct.unpack_from(">IHB", payload, 0)


def parse_modify_room_info_notify(payload: bytes) -> tuple[int, int, int]:
    """(map ID, game type, continue ID)."""
    return struct.unpack_from(">HBI", payload, 0)


def parse_modify_room_notify(payload: bytes) -> tuple[str, int, str]:
    """(room name, RoomPropertyFlag, password)."""
    return _text(payload[4:24]), payload[24], _text(payload[25:41])


def parse_seat_status_notify(payload: bytes) -> tuple[int, int]:
    """(seat 1..8, SET_SEAT_* status)."""
    return payload[0], payload[1]


def parse_room_chat_notify(payload: bytes) -> tuple[int, int, str]:
    """(source PlayerID, destination PlayerID, message); the message already
    starts with "<nickname>说:" unless the source is CHAT_SYSTEM_SOURCE."""
    source, destination, length = struct.unpack_from(">HHH", payload, 0)
    return source, destination, payload[6:6 + length].decode("gbk", "replace")


def parse_kick_notify(payload: bytes) -> tuple[int, int, int]:
    """(reason, room ID, kicked UIN)."""
    return struct.unpack_from(">HHI", payload, 0)
