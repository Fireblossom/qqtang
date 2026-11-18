"""Read observation dumps written by training/tools/dump_observations."""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np

from .model import ACTIONS, CHANNELS, HEIGHT, HIDDEN, SCALARS, WIDTH


def load_dump(directory: str | Path) -> dict[str, np.ndarray]:
    """Return spatial, scalars, legal (bool), memory, reset (bool), stream,
    logits and next_memory arrays of a dump directory (or a .npz fixture)."""
    directory = Path(directory)
    if directory.suffix == ".npz":
        with np.load(directory) as data:
            arrays = {key: data[key] for key in data.files}
        return arrays
    meta = json.loads((directory / "meta.json").read_text())
    count = meta["records"]

    def read(name: str, dtype, shape: tuple[int, ...]) -> np.ndarray:
        return np.fromfile(directory / name, dtype=dtype).reshape((count, *shape))

    return {
        "spatial": read("spatial.f32", "<f4", (CHANNELS, HEIGHT, WIDTH)),
        "scalars": read("scalars.f32", "<f4", (SCALARS,)),
        "legal": read("legal.u8", np.uint8, (ACTIONS,)).astype(bool),
        "memory": read("memory.f32", "<f4", (HIDDEN,)),
        "reset": read("reset.u8", np.uint8, ()).astype(bool),
        "stream": read("stream.i32", "<i4", ()),
        "logits": read("logits.f32", "<f4", (ACTIONS,)),
        "next_memory": read("next_memory.f32", "<f4", (HIDDEN,)),
    }


def save_fixture(arrays: dict[str, np.ndarray], path: str | Path, indices: np.ndarray) -> None:
    """Store a subset of records as a compressed .npz (values kept exact)."""
    np.savez_compressed(path, **{key: value[indices] for key, value in arrays.items()})
