"""End-to-end smoke run: mock workers + stand-in policy learn the mock's target action.

Runs configs/smoke.toml for up to ~2 minutes (shortened by QQTRAIN_SMOKE_MINUTES)
and checks that the loop runs (rollouts, PPO, league snapshots, evaluation,
checkpoints) and that the trivially learnable reward goes up.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import numpy as np

from qqtrain.config import load_config
from qqtrain.train import Trainer

CONFIG = Path(__file__).resolve().parents[1] / "configs" / "smoke.toml"


def test_smoke_training_learns(tmp_path):
    minutes = float(os.environ.get("QQTRAIN_SMOKE_MINUTES", "1.75"))
    cfg = load_config(CONFIG, {"run": {"dir": str(tmp_path / "smoke"), "max_minutes": minutes}, "eval": {"every": 15, "episodes": 16}})
    trainer = Trainer(cfg)
    hist = trainer.run()
    assert len(hist) >= 15, len(hist)
    r = np.array([h["reward_per_decision"] for h in hist])
    ent = np.array([h["entropy"] for h in hist])
    k = max(3, len(hist) // 5)
    first, last = r[:k].mean(), r[-k:].mean()
    print(f"updates={len(hist)} reward/decision first={first:.4f} last={last:.4f} entropy {ent[:k].mean():.3f}->{ent[-k:].mean():.3f}")
    assert last > first + 0.004  # random play ~0.004, learned play approaches 0.05
    assert ent[-k:].mean() < ent[:k].mean()
    assert all(np.isfinite(h["loss"]) for h in hist)
    run = Path(cfg["run"]["dir"])
    assert (run / "metrics.jsonl").exists() and list((run / "checkpoints").glob("ckpt_*.pt"))
    assert list((run / "league").glob("snap_*.pt"))
    evals = [json.loads(line) for line in (run / "eval.jsonl").read_text().splitlines()]
    assert evals and {"1v1", "2v2"} <= set(evals[-1]["modes"])
    assert "baseline" in evals[-1]["elo"]
