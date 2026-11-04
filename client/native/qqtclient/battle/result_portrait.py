"""005DD365/38D pose selection and 004262F0 per-part 200ms clocks."""

import dataclasses


@dataclasses.dataclass
class PortraitPart:
    count: int
    frame: int = 0
    remainder: int = 0

    def advance(self, milliseconds):
        if self.count <= 0:
            return
        self.remainder += milliseconds
        steps, self.remainder = divmod(self.remainder, 200)
        self.frame += steps
        if self.frame >= self.count:
            self.frame = 0  # native loses whole-frame overshoot, keeps time remainder


class ResultPortrait:
    def __init__(self, role, result, frame_counts, base=30, team=0):
        self.role, self.team = role, team
        self.action = self.pose(role, result)
        self.position = (base + 28, 74 if role == 41 else 84)
        self.shadow_position = (base + 58, 133 if role == 41 else 143)
        self.parts = {name: PortraitPart(count) for name, count in frame_counts.items()}

    @staticmethod
    def pose(role, result):
        return ("lose" if result == 0 else "win") if role < 32 else "stand"

    def advance(self, milliseconds):
        for part in self.parts.values():
            part.advance(milliseconds)

    def frames(self):
        return tuple(sorted((name, part.frame) for name, part in self.parts.items()))
