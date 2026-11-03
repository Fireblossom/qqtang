"""Machine launch pictures: native 005EF91F / 006088EC / 006089C5."""

from __future__ import annotations

import dataclasses
import struct

NATIVE_FRAME_MS = 300  # CImageData constructor 00487360; 0042A680 consumes it

def f32(value: float) -> float:
    return struct.unpack("<f", struct.pack("<f", value))[0]


@dataclasses.dataclass
class Segment:
    start: tuple[int, int]  # native (x,y); the packed native points store y first
    end: tuple[int, int]
    x: float = 0.0
    y: float = 0.0
    active: bool = False
    animation_ms: int = 0

    def reset(self) -> None:
        self.x, self.y = map(float, self.start)
        self.active = True
        # 005EFB2D resets position/active, but not the picture's animation.

    def step(self, amount: int) -> None:
        if not self.active:
            return
        dx = self.end[0] - self.start[0]
        if dx == 0:
            sign = -1 if self.end[1] <= self.start[1] else 1
            self.y = f32(self.y + amount * sign)
        else:
            slope = f32((self.end[1] - self.start[1]) / dx)
            sign = -1 if self.end[0] <= self.x else 1
            self.x = f32(self.x + amount * sign)
            self.y = f32(self.y + amount * sign * slope)
        distance_squared = (self.x - self.end[0]) ** 2 + (self.y - self.end[1]) ** 2
        self.active = int(distance_squared) >= 4
        self.animation_ms += amount * 10


class MachineProjectile:
    def __init__(self, row: int, col: int, first: bool):
        x = (col - 1) * 40 + (80 if first else 30)
        y = (row - 2) * 40 + 20
        apex = (280, (row - 2) * 40 - 60)
        self.first = Segment((x, y), apex)
        self.second = Segment(apex, (280, 0))
        self.fired = 0

    def advance(self, clock: int, fired: int) -> None:
        if fired <= 0:
            return
        if self.fired != fired:
            self.fired = fired
            self.first.reset()
            self.second.reset()
        # 005EF7DE passes literal 1 and 2, not elapsed milliseconds. Preserve
        # one update per host frame, including same-frame segment handover.
        if clock - fired >= 700 and self.first.active:
            self.first.step(1)
        if not self.first.active and self.second.active:
            self.second.step(2)

    def picture(self, clock: int) -> tuple[int, int, int, int] | None:
        if self.first.active and clock - self.fired >= 700:
            number, segment = 165, self.first
        elif not self.first.active and self.second.active:
            number, segment = 166, self.second
        else:
            return None
        return number, segment.animation_ms // NATIVE_FRAME_MS, int(segment.x), int(segment.y)
