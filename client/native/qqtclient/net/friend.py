"""Friends and FIND_FRIEND (internal/protocol/game/friend.go, find_friend.go).

Requests from the client (all begin with u32 UIN, u32 time):
  0x00A2 friend list: nothing more;
  0x00A3 add friend: u32 target UIN, u16 length, word;
  0x00A4 remove friend: u32 target UIN;
  0x0067 find friend: u32 target UIN.
0x00A6 answers an application: u16 result, u32 requester UIN, u32 time, u32
responder UIN (the sender).

Replies and pushes:
  0x00A2 u16 result, u32 owner, u16 maximum, u16 count, then 200 u32 UINs
         (the login roster is pushed in the same layout);
  0x00A3 an application forwarded to its target: the request layout with the
         requester's UIN first;
  0x00A4 u16 result, u32 owner, u32 removed UIN;
  0x00A5 one friend's details (163 bytes, FRIEND_INFO);
  0x00A6 u16 result, u32 requester, u32 time, u32 responder: the answerer's
         acknowledgement, the requester's result, or the reply to an
         application whose target is offline;
  0x0067 RESPONSE_FIND_FRIEND.

The original client (QQTSection.dll RequestAddFriend 0x1000F51E) writes its
own nickname into a 21-byte slot at the start of the word and the message
after it; the receiver (0x10017FCA) reads the name from the first 20 bytes
and the message from byte 21. The server relays the word unchanged.
"""

from __future__ import annotations

import dataclasses
import struct
import time

from .player import GAME_INFO_SIZE, ITEM_INFO_SIZE, PATTERN_POINT_SIZE, GameInfo, ItemInfo

FIND_FRIEND_COMMAND = 0x0067
FRIEND_LIST_COMMAND = 0x00A2
ADD_FRIEND_COMMAND = 0x00A3
REMOVE_FRIEND_COMMAND = 0x00A4
FRIEND_UPDATE_COMMAND = 0x00A5
ANSWER_FRIEND_COMMAND = 0x00A6

RESULT_SUCCESS = 0
RESULT_REJECTED = 1
FRIEND_LIST_SLOTS = 200
WORD_NAME_SLOT = 21
WORD_MAXIMUM = 512
NICKNAME_SLOT = 20
EXT_ITEM_SLOTS = 30
FRIEND_UPDATE_SIZE = 163
FIND_STATUS_OFFLINE = 0
FIND_STATUS_ONLINE = 1  # online, not in a room
FIND_STATUS_IN_ROOM = 2  # inferred: the only status QQTSection 0x10010D83 sends into the room
FIND_STATUS_IN_GAME = 3


@dataclasses.dataclass
class FriendList:
    result: int
    owner: int
    maximum: int
    friends: list[int]


@dataclasses.dataclass
class FriendApplication:
    requester: int
    target: int
    nickname: str
    text: str


@dataclasses.dataclass
class FriendUpdate:
    owner: int
    uin: int
    points: int
    ext_item_ids: list[int]
    online: bool
    nickname: str
    gender: int
    identity: int
    ext_points: int


@dataclasses.dataclass
class FriendAnswer:
    result: int
    requester: int
    responder: int


@dataclasses.dataclass
class FoundPlayer:
    result: int
    uin: int
    nickname: str = ""
    player_id: int = 0
    gender: int = 0
    icon_id: int = 0
    identity: int = 0
    server_id: int = 0
    dialog_id: int = 0
    section_id: int = 0
    room_id: int = 0
    room_name: str = ""
    status: int = FIND_STATUS_OFFLINE
    game_info: GameInfo | None = None
    items: list[ItemInfo] = dataclasses.field(default_factory=list)
    kin_index: int = 0
    kin_name: str = ""
    kin_flag: bytes = b"\xff" * 8
    honor: int = 0


def _decode(data: bytes) -> str:
    return data.split(b"\0", 1)[0].decode("gbk", "replace")


def _stamp(uin: int, now: int | None) -> bytes:
    return struct.pack(">II", uin, (int(time.time()) if now is None else now) & 0xFFFFFFFF)


def build_friend_list(uin: int, now: int | None = None) -> bytes:
    return _stamp(uin, now)


def build_add_friend(uin: int, target: int, nickname: str, text: str, now: int | None = None) -> bytes:
    name = nickname.encode("gbk", "replace")[:NICKNAME_SLOT].ljust(WORD_NAME_SLOT, b"\0")
    word = (name + text.encode("gbk", "replace"))[:WORD_MAXIMUM]
    return _stamp(uin, now) + struct.pack(">IH", target, len(word)) + word


def build_remove_friend(uin: int, target: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">I", target)


def build_answer_friend(result: int, requester: int, responder: int, now: int | None = None) -> bytes:
    return struct.pack(">HIII", result, requester, (int(time.time()) if now is None else now) & 0xFFFFFFFF, responder)


def build_find_friend(uin: int, target: int, now: int | None = None) -> bytes:
    return _stamp(uin, now) + struct.pack(">I", target)


def parse_friend_list(payload: bytes) -> FriendList:
    result, owner, maximum, count = struct.unpack_from(">HIHH", payload, 0)
    slots = struct.unpack_from(f">{FRIEND_LIST_SLOTS}I", payload, 10)
    return FriendList(result, owner, maximum, [uin for uin in slots[:count] if uin])


def parse_application(payload: bytes) -> FriendApplication:
    requester, _time, target, length = struct.unpack_from(">IIIH", payload, 0)
    word = payload[14:14 + length]
    return FriendApplication(requester, target, _decode(word[:NICKNAME_SLOT]), _decode(word[WORD_NAME_SLOT:]))


def parse_friend_update(payload: bytes) -> FriendUpdate:
    if len(payload) != FRIEND_UPDATE_SIZE:
        raise ValueError(f"friend update has {len(payload)} bytes, want {FRIEND_UPDATE_SIZE}")
    owner, uin, points, count = struct.unpack_from(">IIIB", payload, 0)
    items = struct.unpack_from(f">{EXT_ITEM_SLOTS}I", payload, 13)
    online = payload[133]
    nickname = _decode(payload[134:134 + NICKNAME_SLOT])
    gender, identity, ext_points = struct.unpack_from(">BII", payload, 154)
    return FriendUpdate(owner, uin, points, list(items[:count]), bool(online), nickname, gender, identity, ext_points)


def parse_remove_result(payload: bytes) -> tuple[int, int]:
    result, _owner, target = struct.unpack_from(">HII", payload, 0)
    return result, target


def parse_answer(payload: bytes) -> FriendAnswer:
    result, requester, _time, responder = struct.unpack_from(">HIII", payload, 0)
    return FriendAnswer(result, requester, responder)


def parse_find_friend(payload: bytes) -> FoundPlayer:
    result, uin = struct.unpack_from(">HI", payload, 0)
    if result != 0:
        return FoundPlayer(result, uin)
    offset = 6
    nickname = _decode(payload[offset:offset + NICKNAME_SLOT])
    offset += NICKNAME_SLOT
    player_id, gender, icon_id, identity, server_id, dialog_id, section_id, room_id = struct.unpack_from(">HBBIIHHH", payload, offset)
    offset += 18
    room_name = _decode(payload[offset:offset + NICKNAME_SLOT])
    offset += NICKNAME_SLOT
    status = payload[offset]
    offset += 1
    game_info = GameInfo(*struct.unpack_from(">6IHBI4I", payload, offset))
    offset += GAME_INFO_SIZE
    (item_count,) = struct.unpack_from(">H", payload, offset)
    offset += 2
    items = [ItemInfo(*struct.unpack_from(">HIBBBBII", payload, offset + ITEM_INFO_SIZE * index)) for index in range(item_count)]
    offset += ITEM_INFO_SIZE * item_count
    kin_index, kin_name_length = struct.unpack_from(">IH", payload, offset)
    offset += 6
    kin_name = payload[offset:offset + kin_name_length].decode("gbk", "replace")
    offset += kin_name_length
    kin_flag = payload[offset:offset + 8]
    (honor,) = struct.unpack_from(">I", payload, offset + 8)
    offset += 12
    offset += 1 + PATTERN_POINT_SIZE * payload[offset]
    if offset != len(payload):
        raise ValueError(f"find-friend reply has {len(payload) - offset} trailing bytes")
    return FoundPlayer(result, uin, nickname, player_id, gender, icon_id, identity, server_id, dialog_id, section_id,
                       room_id, room_name, status, game_info, items, kin_index, kin_name, kin_flag, honor)
