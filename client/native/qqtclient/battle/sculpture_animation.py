"""Sculpture picture lifecycle: 005F5EB8 / 005F61CA / 005F634C."""

from .animation import FrameLoop

STATUE_TRIGGER_MS = 2000


class StatueAnimation:
    def __init__(self):
        self.progress = 0
        self.stage: FrameLoop | None = None
        self.hero: FrameLoop | None = None
        self.trigger_left = 0

    def advance(self, progress: int, milliseconds: int, hero_frames: int = 1) -> None:
        if progress != self.progress:
            self.progress = progress
            # Deposit/removal destroys and reconstructs the stage and hero.
            self.stage = FrameLoop(3) if progress > 0 else None
            self.hero = FrameLoop(max(1, hero_frames)) if progress >= 4 else None
            if self.stage is not None:
                self.trigger_left = STATUE_TRIGGER_MS
        if self.stage is not None:
            self.stage.advance(milliseconds)
            if self.trigger_left > 0:
                self.trigger_left = max(0, self.trigger_left - milliseconds)
                if self.trigger_left == 0:
                    # Select stand after advancing the final trigger frame;
                    # 0042A7E0 retains the frame and time remainder.
                    self.stage.frames = 1
        if self.hero is not None:
            self.hero.advance(milliseconds)
