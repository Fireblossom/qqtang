"""The battle rules sidecar (cmd/qqt-battle-sidecar) as a child process.

It speaks JSON lines: one init command, then one step command per frame.
Build it with tools/build-sidecar.sh.

Offline (start) the sidecar advances the requested number of ticks. Online
(start_online) it plays a live match: it follows the wall clock from
GAME_BEGIN, exchanges the match's UDP traffic itself and returns the
reliable TCP copies the client must send ("tcp_out").
"""

from __future__ import annotations

import json
import pathlib
import subprocess
from typing import Any

DEFAULT_PATH = pathlib.Path(__file__).resolve().parents[2] / "generated" / "bin" / "qqt-battle-sidecar"
TICK_MS = 20

# battleengine.Direction (types.go): 0 none, 1 up, 2 right, 3 down, 4 left.
MOVE_NONE, MOVE_UP, MOVE_RIGHT, MOVE_DOWN, MOVE_LEFT = range(5)

# battleengine.EventKind (types.go).
EVENT_BOMB_PLACED = 1
EVENT_BOMB_EXPLODED = 3
EVENT_CELL_DESTROYED = 5
EVENT_ACTOR_TRAPPED = 6
EVENT_ACTOR_RESCUED = 7
EVENT_ACTOR_ELIMINATED = 8
EVENT_MATCH_ENDED = 9
EVENT_PICKUP_COLLECTED = 11
EVENT_MAP_ELEMENT_MOVED = 20
EVENT_ACTOR_RESPAWNED = 32

# battleengine.ActorState.
ACTOR_ACTIVE, ACTOR_TRAPPED, ACTOR_ELIMINATED = range(3)


class SidecarError(RuntimeError):
    pass


class Sidecar:
    def __init__(self, path: pathlib.Path = DEFAULT_PATH):
        if not path.is_file():
            raise SidecarError(f"{path} is missing; build it with client/native/tools/build-sidecar.sh")
        self.process = subprocess.Popen([str(path)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1)

    def _call(self, command: dict[str, Any]) -> dict[str, Any]:
        assert self.process.stdin is not None and self.process.stdout is not None
        try:
            self.process.stdin.write(json.dumps(command, ensure_ascii=False) + "\n")
            self.process.stdin.flush()
        except BrokenPipeError as error:
            raise SidecarError("battle sidecar exited") from error
        line = self.process.stdout.readline()
        if not line:
            raise SidecarError("battle sidecar exited")
        reply = json.loads(line)
        if "error" in reply:
            raise SidecarError(reply["error"])
        return reply

    def start(self, client_root: pathlib.Path, map_id: int, players: list[dict[str, Any]], spawn_seed: int, item_seed: int,
              trap_ms: int = 6000, free_rule: bool = False) -> dict[str, Any]:
        return self._call({
            "cmd": "init", "client_root": str(client_root), "map_id": map_id, "spawn_seed": spawn_seed, "item_seed": item_seed,
            "tick_ms": TICK_MS, "trap_ms": trap_ms, "free_rule": free_rule, "players": players,
        })

    def start_online(self, client_root: pathlib.Path, begin: bytes, local_id: int, local_uin: int, server_host: str,
                     server_udp_port: int, uins: dict[int, int], free_rule: bool, autoplay: bool = False) -> dict[str, Any]:
        """begin is the GameBegin (0x0089) payload; the reply's "begin" holds
        the map, arbitrator and roster."""
        return self._call({"cmd": "init", "client_root": str(client_root), "online": {
            "begin": begin.hex(), "local_id": local_id, "local_uin": local_uin, "server_host": server_host,
            "server_udp_port": server_udp_port, "uins": {str(key): value for key, value in uins.items()}, "free_rule": free_rule,
            "autoplay": autoplay,
        }})

    def step(self, actions: list[dict[str, Any]], ticks: int = 1, tcp: list[tuple[int, bytes]] | None = None) -> dict[str, Any]:
        command: dict[str, Any] = {"cmd": "step", "ticks": ticks, "actions": actions}
        if tcp:
            command["tcp"] = [{"cmd": cmd, "payload": payload.hex()} for cmd, payload in tcp]
        return self._call(command)

    def close(self) -> None:
        if self.process.poll() is None:
            if self.process.stdin is not None:
                self.process.stdin.close()
            try:
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                self.process.kill()
