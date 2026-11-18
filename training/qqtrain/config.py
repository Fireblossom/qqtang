"""Trainer configuration: TOML files merged over built-in defaults.

TOML is read with the standard library (``tomllib``), so configs need no extra
dependency. Every key that a config may set appears in ``DEFAULTS``; unknown
keys are rejected so typos fail loudly. The ``worker.config`` and
``eval.modes.*.worker_config`` tables are opaque: they are passed to the worker
as its JSON config (schema in ``cmd/qqt-train-worker/config.go``).
"""

from __future__ import annotations

import copy
import json
import tomllib
from pathlib import Path
from typing import Any

# Opaque tables whose keys belong to another component's schema.
_OPAQUE = {"worker.config", "worker.env", "model.kwargs", "baseline.kwargs", "eval.modes"}

DEFAULTS: dict[str, Any] = {
    "run": {
        "dir": "runs/default",  # relative paths resolve against the config file's parent of configs/ (training/)
        "seed": 1,
        "device": "auto",  # auto | cpu | mps | cuda
        "total_agent_steps": 1_000_000,  # stop after this many trained learner decisions
        "max_updates": 0,  # 0 = unlimited
        "max_minutes": 0.0,  # 0 = unlimited; wall-clock limit (smoke tests)
        "log_every": 1,  # updates
        "checkpoint_every": 50,  # updates
        "keep_checkpoints": 3,
        "resume": "auto",  # auto = latest checkpoint in run dir if present; "" = never; or a path
        "tensorboard": False,  # needs the tensorboard package; JSON lines are always written
    },
    "worker": {
        # Command line of one worker; "{python}" is replaced by sys.executable and
        # "{platform}" by e.g. linux-amd64 / darwin-arm64 (training/tools/build-worker.sh
        # names). A relative executable path resolves against training/.
        # "-client-root <dir> -config <json>" is appended.
        "command": ["bin/qqt-train-worker-{platform}"],
        "client_root": "",
        "num_workers": 2,
        "groups": 2,  # pipelining groups: the policy runs on one group while the others step
        "seed_stride": 1000,  # worker i gets config seed = run.seed * 1_000_003 + i * seed_stride
        "step_timeout_s": 120.0,
        "max_restarts": 20,
        "env": {},  # extra environment variables for worker processes (e.g. GOMAXPROCS)
        "config": {"envs": 8, "spatial_dtype": "f16"},
    },
    "model": {
        "factory": "auto",  # auto | standin | qqtrain.model  (auto: R2's model if importable)
        "init_from_baseline": False,  # start the learner from U2392's weights
        "compile": False,  # torch.compile the sequence forward
        "kwargs": {},
    },
    "baseline": {
        "onnx": "../configs/models/qqtang-rule1.onnx",
        "kwargs": {},
    },
    "ppo": {
        "rollout_steps": 64,  # decisions per slot per rollout (T)
        "seq_len": 32,  # truncated BPTT chunk length
        "epochs": 3,
        "minibatches": 4,  # per epoch (chunks are split evenly)
        "gamma": 0.995,
        "gae_lambda": 0.95,
        "clip": 0.2,
        "value_clip": 0.2,
        "value_coef": 0.5,
        "entropy_coef": 0.01,
        "entropy_coef_final": 0.003,
        "target_kl": 0.03,  # stop the epoch loop when the approx KL exceeds this (0 = off)
        "max_grad_norm": 0.5,
        "lr": 3e-4,
        "lr_final": 3e-5,
        "lr_warmup_updates": 10,
        "lr_schedule": "cosine",  # cosine | linear | constant
        "adam_eps": 1e-5,
        "weight_decay": 0.0,
        "norm_adv": True,
        "value_warmup_updates": 0,  # policy loss and entropy off for the first N updates
        "bf16": True,  # autocast on CUDA only
        "tf32": True,
        "storage_device": "auto",  # auto = policy device; "cpu" to keep rollouts in host memory
    },
    "league": {
        "self_play_prob": 0.5,  # opponent = latest learner
        "train_both_sides": True,  # when the opponent is the latest learner, train every team
        "baseline_prob": 0.2,  # floor share of the frozen baseline among non-self-play games
        "snapshot_every": 20,  # updates
        "max_snapshots": 20,
        "resident": 4,  # distinct frozen opponents kept on the device per rollout
        "pfsp_power": 2.0,  # weight = (1 - win_rate) ** power
        "opponent_min_p": 0.1,  # opponents decode like the deployed server (MinP 0.1, T 1.0)
        "opponent_temperature": 1.0,
        "include_random": False,  # a uniform-random opponent in the pool (debugging)
    },
    "eval": {
        "every": 0,  # updates; 0 = off
        "num_workers": 2,
        "episodes": 64,  # per mode and opponent
        "seed": 777,
        "min_p": 0.1,
        "temperature": 1.0,
        "opponents": ["baseline"],  # baseline | latest_snapshots:<n>
        "export_best": True,
        "modes": {},  # name -> {worker_config = {...}}
    },
}


def _merge(base: dict, over: dict, path: str = "", strict: bool = True) -> dict:
    """Deep merge; keys are checked against the defaults except inside opaque tables."""
    out = copy.deepcopy(base)
    for key, value in over.items():
        full = f"{path}.{key}" if path else key
        if strict and key not in out and full not in _OPAQUE:
            raise KeyError(f"unknown config key {full!r}")
        if isinstance(value, dict) and isinstance(out.get(key), dict):
            out[key] = _merge(out[key], value, full, strict and full not in _OPAQUE)
        else:
            out[key] = copy.deepcopy(value)
    return out


def load_config(path: str | Path | None = None, overrides: dict | None = None) -> dict:
    """Load a TOML (or JSON) config over the defaults.

    Relative paths inside the config (run.dir, worker.command[0] when it
    contains a slash, worker.client_root, baseline.onnx) are resolved against
    the training/ directory, i.e. the parent of the config file's directory
    when the file lives in training/configs, else the file's directory.
    """
    cfg = copy.deepcopy(DEFAULTS)
    base_dir = Path.cwd()
    if path is not None:
        path = Path(path).resolve()
        text = path.read_bytes()
        data = json.loads(text) if path.suffix == ".json" else tomllib.loads(text.decode())
        cfg = _merge(cfg, data)
        base_dir = path.parent.parent if path.parent.name == "configs" else path.parent
    if overrides:
        cfg = _merge(cfg, overrides)
    cfg["_base_dir"] = str(base_dir)
    return cfg


def resolve_path(cfg: dict, value: str) -> str:
    if not value:
        return value
    p = Path(value).expanduser()
    if not p.is_absolute():
        p = Path(cfg.get("_base_dir", ".")) / p
    return str(p)


def parse_override(text: str) -> dict:
    """``a.b.c=value`` -> nested dict; value parsed as TOML when possible."""
    key, _, raw = text.partition("=")
    if not _:
        raise ValueError(f"override {text!r} must be key=value")
    try:
        value = tomllib.loads(f"v = {raw}")["v"]
    except tomllib.TOMLDecodeError:
        value = raw
    out: dict = {}
    cur = out
    parts = key.strip().split(".")
    for part in parts[:-1]:
        cur = cur.setdefault(part, {})
    cur[parts[-1]] = value
    return out


def platform_tag() -> str:
    import platform
    import sys

    machine = platform.machine().lower()
    arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(machine, machine)
    return f"{sys.platform}-{arch}"


def worker_command(cfg: dict) -> list[str]:
    """``worker.command`` with placeholders filled and a relative executable resolved against training/."""
    cmd = [c.replace("{platform}", platform_tag()) for c in cfg["worker"]["command"]]
    if cmd and "/" in cmd[0] and not cmd[0].startswith("{"):
        cmd[0] = resolve_path(cfg, cmd[0])
    return cmd
