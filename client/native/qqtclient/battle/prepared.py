"""Prepared-prop availability and mask parameters from Client.exe 005DAC82."""

from __future__ import annotations


def ready(cooldown: dict, clock: int) -> bool:
    last = cooldown["last_use"]
    return (cooldown["allowed"] and clock >= cooldown["initial"]
            and (not last or ((clock - last) & 0xFFFFFFFF) >= cooldown["interval"]))


def mask(cooldown: dict | None, clock: int) -> tuple[float, float]:
    """Return current angle and degrees/sec; destination is always 360.

    Native InitItemTime leaves completed/unrestricted slots at 360. The
    writer's reuse animation adds 400ms to its speed denominator only.
    """
    if cooldown is None:
        return 360.0, 0.0
    if not cooldown["allowed"]:
        return 0.0, 0.0
    angle, speed = 360.0, 0.0
    initial = cooldown["initial"]
    if clock < initial:
        angle, speed = clock * 360.0 / initial, 360000.0 / initial
    last, interval = cooldown["last_use"], cooldown["interval"]
    if last and interval:
        elapsed = (clock - last) & 0xFFFFFFFF
        angle, speed = min(360.0, elapsed * 360.0 / interval), 360000.0 / (interval + 400)
    return angle, speed
