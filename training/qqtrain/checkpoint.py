"""Checkpoint save/resume and run logging (JSON lines, optional TensorBoard)."""

from __future__ import annotations

import json
import logging
import os
import random
import re
import time
from pathlib import Path

import numpy as np
import torch

log = logging.getLogger(__name__)

_CKPT = re.compile(r"ckpt_(\d+)\.pt$")


def rng_state() -> dict:
    state = {
        "python": random.getstate(),
        "numpy": np.random.get_state(),
        "torch": torch.get_rng_state(),
    }
    if torch.cuda.is_available():
        state["cuda"] = torch.cuda.get_rng_state_all()
    return state


def set_rng_state(state: dict) -> None:
    random.setstate(state["python"])
    np.random.set_state(state["numpy"])
    torch.set_rng_state(state["torch"])
    if "cuda" in state and torch.cuda.is_available():
        try:
            torch.cuda.set_rng_state_all(state["cuda"])
        except RuntimeError as exc:  # different GPU count
            log.warning("CUDA RNG state not restored: %s", exc)


def save_checkpoint(run_dir: str | Path, update: int, state: dict, keep: int = 3) -> Path:
    """Atomically write ``checkpoints/ckpt_<update>.pt`` and prune old ones."""
    d = Path(run_dir) / "checkpoints"
    d.mkdir(parents=True, exist_ok=True)
    path = d / f"ckpt_{update:06d}.pt"
    tmp = path.with_suffix(".tmp")
    torch.save(state, tmp)
    os.replace(tmp, path)
    ckpts = list_checkpoints(run_dir)
    for old in ckpts[:-keep] if keep > 0 else []:
        try:
            old.unlink()
        except OSError:
            pass
    return path


def list_checkpoints(run_dir: str | Path) -> list[Path]:
    d = Path(run_dir) / "checkpoints"
    if not d.is_dir():
        return []
    found = [(int(m.group(1)), p) for p in d.iterdir() if (m := _CKPT.search(p.name))]
    return [p for _, p in sorted(found)]


def latest_checkpoint(run_dir: str | Path) -> Path | None:
    ck = list_checkpoints(run_dir)
    return ck[-1] if ck else None


def load_checkpoint(path: str | Path, map_location="cpu") -> dict:
    # Checkpoints contain numpy RNG state and config dicts, so full unpickling is needed.
    # Only load checkpoints you wrote yourself.
    return torch.load(path, map_location=map_location, weights_only=False)


class MetricsLogger:
    """Appends one JSON object per update to ``metrics.jsonl``; mirrors scalars to TensorBoard if enabled."""

    def __init__(self, run_dir: str | Path, tensorboard: bool = False):
        self.path = Path(run_dir) / "metrics.jsonl"
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.tb = None
        if tensorboard:
            try:
                from torch.utils.tensorboard import SummaryWriter

                self.tb = SummaryWriter(str(Path(run_dir) / "tb"))
            except Exception as exc:  # noqa: BLE001 - optional dependency
                log.warning("TensorBoard unavailable (%s); JSON lines only", exc)

    def log(self, step: int, metrics: dict) -> None:
        rec = {"step": step, "time": time.time(), **metrics}
        with open(self.path, "a") as f:
            f.write(json.dumps(rec, default=_jsonable) + "\n")
        if self.tb is not None:
            for k, v in _flatten(metrics).items():
                if isinstance(v, (int, float)) and not isinstance(v, bool):
                    self.tb.add_scalar(k, v, step)

    def close(self) -> None:
        if self.tb is not None:
            self.tb.close()


def _jsonable(x):
    if isinstance(x, (np.integer,)):
        return int(x)
    if isinstance(x, (np.floating,)):
        return float(x)
    if isinstance(x, np.ndarray):
        return x.tolist()
    return str(x)


def _flatten(d: dict, prefix: str = "") -> dict:
    out = {}
    for k, v in d.items():
        key = f"{prefix}/{k}" if prefix else str(k)
        if isinstance(v, dict):
            out.update(_flatten(v, key))
        else:
            out[key] = v
    return out
