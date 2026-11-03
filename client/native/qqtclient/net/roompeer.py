"""Room-scene messages (docs/chatroom-move-re.md §3, §4): ROOM_MSG_DATA
(schema 0x0BBA) carrying ROOM_PLAYER_MOVE_INFO 0x0BB8 and
ROOM_PLAYER_PUT_BOMB 0x0BB9, sent in the TCP 0x0065 room-fast packet and
received as QQTPPP Type-2 UDP datagrams after a Type-1 presence."""

from __future__ import annotations

import socket
import struct

from . import qqtea
from .game import LOCAL_KEY, MARKER

MOVE_ID = 0x0BB8
PUT_BOMB_ID = 0x0BB9
ROOM_FAST_COMMAND = 0x0065
UDP_HEADER = 18
TYPE_PRESENCE, TYPE_MULTICAST = 1, 2


def move_body(seat: int, x: int, y: int, direction: int, speed: float) -> bytes:
    """17 bytes, little-endian memory (0x00457CF0)."""
    return struct.pack("<BiiIf", seat, x, y, direction, speed)


def bomb_body(seat: int, x: int, y: int) -> bytes:
    return struct.pack("<Bii", seat, x, y)


def room_msg_data(data_id: int, body: bytes, sequence: int, tick: int) -> bytes:
    """Big-endian Time, DataID, DataLen, Sequence, then the raw body."""
    return struct.pack(">IHHI", tick & 0xFFFFFFFF, data_id, len(body), sequence & 0xFFFFFFFF) + body


def parse_room_msg_data(data: bytes) -> tuple[int, tuple] | None:
    if len(data) < 12:
        return None
    _tick, data_id, length, _sequence = struct.unpack_from(">IHHI", data, 0)
    body = data[12:12 + length]
    if data_id == MOVE_ID and length == 17 and len(body) == 17:
        return data_id, struct.unpack("<BiiIf", body)
    if data_id == PUT_BOMB_ID and length == 9 and len(body) == 9:
        return data_id, struct.unpack("<Bii", body)
    return None


def build_room_fast(uin: int, route_sequence: int, recipients: list[int], messages: list[bytes]) -> bytes:
    """The TCP 0x0065 packet (internal/protocol/game/room_fast_packet.go):
    a "0065 N ids" ST instead of the local ST, then QQTEA(LocalKey) over a
    14-byte inner header (route 1, section 1) and the entries."""
    st = struct.pack(">HB", ROOM_FAST_COMMAND, len(recipients)) + b"".join(struct.pack(">H", player) for player in recipients)
    inner = struct.pack(">HIHHHH", 0, 0, route_sequence, 1, MARKER, 1) + bytes([len(messages)])
    for message in messages:
        inner += struct.pack(">IIH", 0, 0, len(message)) + message
    ciphertext = qqtea.encrypt(inner, LOCAL_KEY)
    header = struct.pack(">IIHHIBB", 18 + len(st) + len(ciphertext), 0, route_sequence, 0xFFFF, uin, 1, len(st))
    return header + st + ciphertext


def checksum(data: bytes) -> int:
    """QQTPPP's one's-complement sum of little-endian words."""
    total = sum(data[i] | data[i + 1] << 8 for i in range(0, len(data) - 1, 2))
    if len(data) % 2:
        total += data[-1]
    total = (total & 0xFFFF) + (total >> 16)
    total = (total & 0xFFFF) + (total >> 16)
    return ~total & 0xFFFF


def build_presence(packet_number: int, player_id: int, uin: int, address: tuple[str, int]) -> bytes:
    endpoint = socket.inet_aton(address[0]) + struct.pack(">H", address[1])
    data = bytearray(struct.pack(">HBIHIHHB", UDP_HEADER + 6, 0, packet_number, player_id, uin, TYPE_PRESENCE, 0, 6) + endpoint)
    struct.pack_into(">H", data, 15, checksum(bytes(data)))
    return bytes(data)


def parse_datagram(data: bytes) -> tuple[int, int, int, int, bytes] | None:
    """(type, packet number, sender player, sender UIN, Type-2 data) of a
    valid datagram, else None."""
    if len(data) < UDP_HEADER or struct.unpack_from(">H", data, 0)[0] != len(data) or checksum(data) != 0:
        return None
    _flags, number, player, uin, kind, _sum, route_bytes = struct.unpack_from(">BIHIHHB", data, 2)
    payload = data[UDP_HEADER:]
    if kind == TYPE_MULTICAST:
        return kind, number, player, uin, payload[route_bytes:]
    return kind, number, player, uin, payload
