"""Standalone forged part/bubble, after Client.exe 0x0047C600.

Parts face down at point-(50,84), 200 ms/frame. Bubbles use point-(20,50)
and the image controller's default 300 ms/frame. Effects stay at the point.
"""

from __future__ import annotations

import pygame

from .battle.effect import EffectPlayer


class ForgePreview:
    def __init__(self, resources, sprites):
        self.sprites = sprites
        self.effects = EffectPlayer(resources.read, sprites.get)
        self.item = 0
        self.kind = ""
        self.sub_id = 0
        self.effect = ""
        self.position = (100, 394)
        self.started = 0
        self.signature = None

    def wear(self, item: int, descriptor, effect: str, now: int):
        signature = (item, effect)
        if signature == self.signature:
            return
        self.signature = signature
        self.item = item
        self.kind, self.sub_id = descriptor[:2] if descriptor else ("", 0)
        self.effect = effect
        self.started = now

    def draw(self, surface: pygame.Surface, now: int):
        if not self.item or not self.kind:
            return
        path = f"object/{self.kind}/{self.kind}{self.sub_id}_stand.img"
        frames = self.sprites.get(path)
        if not frames:
            return
        elapsed = max(0, now - self.started)
        x, y = self.position
        bubble = self.kind == "bomb"
        if self.effect and not bubble:
            self.effects.draw(surface, self.effect, elapsed, x, y, foreground=False)
        directions = self.sprites.directions.get(path, 1)
        per_direction = len(frames) // directions
        frame = min(3, directions - 1) * per_direction + elapsed // (300 if bubble else 200) % per_direction
        image, dx, dy = frames[frame]
        if image is not None:
            surface.blit(image, (x - (20 if bubble else 50) + dx, y - (50 if bubble else 84) + dy))
        if self.effect:
            self.effects.draw(surface, self.effect, elapsed, x, y, foreground=None if bubble else True)
