"""Native .qbv battle recordings (Client.exe 0x005A35FB writes, 0x005A36CE plays).

A zlib stream whose body is little-endian: "vaqq", version 1, four header
DWORDs (an unidentified word, the recorder's QQ number, the recorder's
PlayerID, the player count), PlayerCount + 2 bootstrap objects (GAME_BEGIN
0x0FA1, GAME_OVER 0x0FBB, one PLAYER_INFO_IN_ROOM_OLD 0x042A per player) as
(length, schema, data), then the event count and each battle message as
(time, schema, length, data, three DWORDs). Message bodies keep their
big-endian network form. internal/game/qbv is the Go decoder the sidecar
replays with.
"""

from __future__ import annotations

import dataclasses
import pathlib
import struct
import zlib

from .net.player import Reader
from .net.room import RoomPlayer, read_room_player

MAGIC = b"vaqq"
VERSION = 1
SCHEMA_GAME_BEGIN = 0x0FA1
SCHEMA_GAME_OVER = 0x0FBB
SCHEMA_PLAYER = 0x042A
MAX_PLAYERS = 8


@dataclasses.dataclass
class Event:
    time_ms: int
    schema: int
    data: bytes
    opaque: tuple[int, int, int] = (0, 0, 0)


@dataclasses.dataclass
class Recording:
    local_uin: int
    local_id: int
    begin: bytes  # GAME_BEGIN as GameBeginData, without the 2-byte length prefix
    over: bytes  # NOTIFY_GAME_OVER body
    players: list[RoomPlayer]
    events: list[Event]
    unknown: int = 0  # header word 0

    @property
    def duration_ms(self) -> int:
        return self.events[-1].time_ms if self.events else 0


def read(path: pathlib.Path) -> Recording:
    data = zlib.decompress(path.read_bytes())
    if data[:4] != MAGIC:
        raise ValueError(f"{path.name} is not a QQTang recording")
    version, unknown, local_uin, local_id, count = struct.unpack_from("<5I", data, 4)
    if version != VERSION or not 0 < count <= MAX_PLAYERS:
        raise ValueError(f"{path.name}: version {version}, {count} players")
    offset = 24
    bootstrap: list[tuple[int, bytes]] = []
    for _ in range(count + 2):
        length, schema = struct.unpack_from("<II", data, offset)
        bootstrap.append((schema, data[offset + 8:offset + 8 + length]))
        offset += 8 + length
    (event_count,) = struct.unpack_from("<I", data, offset)
    offset += 4
    events = []
    for _ in range(event_count):
        time_ms, schema, length = struct.unpack_from("<IHH", data, offset)
        body = data[offset + 8:offset + 8 + length]
        opaque = struct.unpack_from("<3I", data, offset + 8 + length)
        events.append(Event(time_ms, schema, body, opaque))
        offset += 8 + length + 12
    if offset != len(data):
        raise ValueError(f"{path.name} has {len(data) - offset} trailing bytes")
    by_schema = {schema: body for schema, body in bootstrap[:2]}
    players = [read_room_player(Reader(body)) for schema, body in bootstrap if schema == SCHEMA_PLAYER]
    return Recording(local_uin, local_id, by_schema.get(SCHEMA_GAME_BEGIN, b""), by_schema.get(SCHEMA_GAME_OVER, b""),
                     players, events, unknown)


def write(path: pathlib.Path, recording: Recording, player_records: list[bytes]) -> None:
    """player_records are encoded PLAYER_INFO_IN_ROOM_OLD bodies, in the
    order the recording lists its players."""
    body = bytearray(MAGIC)
    body += struct.pack("<5I", VERSION, recording.unknown, recording.local_uin, recording.local_id, len(player_records))
    for schema, data in [(SCHEMA_GAME_BEGIN, recording.begin), (SCHEMA_GAME_OVER, recording.over)] + \
            [(SCHEMA_PLAYER, record) for record in player_records]:
        body += struct.pack("<II", len(data), schema) + data
    body += struct.pack("<I", len(recording.events))
    for event in recording.events:
        body += struct.pack("<IHH", event.time_ms, event.schema, len(event.data)) + event.data
        body += struct.pack("<3I", *event.opaque)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(zlib.compress(bytes(body)))
