"""Offline matches against AI seats, on any map and rule.

The sidecar's offline mode (cmd/qqt-battle-sidecar/offline.go) plays the
whole match: every seat is a client of its own, the local player's is the
arbitrator, the AI seats are played by the sidecar's bot (or the rule-1
model), and a local referee keeps the server's ledger and produces the
GAME_OVER. This class is the client side: it drives the scene with the
sidecar's clock (as practice mode, so pauses do not matter), shows the
GAME_OVER on the original result screen as online, follows adventure stage
changes and then goes back (on_leave), or quits when started from the
command line.

offline_players() builds the seats from a team layout such as "1,2" (one
player per team) or "1,1,2,2" (two teams of two), the local player first.
"""

from __future__ import annotations

import random
from typing import Any, Callable

import pygame

from ..avatar import AvatarCache
from ..keylayout import BattleKeys
from ..net import match
from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from ..script import call_script, log
from .online import MatchMember, result_rows
from .scene import BattleScene
from .sidecar import Sidecar, SidecarError

LOCAL_ID = 1
RESULT_SHOWN_MS = 12_000  # native result controller 00612C86, as online
SKIP_KEYS = (pygame.K_RETURN, pygame.K_KP_ENTER, pygame.K_ESCAPE)
ROLES = 8  # AIs pick among the eight ordinary roles


def team_layout(teammates: int, opponents: int, teams: str | None = None) -> list[int]:
    """Team of every seat, the local player first. teams ("1,2,2") wins over
    the counts; otherwise the local player and its teammates are team 1 and
    the opponents team 2."""
    if teams:
        layout = [int(team) for team in teams.replace(" ", "").split(",") if team]
        if not layout or any(not 1 <= team <= 8 for team in layout) or len(layout) > 8:
            raise ValueError(f"bad team layout {teams!r}: 1..8 seats with teams 1..8")
        return layout
    layout = [1] * (1 + max(0, teammates)) + [2] * max(1, opponents)
    if len(layout) > 8:
        raise ValueError("at most 8 players")
    return layout


def offline_players(role: int, layout: list[int], names: dict[int, str] | None = None,
                    ext_point: int = 0, policy: str = "") -> list[dict[str, Any]]:
    """Seats for Sidecar.start_offline: the local player (ID 1) and AIs."""
    players = []
    for index, team in enumerate(layout):
        player_id = index + 1
        local = index == 0
        entry: dict[str, Any] = {"id": player_id, "role": role if local else (role + index) % ROLES + 1, "team": team,
                                 "ai": not local, "name": (names or {}).get(player_id) or ("我" if local else f"电脑{index}"),
                                 "seat": index}
        if ext_point:
            entry["ext_point"] = ext_point
        if policy and not local:
            entry["policy"] = policy
        players.append(entry)
    return players


class OfflineBattle:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources, map_id: int,
                 players: list[dict[str, Any]], local_id: int = LOCAL_ID, on_leave: Callable[[bool], None] | None = None,
                 boss: bool = False, policy: str = "", free_rule: bool = False, game_type: int = 0,
                 wear: dict[int, tuple[tuple[str, int], ...]] | None = None, uin: int | None = None, autoplay: bool = False):
        """on_leave(ended) is called once it is over: ended is whether the
        match finished rather than being left. Without it the program quits
        (Enter starts another match instead). autoplay lets the sidecar's AI
        play the local seat too (tests and screenshots)."""
        self.runtime, self.renderer, self.resources = runtime, renderer, resources
        self.map_id, self.players, self.local_id = map_id, players, local_id
        self.on_leave, self.boss, self.policy, self.free_rule = on_leave, boss, policy, free_rule
        self.game_type, self.wear, self.uin, self.autoplay = game_type, wear or {}, uin, autoplay
        self.avatars = AvatarCache(resources)
        self.scene: BattleScene | None = None
        self.sidecar: Sidecar | None = None
        self.finished = False
        self.over: match.GameOver | None = None
        self.shown_ms = 0.0
        self.stage = 0
        self.members = {player["id"]: MatchMember(0, player.get("name", ""), player.get("points", 0), seat=player.get("seat", -1))
                        for player in players}
        runtime.namespace.update({
            "LeaveGame": self.leave,
            "GetPVEBossInfo": lambda: [],
            "GetPVEPlayerInfo": lambda: [],
        })

    def start(self) -> None:
        self.close()
        self.finished, self.over, self.shown_ms, self.stage = False, None, 0.0, 0
        seed = random.getrandbits(31) or 1
        self.sidecar = Sidecar()
        try:
            seats = [dict(player, ai=True) for player in self.players] if self.autoplay else self.players
            state = self.sidecar.start_offline(self.resources.root, self.map_id, seats, 0 if self.autoplay else self.local_id, seed,
                                               (seed ^ 0x5A5A) or 1, self.free_rule, self.boss, self.policy)
            ai = (state.get("offline") or {}).get("ai") or {}
            log(f"[offline] map {self.map_id} ({state['begin']['rule']}): " + ", ".join(
                f"{player['id']}:{player.get('name')} team {player['team']}{' ' + ai.get(str(player['id']), '') if player.get('ai') else ''}"
                for player in self.players))
            self._show(state)
        except Exception:
            # Whatever failed, the sidecar process must not outlive the match.
            self.close()
            self.renderer.underlay = self.renderer.overlay = None
            raise

    def _show(self, state: dict[str, Any]) -> None:
        """Build the scene for a (new) GAME_BEGIN."""
        begin = state["begin"]
        names = {player["id"]: player for player in self.players}
        players = [dict(player, name=names.get(player["id"], {}).get("name") or f"电脑{player['id']}",
                        seat=names.get(player["id"], {}).get("seat", index), points=0)
                   for index, player in enumerate(begin["players"])]
        if self.runtime.current_screen != "game":
            self.runtime.goto_screen("game")
        assert self.sidecar is not None
        self.scene = BattleScene(self.resources, self.renderer.sprites, self.avatars, self.sidecar, begin["map_id"], players,
                                 local_id=self.local_id, state=state, realtime=True, wear=self.wear, game_type=self.game_type)
        self.scene.keys = BattleKeys.for_uin(self.uin or self.runtime.namespace.get("uin") or None)
        self.scene.attach(self.runtime, begin["map_id"], self.renderer.font, self.renderer.text_line, self.renderer.draw_widget)
        self.renderer.underlay, self.renderer.overlay = self.scene.draw, self.scene.draw_overlay
        for name in ("showRecordRecorder", "showRecordPlayer"):  # 0x005A3C7F: no recording offline
            if callable(function := self.runtime.namespace.get(name)):
                call_script(function, 0)

    def handle(self, event: pygame.event.Event) -> None:
        if self.scene is None or self.finished:
            return
        if self.over is not None:
            if event.type == pygame.KEYDOWN and event.key in SKIP_KEYS or event.type == pygame.MOUSEBUTTONDOWN:
                if self.on_leave is None and event.type == pygame.KEYDOWN and event.key in (pygame.K_RETURN, pygame.K_KP_ENTER):
                    self.start()
                    return
                self._finish(True)
            return
        self.scene.handle(event)

    def tick(self, elapsed_ms: float) -> None:
        if self.scene is None or self.finished:
            return
        try:
            self.scene.tick(elapsed_ms)
        except SidecarError as error:
            log(f"[offline] sidecar failed: {error}")
            self._finish(False)
            return
        offline = self.scene.state.get("offline") or {}
        if offline.get("stage", 0) != self.stage and self.scene.state.get("begin"):
            # Rule 12: the survivors went through the door; a new stage.
            self.stage = offline["stage"]
            log(f"[offline] adventure stage {self.stage}: map {self.scene.state['begin']['map_id']}")
            self._show(self.scene.state)
            return
        if self.over is None and offline.get("game_over"):
            self._show_result(bytes.fromhex(offline["game_over"]))
        if self.over is not None:
            self.shown_ms += elapsed_ms
            if self.shown_ms >= RESULT_SHOWN_MS and self.on_leave is not None:
                self._finish(True)

    def _show_result(self, body: bytes) -> None:
        assert self.scene is not None
        self.over = match.parse_game_over(body)
        mine = next((result for result in self.over.results if result.player_id == self.local_id), None)
        log("[offline] game over: " + ", ".join(f"{result.player_id}={result.result}" for result in self.over.results))
        self.scene.result = mine.result if mine is not None else match.RESULT_LOSS
        self.scene.over = True
        self.scene.show_results(result_rows(self.over, self.members, self.scene.map.rule))

    def leave(self) -> None:
        """The leave button."""
        if self.on_leave is not None:
            self._finish(False)
        else:
            self.runtime.quit_requested = True

    def _finish(self, ended: bool) -> None:
        if self.finished:
            return
        self.close()
        self.finished = True
        self.renderer.underlay = self.renderer.overlay = None
        if self.on_leave is not None:
            self.on_leave(ended)
        else:
            self.runtime.quit_requested = True

    def close(self) -> None:
        if self.scene is not None:
            self.scene.close()
            self.scene = None
        elif self.sidecar is not None:
            self.sidecar.close()
        self.sidecar = None
