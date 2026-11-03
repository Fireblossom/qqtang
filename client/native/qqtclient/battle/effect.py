"""The `effect/*.eff` player (README "特效文件"; Client.exe 0x0057D840 loads,
0x0057C9C0 reads a track, 0x0057CD10 picks its frame, 0x00582D20 plays).

A file is a Python literal (version, [track, ...], [play mode, ?]). Play
mode 1 loops, 2 holds the last moment, anything else stops drawing after
the effect's length, the latest keyframe of any track. A track is
((image ID, (image name, reversed, ms per frame, ?), (blend,), (?,),
layer, (x, y)), [keyframe, ...]) and a keyframe [time, ((x, y, z),
(scale x, scale y), rotation, (r, g, b), alpha)]; values between
keyframes are linear. A non-zero image ID names object/magic/magic%04d.img,
ID 0 takes the caller's image. Blend 1 adds colour * alpha to the screen
(GFX.dll flag 0x10); other values are drawn as ordinary alpha here.

Positions are relative to the effect point the caller gives; the frame's
own anchor offset is scaled with the image. Rotation (degrees) turns the
image about that anchor.
"""

from __future__ import annotations

import ast
import dataclasses
import math
from typing import Callable

import pygame

from ..script import log

Frames = list[tuple[pygame.Surface | None, int, int]]


@dataclasses.dataclass
class Key:
    time: int
    x: float
    y: float
    scale_x: float
    scale_y: float
    rotation: float
    color: tuple[float, float, float]
    alpha: float


@dataclasses.dataclass
class Track:
    image_id: int
    image: str
    reverse: bool
    frame_ms: int
    blend: int
    layer: int
    x: int
    y: int
    keys: list[Key]


@dataclasses.dataclass
class Effect:
    tracks: list[Track]
    mode: int
    length: int


def parse_effect(text: str) -> Effect:
    _version, raw_tracks, tail = ast.literal_eval(text)
    tracks = []
    for header, raw_keys in raw_tracks:
        image_id, (image, reverse, frame_ms, _), (blend,), _unknown, layer, (x, y) = header
        keys = [Key(int(t), float(p[0]), float(p[1]), float(s[0]), float(s[1]), float(r), tuple(map(float, c)), float(a))
                for t, (p, s, r, c, a) in raw_keys]
        tracks.append(Track(int(image_id), str(image), bool(reverse), max(1, int(frame_ms)), int(blend), int(layer), int(x), int(y), keys))
    length = max((track.keys[-1].time for track in tracks if track.keys), default=0)
    return Effect(tracks, int(tail[0]) if tail else 0, length)


def _lerp(a: Key, b: Key, t: int) -> Key:
    if b.time <= a.time:
        return a
    f = (t - a.time) / (b.time - a.time)
    mix = lambda u, v: u + (v - u) * f  # noqa: E731
    return Key(t, mix(a.x, b.x), mix(a.y, b.y), mix(a.scale_x, b.scale_x), mix(a.scale_y, b.scale_y), mix(a.rotation, b.rotation),
               tuple(mix(u, v) for u, v in zip(a.color, b.color)), mix(a.alpha, b.alpha))


def _key_at(track: Track, t: int) -> Key | None:
    keys = track.keys
    if not keys or t < keys[0].time or t > keys[-1].time:
        return None
    for a, b in zip(keys, keys[1:]):
        if a.time <= t <= b.time:
            return _lerp(a, b, t)
    return keys[-1]


class EffectPlayer:
    """Loads .eff files on first use and draws them."""

    def __init__(self, read: Callable[[str], bytes | None], frames: Callable[[str], Frames | None]):
        self.read, self.frames = read, frames
        self.effects: dict[str, Effect | None] = {}
        self.cache: dict[tuple, tuple[pygame.Surface, int, int]] = {}

    def load(self, name: str) -> Effect | None:
        if name not in self.effects:
            data = self.read(name if "/" in name else "effect/" + name)
            effect = None
            if data is not None:
                try:
                    effect = parse_effect(data.decode("gbk", "replace"))
                except (ValueError, SyntaxError, TypeError) as error:
                    log(f"[effect] {name}: {error}")
            self.effects[name] = effect
        return self.effects[name]

    def length(self, name: str) -> int:
        effect = self.load(name)
        return effect.length if effect else 0

    def draw(self, surface: pygame.Surface, name: str, elapsed: int, x: int, y: int, image: str = "",
             *, foreground: bool | None = None) -> bool:
        """Draws the effect `elapsed` ms after it started at effect point
        (x, y); False once a non-looping effect is over."""
        effect = self.load(name)
        if effect is None or elapsed < 0:
            return False
        t = elapsed
        if effect.length > 0 and t > effect.length:
            if effect.mode == 1:
                t %= effect.length
            elif effect.mode == 2:
                t = effect.length
            else:
                return False
        for track in sorted(effect.tracks, key=lambda track: track.layer):
            # Avatar parts facing down draw positive layers before the part,
            # zero/negative layers after it (0x00583B20 / 0x00583BC0).
            if foreground is not None and (track.layer <= 0) != foreground:
                continue
            key = _key_at(track, t)
            if key is None or key.alpha <= 0:
                continue
            path = f"object/magic/magic{track.image_id:04d}.img" if track.image_id else image
            frames = self.frames(path) if path else None
            if not frames:
                continue
            index = (t - track.keys[0].time) % (len(frames) * track.frame_ms) // track.frame_ms
            if track.reverse:
                index = len(frames) - 1 - index
            drawn = self._transform(path, frames, index, key, track.blend == 1)
            if drawn is None:
                continue
            picture, ox, oy = drawn
            at = (round(x + track.x + key.x + ox), round(y + track.y + key.y + oy))
            if track.blend == 1:
                surface.blit(picture, at, special_flags=pygame.BLEND_RGB_ADD)
            else:
                surface.blit(picture, at)
        return True

    def _transform(self, path: str, frames: Frames, index: int, key: Key, additive: bool) -> tuple[pygame.Surface, int, int] | None:
        sx, sy, rotation = round(key.scale_x, 2), round(key.scale_y, 2), round(key.rotation) % 360
        color = tuple(round(c) for c in key.color)
        alpha = round(min(key.alpha, 1.0) * 32) / 32
        cache_key = (path, index, sx, sy, rotation, color, alpha, additive)
        if cache_key in self.cache:
            return self.cache[cache_key]
        image, ox, oy = frames[index]
        if image is None or sx <= 0 or sy <= 0:
            return None
        if pygame.display.get_surface() is not None:
            picture = image.convert_alpha()
        else:  # headless: keep a per-pixel alpha copy
            picture = pygame.Surface(image.get_size(), pygame.SRCALPHA)
            picture.blit(image, (0, 0))
        if (sx, sy) != (1, 1):
            picture = pygame.transform.smoothscale(picture, (max(1, round(picture.get_width() * sx)), max(1, round(picture.get_height() * sy))))
        ax, ay = -ox * sx, -oy * sy  # the anchor inside the scaled picture
        if rotation:
            w, h = picture.get_size()
            picture = pygame.transform.rotate(picture, -rotation)
            # Move the anchor with the image: rotate its offset from the centre.
            cx, cy = ax - w / 2, ay - h / 2
            rad = math.radians(rotation)
            rx, ry = cx * math.cos(rad) - cy * math.sin(rad), cx * math.sin(rad) + cy * math.cos(rad)
            ax, ay = rx + picture.get_width() / 2, ry + picture.get_height() / 2
        if color != (255, 255, 255) or alpha < 1:
            picture = picture.copy()
            picture.fill((*color, round(alpha * 255)), special_flags=pygame.BLEND_RGBA_MULT)
        if additive:
            picture = picture.premul_alpha()
        drawn = (picture, round(-ax), round(-ay))
        if len(self.cache) > 4096:
            self.cache.clear()
        self.cache[cache_key] = drawn
        return drawn
