"""Character avatars: object/player/player<role>.ini parts stacked into one sprite.

Each part is object/<part>/<part><id>_<action>.img on a shared 100x100 canvas
with one frame group per direction (0 right, 1 up, 2 left, 3 down). Parts
that change with the team colour also ship a grey <..>_m.img mask; the plain
image is the red team's colouring.
"""

from __future__ import annotations

import configparser

import numpy as np
import pygame

from . import dimg
from .resources import Resources

# Back to front. npack (the held item) is drawn first so the body covers it.
REMOVED_PART = -1  # a wear value that drops the role's own part
PART_ORDER = ("npack", "foot", "leg", "body", "cloth", "head", "face", "hair")
# Worn shop parts that a role's ini does not list, back to front among the
# parts above. Their exact native order is not traced.
EXTRA_PARTS_BEFORE = {"bhadorn": "npack", "fpack": "hair", "cladorn": "head", "eye": "hair", "mouth": "hair",
                      "ear": "hair", "mask": "hair"}
EXTRA_PARTS_AFTER = ("cap", "thadorn", "fhadorn")
# Trapped (trigger) and dying poses are one full-body image named after the
# standing cloth, e.g. cloth10101_trigger.img (23 frames) for role 1. The
# cloth IDs in the ini's [trigger]/[die] sections have no image files.
FULL_BODY_ACTIONS = ("trigger", "die", "win", "lose")
CANVAS = 100
DIRECTION_DOWN = 3
RED_TEAM = 0


def load_team_colors(resources: Resources) -> list[tuple[float, float, float]]:
    """colorlist.dat [Player0]: hue (degrees), saturation and brightness factors per team."""
    data = resources.read("colorlist.dat")
    parser = configparser.ConfigParser()
    parser.read_string(data.decode("gbk", "replace") if data else "")
    if not parser.has_section("Player0"):
        return []
    colors = []
    for _, value in parser.items("Player0"):
        hue, saturation, brightness = (float(part) for part in value.split(","))
        colors.append((hue, saturation, brightness))
    return colors


def tint(mask: np.ndarray, color: tuple[float, float, float]) -> np.ndarray:
    """Colour a grey mask, keeping each pixel's HLS lightness.

    Fitted against the red team's plain images (lightness matches within 1%);
    a zero saturation factor (grey team) scales lightness by the third factor.
    """
    hue, saturation, brightness = color
    rgb = mask[..., :3].astype(np.float32) / 255
    lightness = (rgb.max(-1) + rgb.min(-1)) / 2
    if saturation <= 0:
        lightness = np.clip(lightness * brightness, 0, 1)
    sat = min(1.0, saturation)
    # colorsys.hls_to_rgb, vectorised.
    q = np.where(lightness < 0.5, lightness * (1 + sat), lightness + sat - lightness * sat)
    p = 2 * lightness - q
    channels = []
    for offset in (1 / 3, 0, -1 / 3):
        t = (hue / 360 + offset) % 1
        channels.append(np.select(
            [t < 1 / 6, t < 1 / 2, t < 2 / 3],
            [p + (q - p) * 6 * t, q, p + (q - p) * (2 / 3 - t) * 6],
            p,
        ))
    out = mask.copy()
    out[..., :3] = (np.stack(channels, -1) * 255).round().astype(np.uint8)
    return out


class AvatarCache:
    def __init__(self, resources: Resources):
        self.resources = resources
        self.colors = load_team_colors(resources)
        self.cache: dict[tuple, pygame.Surface | None] = {}
        self.sprites: dict[str, dimg.Sprite | None] = {}

    def _sprite(self, path: str) -> dimg.Sprite | None:
        if path not in self.sprites:
            data = self.resources.read(path)
            try:
                self.sprites[path] = dimg.parse(data) if data else None
            except dimg.DIMGError:
                self.sprites[path] = None
        return self.sprites[path]

    def parts(self, role_id: int, action: str) -> dict[str, int]:
        data = self.resources.read(f"object/player/player{role_id}.ini")
        parser = configparser.ConfigParser()
        parser.read_string(data.decode("gbk", "replace") if data else "")
        stand = dict(parser.items("stand")) if parser.has_section("stand") else {}
        if action in ("win", "lose"):
            # 00425D60/00425F10: action INI values enable a part; its image
            # uses the STAND part number with the requested action suffix.
            enabled = dict(parser.items(action)) if parser.has_section(action) else {}
            return {name: int(stand[name]) for name, value in enabled.items()
                    if value.strip().isdigit() and int(value) > 0
                    and stand.get(name, "").strip().isdigit()}
        cloth = stand.get("cloth", "").strip()
        if action in FULL_BODY_ACTIONS and cloth.isdigit() and self._sprite(f"object/cloth/cloth{cloth}_{action}.img"):
            return {"cloth": int(cloth)}
        parts = dict(stand)
        if action != "stand" and parser.has_section(action):
            parts.update(parser.items(action))
        return {name: int(value) for name, value in parts.items() if value.strip().isdigit()}

    def get(self, role_id: int, team: int, action: str = "stand", direction: int = DIRECTION_DOWN,
            frame: int = 0, wear: tuple[tuple[str, int], ...] = (),
            part_frames: tuple[tuple[str, int], ...] = ()) -> pygame.Surface | None:
        """One frame of an action (walk has 6 per direction; stand has 1).
        wear replaces parts with worn items, e.g. (("cap", 101),)."""
        key = (role_id, team, action, direction, frame, wear, part_frames)
        if key not in self.cache:
            self.cache[key] = self._compose(role_id, team, action, direction, frame, wear, part_frames)
        return self.cache[key]

    def part_frame_counts(self, role_id: int, action: str) -> dict[str, int]:
        counts = {}
        for name, number in self.parts(role_id, action).items():
            sprite = self._sprite(f"object/{name}/{name}{number}_{action}.img")
            counts[name] = len(sprite.frames) // sprite.directions if sprite else 0
        return counts

    @staticmethod
    def part_order(parts: dict[str, int]) -> list[str]:
        order = list(PART_ORDER)
        for name, before in EXTRA_PARTS_BEFORE.items():
            if name in parts:
                order.insert(order.index(before), name)
        order.extend(name for name in EXTRA_PARTS_AFTER if name in parts)
        return order

    def _compose(self, role_id: int, team: int, action: str, direction: int, frame_index: int,
                 wear: tuple[tuple[str, int], ...] = (),
                 part_frames: tuple[tuple[str, int], ...] = ()) -> pygame.Surface | None:
        parts = self.parts(role_id, action)
        full_body = action in FULL_BODY_ACTIONS and set(parts) == {"cloth"}
        if not full_body:
            for name, part in wear:
                if part > 0:
                    parts[name] = part
                elif part == REMOVED_PART:
                    parts.pop(name, None)
        if not parts:
            return None
        canvas = np.zeros((CANVAS, CANVAS, 4), np.float32)
        color = self.colors[team] if team != RED_TEAM and 0 <= team < len(self.colors) else None
        frames_by_part = dict(part_frames)
        for name in self.part_order(parts):
            if name not in parts:
                continue
            sprite, masked = None, False
            # Some action sections name parts that only have stand images.
            for suffix in ((action,) if action in ("win", "lose") else dict.fromkeys((action, "stand"))):
                base = f"object/{name}/{name}{parts[name]}_{suffix}"
                if color is not None:
                    sprite, masked = self._sprite(base + "_m.img"), True
                if sprite is None:
                    sprite, masked = self._sprite(base + ".img"), False
                if sprite is not None:
                    break
            if sprite is None:
                continue
            per_direction = len(sprite.frames) // sprite.directions
            index = min(direction, sprite.directions - 1) * per_direction + frames_by_part.get(name, frame_index) % per_direction
            frame = sprite.frames[index]
            if frame.pixels is None:
                continue
            pixels = tint(frame.pixels, color) if masked else frame.pixels
            # Some frames (e.g. the dying splash) reach past the canvas.
            pixels = pixels[max(0, -frame.y):CANVAS - frame.y, max(0, -frame.x):CANVAS - frame.x]
            height, width = pixels.shape[:2]
            if height <= 0 or width <= 0:
                continue
            alpha = pixels[..., 3:4].astype(np.float32) / 255
            top, left = max(0, frame.y), max(0, frame.x)
            region = canvas[top:top + height, left:left + width]
            region[..., :3] = pixels[..., :3] * alpha + region[..., :3] * (1 - alpha)
            region[..., 3:4] = alpha * 255 + region[..., 3:4] * (1 - alpha)
        image = canvas.round().astype(np.uint8)
        return pygame.image.frombuffer(image.tobytes(), (CANVAS, CANVAS), "RGBA").copy()
