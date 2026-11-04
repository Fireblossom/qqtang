"""Stateful gameUI background frames (1002A16B / 100297F3 / 10013238)."""

from dataclasses import dataclass

from .ui_alpha import float32


@dataclass
class ImageAnimation:
    frame: int = 0
    state: int = 2
    row: int = 0
    frame_age: float = 0.0
    loop_age: float = 0.0
    hidden: bool = False

    def advance(self, seconds: float, state: int, first: int, last: int,
                speed: float, loop_speed: float) -> None:
        if self.state == state:
            seconds = float32(seconds)
            self.frame_age = float32(seconds + float32(self.frame_age))
            self.loop_age = float32(seconds + float32(self.loop_age))
            if self.frame_age >= float32(speed):
                self.frame += 1
                self.frame_age = 0.0
                if self.frame == first + 1:
                    if self.loop_age < loop_speed:
                        self.frame = first
                    else:
                        self.loop_age = 0.0
        else:
            self.state = state
            self.frame = first
            self.loop_age = loop_speed
        if self.frame < first or self.frame > last:
            self.frame = first

    def draw_frame(self, count: int, first: int, image_flag: int) -> int | None:
        if self.hidden or self.frame < 0 or count == 0:
            return None
        frame = min(self.frame, count - 1)
        if self.frame >= count:
            if image_flag & 8 and self.frame > 0:
                self.hidden = True
            self.frame = first
        return frame
