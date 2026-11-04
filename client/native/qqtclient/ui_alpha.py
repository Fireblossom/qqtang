"""gameUI base-widget alpha math (1002A017 / 100297F3, D3D PC24)."""

import struct

_F32 = struct.Struct("<f")


def float32(value: float) -> float:
    return _F32.unpack(_F32.pack(value))[0]


def advance_alpha(alpha: float, speed: float, minimum: float, direction: int) -> tuple[float, int, bool]:
    """One native widget update; only strict overshoot ends the fade."""
    if direction == 1:
        alpha = float32(alpha + speed)
        if alpha > 1.0:
            return 1.0, 0, False
    elif direction == 2:
        alpha = float32(alpha - speed)
        if alpha < minimum:
            return minimum, 0, True
    return alpha, direction, False


def combined_alpha(alpha: float, parent: float | None) -> float:
    return alpha if parent is None else float32(alpha * parent)


def alpha_byte(alpha: float, color_alpha: int = 255) -> int:
    return int(float32(alpha * color_alpha)) & 255
