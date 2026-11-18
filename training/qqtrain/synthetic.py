"""Plausible random policy inputs for tests and benchmarks (not real observations)."""

from __future__ import annotations

import numpy as np

from .model import ACTIONS, CHANNELS, HEIGHT, HIDDEN, SCALARS, WIDTH


def random_inputs(batch: int, seed: int = 0) -> dict[str, np.ndarray]:
    """Sparse spatial planes with one self cell, some allies/enemies, scalars in
    [0,1], legal masks that always allow waiting, memory in [-1,1] and random
    resets."""
    rng = np.random.default_rng(seed)
    spatial = (rng.random((batch, CHANNELS, HEIGHT, WIDTH)) < 0.08).astype(np.float32)
    spatial *= rng.random(spatial.shape, dtype=np.float32)
    spatial[:, 7:13] = 0
    for b in range(batch):
        r, c = rng.integers(HEIGHT), rng.integers(WIDTH)
        spatial[b, 7 + int(rng.random() < 0.1), r, c] = 1
        for _ in range(rng.integers(0, 3)):
            spatial[b, 9 + int(rng.random() < 0.2), rng.integers(HEIGHT), rng.integers(WIDTH)] = 1
        for _ in range(rng.integers(0, 4)):
            spatial[b, 11 + int(rng.random() < 0.2), rng.integers(HEIGHT), rng.integers(WIDTH)] = 1
    if batch > 1:
        spatial[0, 7:9] = 0  # actor not on the board (eliminated / inactive slot)
    scalars = rng.random((batch, SCALARS), dtype=np.float32)
    scalars[:, 112:182] = rng.random((batch, 70)) < 0.5
    legal = rng.random((batch, ACTIONS)) < 0.4
    legal[:, 0] = True
    memory = rng.uniform(-1, 1, (batch, HIDDEN)).astype(np.float32)
    reset = rng.random(batch) < 0.3
    return {"spatial": spatial, "scalars": scalars, "legal": legal, "memory": memory, "reset": reset}
