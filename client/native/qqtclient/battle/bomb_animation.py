"""Each bubble owns a picture clock; flight updates only its position."""

import dataclasses

from .animation import FrameLoop


@dataclasses.dataclass
class BombAnimation(FrameLoop):
    flying: bool = False

    def advance(self, milliseconds: int, flying: bool = False) -> None:
        # Sidecar exports the state after movement. The landing callback
        # selects stand but does not update the picture until the next tick.
        if not flying and not self.flying:
            super().advance(milliseconds)
        self.flying = flying
