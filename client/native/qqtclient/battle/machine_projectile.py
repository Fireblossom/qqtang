"""Machine pictures and launch paths: native 005EF7DE / 005EF91F."""

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


@dataclasses.dataclass
class FrameLoop:
    frames: int
    frame: int = 0
    remainder: int = 0

    def advance(self, milliseconds: int) -> None:
        self.remainder += milliseconds
        if self.remainder >= NATIVE_FRAME_MS:
            self.frame += self.remainder // NATIVE_FRAME_MS
            self.remainder %= NATIVE_FRAME_MS
            if self.frame >= self.frames:
                # 0042A680 resets to zero, rather than preserving overshoot.
                self.frame = 0


class MachineGateAnimation(MachineProjectile):
    def __init__(self, row: int, col: int, first: bool):
        super().__init__(row, col, first)
        self.loops = {"idle": FrameLoop(8), "ready": FrameLoop(12), "fire": FrameLoop(24), "arrow": FrameLoop(2)}
        self.is_ready = False
        self.arrow_visible = False
        self.draw_clock = 0

    def advance(self, clock: int, fired: int, milliseconds: int = 0, ready: bool = False, local: bool = False) -> None:
        # The image updates use the preceding frame's ready/arrow/clock state.
        if self.arrow_visible:
            self.loops["arrow"].advance(milliseconds)
        if self.is_ready:
            self.loops["ready"].advance(milliseconds)
        elif not fired or ((self.draw_clock - fired) & 0xFFFFFFFF) >= 800:
            self.loops["idle"].advance(milliseconds)
        self.is_ready = ready
        if not ready:
            self.arrow_visible = False
        elif local:
            self.arrow_visible = True
        self.draw_clock = clock
        if ((clock - fired) & 0xFFFFFFFF) < 800:
            self.arrow_visible = False
            self.loops["fire"].advance(milliseconds * 3)
        second_time = self.second.animation_ms
        super().advance(clock, fired)
        if self.second.animation_ms != second_time:
            # 005EF912 writes 1 to the parent's cached drawing clock.
            self.draw_clock = 1

    def body_layers(self) -> list[tuple[str, int]]:
        layers = []
        if self.is_ready:
            layers.append(("ready", self.loops["ready"].frame))
        if not self.is_ready and (not self.fired or ((self.draw_clock - self.fired) & 0xFFFFFFFF) >= 800):
            layers.append(("idle", self.loops["idle"].frame))
        if self.draw_clock > 3000 and ((self.draw_clock - self.fired) & 0xFFFFFFFF) < 800:
            layers.append(("fire", self.loops["fire"].frame))
        return layers
