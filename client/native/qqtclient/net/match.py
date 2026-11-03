"""In-match TCP messages (internal/protocol/game/game_event.go, game_result.go,
hello.go). Scene traffic itself goes over UDP through the battle sidecar;
the client only forwards these and sends the sidecar's REQUEST_PLAY copies.
"""

from __future__ import annotations

import dataclasses
import struct
import time

REQUEST_PLAY_COMMAND = 0x0083
GAME_EVENT_NOTIFY_COMMAND = 0x0084
NOTIFY_UDP_OK_COMMAND = 0x0087
HELLO_COMMAND = 0x0088

# NOTIFY_GAME_EVENT schemas the client handles itself.
SCHEMA_GAME_OVER = 0x0FBB
# NOTIFY_GAME_NEXTMAP: an adventure stage's successor; the body is the next
# stage's GAME_BEGIN_DATA without the uint16 length prefix (0x5A2452).
SCHEMA_GAME_NEXTMAP = 0x1177
SCHEMA_CHANGE_ARBITRATOR = 0x0FB1

RESULT_LOSS, RESULT_DRAW, RESULT_WIN = 0, 1, 2


@dataclasses.dataclass
class GameEvent:
    room_id: int
    schema: int
    body: bytes


@dataclasses.dataclass
class PlayerResult:
    player_id: int
    result: int  # RESULT_*
    point: int
    counts: list[int]  # kills, rescues, reward, mode-specific
    scores: list[int]


@dataclasses.dataclass
class GameOver:
    time_ms: int
    results: list[PlayerResult]
    game_mode: int


def parse_game_event_notify(payload: bytes) -> GameEvent:
    """NOTIFY_GAME_EVENT: u16 room, u32 sequence, u32 length, then the game
    data (u32 little-endian schema and its body)."""
    room_id, _sequence, length = struct.unpack_from(">HII", payload, 0)
    if length < 4 or 10 + length > len(payload):
        raise ValueError(f"game event length {length} does not fit {len(payload)} bytes")
    (schema,) = struct.unpack_from("<I", payload, 10)
    return GameEvent(room_id, schema, payload[14:10 + length])


def parse_game_over(body: bytes) -> GameOver:
    time_ms, count = struct.unpack_from(">IB", body, 0)
    offset = 5
    results = []
    for _ in range(count):
        player_id, result, _remark, point, fields = struct.unpack_from(">HBIIB", body, offset)
        offset += 12
        counts = list(struct.unpack_from(f">{fields}I", body, offset))
        offset += 4 * fields
        scores = list(struct.unpack_from(f">{fields}I", body, offset))
        offset += 4 * fields
        results.append(PlayerResult(player_id, result, point, counts, scores))
    game_mode = body[offset] if offset < len(body) else 0
    return GameOver(time_ms, results, game_mode)


def build_hello(uin: int, now: int | None = None) -> bytes:
    """Keep-alive: u32 UIN, u32 time, u16 info length (no info)."""
    return struct.pack(">IIH", uin, (int(time.time()) if now is None else now) & 0xFFFFFFFF, 0)
