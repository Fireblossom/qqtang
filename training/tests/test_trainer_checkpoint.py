"""Checkpoint save/resume round trip through the Trainer."""

from __future__ import annotations

import json

import numpy as np
import torch
from trainer_helpers import mock_cfg

from qqtrain.checkpoint import latest_checkpoint, list_checkpoints, load_checkpoint
from qqtrain.train import Trainer


def test_checkpoint_round_trip_and_resume(tmp_path):
    cfg = mock_cfg(
        tmp_path,
        run={"max_updates": 3, "checkpoint_every": 1, "keep_checkpoints": 2, "resume": "auto"},
        league={"snapshot_every": 2},
    )
    t1 = Trainer(cfg)
    t1.run()
    ckpts = list_checkpoints(t1.run_dir)
    assert [p.name for p in ckpts] == ["ckpt_000002.pt", "ckpt_000003.pt"]  # pruned to 2
    ck = load_checkpoint(latest_checkpoint(t1.run_dir))
    assert ck["counters"]["update"] == 3 and ck["counters"]["agent_steps"] > 0
    assert any(m["id"] == "snap_000002" for m in ck["league"]["members"])

    cfg2 = json.loads(json.dumps(cfg))
    cfg2["run"]["max_updates"] = 4
    t2 = Trainer(cfg2)
    for k, v in t1.policy.state_dict().items():
        torch.testing.assert_close(t2.policy.state_dict()[k], v)
    s1, s2 = t1.ppo.opt.state_dict(), t2.ppo.opt.state_dict()
    assert s1["param_groups"][0]["lr"] == s2["param_groups"][0]["lr"]
    for k in s1["state"]:
        torch.testing.assert_close(s1["state"][k]["exp_avg"], s2["state"][k]["exp_avg"])
    assert t2.counters == t1.counters
    assert t2.league.summary() == t1.league.summary()
    assert torch.equal(torch.get_rng_state(), ck["rng"]["torch"])
    assert np.random.get_state()[1].tolist() == ck["rng"]["numpy"][1].tolist()
    hist = t2.run()
    assert len(hist) == 1 and hist[0]["update"] == 4
    assert latest_checkpoint(t2.run_dir).name == "ckpt_000004.pt"
