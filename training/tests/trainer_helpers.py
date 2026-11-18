"""Shared helpers for the trainer tests (mock worker configs, toy policies)."""

from __future__ import annotations

import torch
import torch.nn as nn

from qqtrain.config import load_config

MOCK_CMD = ["{python}", "-m", "qqtrain.mock_worker"]


def mock_cfg(tmp_path, **sections) -> dict:
    over = {
        "run": {"dir": str(tmp_path / "run"), "device": "cpu", "resume": ""},
        "worker": {"command": MOCK_CMD, "num_workers": 2, "groups": 2, "step_timeout_s": 20.0, "config": {"envs": 4, "agents": 4}},
        "model": {"factory": "standin", "kwargs": {"conv_channels": 4, "hidden": 32}},
        "ppo": {"rollout_steps": 8, "seq_len": 4, "epochs": 1, "minibatches": 2},
        "league": {"resident": 2},
    }
    for k, v in sections.items():
        base = over.setdefault(k, {})
        for kk, vv in v.items():
            if isinstance(vv, dict) and isinstance(base.get(kk), dict):
                base[kk] = {**base[kk], **vv}
            else:
                base[kk] = vv
    return load_config(None, over)


class OraclePolicy(nn.Module):
    """Picks the mock's target action (one-hot in scalars[0:45])."""

    def __init__(self):
        super().__init__()
        self.register_buffer("_d", torch.zeros(1))

    def forward(self, spatial, scalars, legal, memory, reset):
        logits = (scalars[:, :45].float() * 20.0).masked_fill(~legal.bool(), float("-inf"))
        return logits, torch.zeros(scalars.shape[0]), memory
