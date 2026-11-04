"""The default CImageData clock (00487360 / 0042A680)."""

import dataclasses

NATIVE_FRAME_MS = 300


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
