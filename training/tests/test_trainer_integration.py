"""Trainer with the real network (QQTPolicy), the frozen U2392 baseline and, when
available, the real Go worker.

* ``test_qqtpolicy_end_to_end``: mock worker, learner initialised from U2392,
  U2392 as the league baseline (``QQTPolicy.from_onnx``), evaluation and the
  best-policy ONNX export through ``qqtrain.export``.
* ``test_real_worker``: runs only when the worker binary
  (training/bin/qqt-train-worker-<platform>, training/tools/build-worker.sh)
  and the client data (env QQT_CLIENT_ROOT, the client-patched directory) exist.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest
from trainer_helpers import mock_cfg

from qqtrain.config import load_config, platform_tag
from qqtrain.model import QQTPolicy
from qqtrain.train import Trainer

TRAINING = Path(__file__).resolve().parents[1]
ONNX = TRAINING.parent / "configs" / "models" / "qqtang-rule1.onnx"


def test_qqtpolicy_end_to_end(tmp_path):
    cfg = mock_cfg(
        tmp_path,
        run={"max_updates": 2},
        worker={"num_workers": 2, "config": {"envs": 2, "agents": 4, "min_len": 4, "max_len": 8}},
        model={"factory": "qqtrain.model", "init_from_baseline": True},
        baseline={"onnx": str(ONNX)},
        ppo={"rollout_steps": 4, "seq_len": 4, "value_warmup_updates": 1},
        league={"self_play_prob": 0.0, "snapshot_every": 1},
        eval={"every": 2, "num_workers": 1, "episodes": 2, "opponents": ["baseline"], "modes": {"1v1": {"worker_config": {"player_counts": [2]}}}},
    )
    cfg["model"]["kwargs"] = {}  # the helper's stand-in sizes do not apply
    t = Trainer(cfg)
    assert isinstance(t.policy, QQTPolicy)
    hist = t.run()
    assert [h["update"] for h in hist] == [1, 2]
    assert hist[0]["value_warmup"] == 1.0 and hist[1]["value_warmup"] == 0.0
    assert isinstance(t.league.model("baseline"), QQTPolicy)
    run = Path(cfg["run"]["dir"])
    best = json.loads((run / "export" / "best_eval.json").read_text())
    assert best["update"] == 2
    meta = json.loads((run / "export" / "best.onnx.json").read_text())
    assert meta["contract_version"] == 2 and (run / "export" / "best.onnx").stat().st_size > 1_000_000


WORKER = TRAINING / "bin" / f"qqt-train-worker-{platform_tag()}"
CLIENT = os.environ.get("QQT_CLIENT_ROOT", str(Path.home() / "Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched"))


@pytest.mark.skipif(not (WORKER.exists() and Path(CLIENT, "map").is_dir()), reason="worker binary or client data missing")
def test_real_worker(tmp_path):
    cfg = load_config(
        None,
        {
            "run": {"dir": str(tmp_path / "run"), "device": "cpu", "resume": "", "max_updates": 3},
            "worker": {
                "command": [str(WORKER)],
                "client_root": CLIENT,
                "num_workers": 2,
                "config": {"envs": 4, "agents": 4, "player_counts": [2, 4], "workers": 1},
            },
            "model": {"factory": "standin"},
            "ppo": {"rollout_steps": 16, "seq_len": 8, "epochs": 1},
            "league": {"self_play_prob": 0.5, "snapshot_every": 2},
        },
    )
    hist = Trainer(cfg).run()
    assert len(hist) == 3 and all(h["samples"] > 0 for h in hist)
    assert all(h["worker_crashes"] == 0 for h in hist)
