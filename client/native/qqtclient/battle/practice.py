"""Offline practice battles: the local player against wandering opponents.

No server is involved; the sidecar decides everything. From the command
line, Enter starts another match on the same map after one ends and Esc (or
the leave button) quits. Started from the lobby (free practice), the session
reports back through on_leave instead: after the results, or when the player
leaves.
"""

from __future__ import annotations

import random
from typing import Any, Callable

import pygame

from ..avatar import AvatarCache
from ..keylayout import BattleKeys
from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from .scene import BattleScene
from .sidecar import Sidecar

LOCAL_ID = 1
RESULT_SHOWN_MS = 12_000  # native result controller 00612C86, as online
SKIP_KEYS = (pygame.K_RETURN, pygame.K_KP_ENTER, pygame.K_ESCAPE)


def practice_players(role: int, opponents: int) -> list[dict[str, Any]]:
    """Local player 1 on team 1 against wandering opponents, one team each."""
    players = [{"id": LOCAL_ID, "role": role, "team": 1, "name": "我"}]
    for index in range(opponents):
        players.append({"id": index + 2, "role": (role + index) % 11 + 1, "team": index + 2, "ai": True, "name": f"电脑{index + 1}"})
    return players


class PracticeSession:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources, map_id: int, role: int, opponents: int,
                 on_leave: Callable[[bool], None] | None = None):
        """on_leave(ended) is called once the session is over: ended is
        whether the match finished rather than being left."""
        self.runtime, self.renderer, self.resources = runtime, renderer, resources
        self.on_leave = on_leave
        self.finished = False
        self.ended_ms: float | None = None
        self.map_id, self.players = map_id, practice_players(role, opponents)
        self.avatars = AvatarCache(resources)
        self.scene: BattleScene | None = None
        runtime.namespace.update({
            "LeaveGame": self.leave,
            # Explore-mode panels; empty lists make uiGame hide them.
            "GetPVEBossInfo": lambda: [],
            "GetPVEPlayerInfo": lambda: [],
        })

    def start(self) -> None:
        self.close()
        if self.runtime.current_screen != "game":
            self.runtime.goto_screen("game")
        seed = random.getrandbits(31)
        sidecar = Sidecar()
        state = sidecar.start(self.resources.root, self.map_id, self.players, seed, seed ^ 0x5A5A)
        self.scene = BattleScene(self.resources, self.renderer.sprites, self.avatars, sidecar, self.map_id, self.players,
                                 local_id=LOCAL_ID, state=state)
        # 0x00458CC0 loads the layout of getMyUin(); offline there may be none.
        self.scene.keys = BattleKeys.for_uin(self.runtime.namespace.get("uin") or None)
        self.scene.attach(self.runtime, self.map_id, self.renderer.font, self.renderer.text_line, self.renderer.draw_widget)
        self.renderer.underlay, self.renderer.overlay = self.scene.draw, self.scene.draw_overlay

    def handle(self, event: pygame.event.Event) -> None:
        if self.scene is None:
            return
        if self.on_leave is not None and self.ended_ms is not None:
            if event.type == pygame.KEYDOWN and event.key in SKIP_KEYS or event.type == pygame.MOUSEBUTTONDOWN:
                self._finish(True)
                return
        elif event.type == pygame.KEYDOWN and self.scene.outcome["ended"]:
            if event.key in (pygame.K_RETURN, pygame.K_KP_ENTER):
                self.start()
                return
            if event.key == pygame.K_ESCAPE:
                self.leave()
                return
        self.scene.handle(event)

    def tick(self, elapsed_ms: float) -> None:
        if self.scene is None:
            return
        self.scene.tick(elapsed_ms)
        if self.on_leave is not None and self.scene.outcome["ended"]:
            self.ended_ms = (self.ended_ms or 0.0) + elapsed_ms
            if self.ended_ms >= RESULT_SHOWN_MS:
                self._finish(True)

    def leave(self) -> None:
        if self.on_leave is not None:
            self._finish(False)
        else:
            self.runtime.quit_requested = True

    def _finish(self, ended: bool) -> None:
        if self.finished:
            return
        self.finished = True
        self.close()
        self.renderer.underlay = self.renderer.overlay = None
        assert self.on_leave is not None
        self.on_leave(ended)

    def close(self) -> None:
        if self.scene is not None:
            self.scene.close()
            self.scene = None
