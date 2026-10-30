"""Offline practice battles: the local player against wandering opponents.

No server is involved; the sidecar decides everything. After a match ends,
Enter starts another one on the same map and Esc (or the leave button)
quits.
"""

from __future__ import annotations

import random
from typing import Any

import pygame

from ..avatar import AvatarCache
from ..render import Renderer
from ..resources import Resources
from ..runtime import UIRuntime
from .scene import BattleScene
from .sidecar import Sidecar

LOCAL_ID = 1


def practice_players(role: int, opponents: int) -> list[dict[str, Any]]:
    """Local player 1 on team 1 against wandering opponents, one team each."""
    players = [{"id": LOCAL_ID, "role": role, "team": 1, "name": "我"}]
    for index in range(opponents):
        players.append({"id": index + 2, "role": (role + index) % 11 + 1, "team": index + 2, "ai": True, "name": f"电脑{index + 1}"})
    return players


class PracticeSession:
    def __init__(self, runtime: UIRuntime, renderer: Renderer, resources: Resources, map_id: int, role: int, opponents: int):
        self.runtime, self.renderer, self.resources = runtime, renderer, resources
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
        self.scene.attach(self.runtime, self.map_id, self.renderer.font, self.renderer.text_line)
        self.renderer.underlay, self.renderer.overlay = self.scene.draw, self.scene.draw_overlay

    def handle(self, event: pygame.event.Event) -> None:
        if self.scene is None:
            return
        if event.type == pygame.KEYDOWN and self.scene.outcome["ended"]:
            if event.key in (pygame.K_RETURN, pygame.K_KP_ENTER):
                self.start()
                return
            if event.key == pygame.K_ESCAPE:
                self.leave()
                return
        self.scene.handle(event)

    def tick(self, elapsed_ms: float) -> None:
        if self.scene is not None:
            self.scene.tick(elapsed_ms)

    def leave(self) -> None:
        self.runtime.quit_requested = True

    def close(self) -> None:
        if self.scene is not None:
            self.scene.close()
            self.scene = None
