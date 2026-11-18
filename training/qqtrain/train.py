"""Training entry point.

    uv run python -m qqtrain.train --config configs/smoke.toml
    uv run python -m qqtrain.train --config configs/h100.toml --set run.dir=/mlde/qqtang-rl/runs/h100-a

Every config key can be overridden with ``--set section.key=value`` (value
parsed as TOML). The run directory holds ``train.log``, ``metrics.jsonl``,
``eval.jsonl``, ``checkpoints/``, ``league/`` snapshots, ``export/`` (best
policy) and the worker logs/configs. With ``run.resume = "auto"`` (default)
a restarted run continues from its latest checkpoint, so the same command can
simply be re-run after the cluster container is reclaimed. SIGTERM/SIGINT
finish the current update, write a checkpoint and exit.
"""

from __future__ import annotations

import argparse
import json
import logging
import random
import signal
import sys
import time
from pathlib import Path

import numpy as np
import torch

from .checkpoint import MetricsLogger, latest_checkpoint, load_checkpoint, rng_state, save_checkpoint, set_rng_state
from .config import _merge, load_config, parse_override, resolve_path, worker_command
from .evaluate import Evaluator
from .league import League
from .policy_api import load_baseline, make_policy
from .ppo import PPO
from .rollout import Collector, compute_gae
from .worker import WorkerPool, worker_configs

log = logging.getLogger("qqtrain")


def pick_device(name: str) -> torch.device:
    if name == "auto":
        if torch.cuda.is_available():
            return torch.device("cuda")
        return torch.device("cpu")  # MPS only on request: small batches run faster on the CPU
    return torch.device(name)


def setup_logging(run_dir: Path) -> None:
    run_dir.mkdir(parents=True, exist_ok=True)
    fmt = logging.Formatter("%(asctime)s %(levelname)s %(name)s: %(message)s")
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    for h in list(root.handlers):
        if getattr(h, "_qqtrain", False):
            root.removeHandler(h)
    for h in (logging.StreamHandler(sys.stdout), logging.FileHandler(run_dir / "train.log")):
        h.setFormatter(fmt)
        h._qqtrain = True  # type: ignore[attr-defined]
        root.addHandler(h)


class Trainer:
    def __init__(self, cfg: dict):
        self.cfg = cfg
        rc = cfg["run"]
        self.run_dir = Path(resolve_path(cfg, rc["dir"]))
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.device = pick_device(rc["device"])
        seed = int(rc["seed"])
        random.seed(seed)
        np.random.seed(seed)
        torch.manual_seed(seed)
        if cfg["ppo"].get("tf32", True) and self.device.type == "cuda":
            torch.backends.cuda.matmul.allow_tf32 = True
            torch.backends.cudnn.allow_tf32 = True
        cfg["baseline"]["onnx"] = resolve_path(cfg, cfg["baseline"]["onnx"])
        self.policy = make_policy(cfg).to(self.device)
        if cfg["model"].get("init_from_baseline"):
            base = load_baseline(cfg)
            missing, unexpected = self.policy.load_state_dict(base.state_dict(), strict=False)
            log.info("initialised from the baseline (missing %s, unexpected %s)", missing, unexpected)
        nparams = sum(p.numel() for p in self.policy.parameters())
        log.info("policy %s with %d parameters on %s", type(self.policy).__name__, nparams, self.device)
        self.league = League(
            cfg,
            self.run_dir,
            lambda: make_policy(cfg),
            lambda: load_baseline(cfg),
            self.device,
            np.random.default_rng(seed + 1),
        )
        self.ppo = PPO(self.policy, cfg, self.device)
        self.evaluator = Evaluator(cfg, self.run_dir, self.league, self.device)
        self.counters = {"update": 0, "agent_steps": 0, "env_steps": 0, "wall_s": 0.0}
        self.metrics = MetricsLogger(self.run_dir, bool(rc.get("tensorboard")))
        self.stop = False
        self.history: list[dict] = []
        self._maybe_resume()
        (self.run_dir / "config.json").write_text(json.dumps(cfg, indent=1, default=str))

    # ------------------------------------------------------------------ state
    def state(self) -> dict:
        return {
            "policy": {k: v.detach().cpu() for k, v in self.policy.state_dict().items()},
            "ppo": self.ppo.state_dict(),
            "league": self.league.state_dict(),
            "evaluator": self.evaluator.state_dict(),
            "counters": dict(self.counters),
            "rng": rng_state(),
            "config": self.cfg,
            "policy_class": type(self.policy).__name__,
        }

    def save(self) -> Path:
        path = save_checkpoint(self.run_dir, self.counters["update"], self.state(), int(self.cfg["run"]["keep_checkpoints"]))
        log.info("checkpoint %s", path)
        return path

    def _maybe_resume(self) -> None:
        spec = self.cfg["run"].get("resume", "auto")
        if not spec:
            return
        path = latest_checkpoint(self.run_dir) if spec == "auto" else Path(resolve_path(self.cfg, spec))
        if path is None:
            return
        ck = load_checkpoint(path)
        self.policy.load_state_dict(ck["policy"])
        self.ppo.load_state_dict(ck["ppo"])
        self.league.load_state_dict(ck["league"])
        self.evaluator.load_state_dict(ck.get("evaluator", {}))
        self.counters.update(ck["counters"])
        set_rng_state(ck["rng"])
        log.info("resumed from %s at update %d (%d agent steps)", path, self.counters["update"], self.counters["agent_steps"])

    # ------------------------------------------------------------------ loop
    def _install_signals(self) -> None:
        def handler(signum, _frame):
            log.warning("signal %d: stopping after this update", signum)
            self.stop = True

        try:
            signal.signal(signal.SIGTERM, handler)
            signal.signal(signal.SIGINT, handler)
        except ValueError:  # not the main thread (tests)
            pass

    def _done(self, t_start: float) -> bool:
        rc = self.cfg["run"]
        c = self.counters
        if self.stop:
            return True
        if rc["total_agent_steps"] and c["agent_steps"] >= int(rc["total_agent_steps"]):
            return True
        if rc["max_updates"] and c["update"] >= int(rc["max_updates"]):
            return True
        if rc["max_minutes"] and (time.time() - t_start) / 60.0 >= float(rc["max_minutes"]):
            return True
        return False

    def run(self) -> list[dict]:
        cfg, rc, wc = self.cfg, self.cfg["run"], self.cfg["worker"]
        self._install_signals()
        pool = WorkerPool(
            worker_command(cfg),
            resolve_path(cfg, wc["client_root"]),
            worker_configs(cfg, seed=int(rc["seed"]) + self.counters["update"]),
            self.run_dir / "workers",
            groups=int(wc["groups"]),
            pin_memory=self.device.type == "cuda",
            step_timeout_s=float(wc["step_timeout_s"]),
            max_restarts=int(wc["max_restarts"]),
            env=wc.get("env", {}),
        )
        sd = cfg["ppo"].get("storage_device", "auto")
        storage_device = self.device if sd == "auto" else torch.device(sd)
        t_start = time.time()
        try:
            pool.start()
            collector = Collector(cfg, pool, self.policy, self.league, self.device, storage_device)
            log.info(
                "rollout storage %.2f GB on %s for %d slots x %d rows",
                collector.storage.nbytes() / 1e9,
                storage_device,
                collector.S,
                collector.storage.R,
            )
            while not self._done(t_start):
                t0 = time.perf_counter()
                stats = collector.collect()
                adv, ret = compute_gae(collector.storage, float(cfg["ppo"]["gamma"]), float(cfg["ppo"]["gae_lambda"]))
                progress = self.counters["agent_steps"] / max(int(rc["total_agent_steps"]), 1)
                m = self.ppo.update(collector.storage, adv, ret, progress)
                collector.advance()
                c = self.counters
                c["update"] += 1
                c["agent_steps"] += int(m.get("samples", 0))
                c["env_steps"] += int(stats.get("env_steps", 0))
                dt = time.perf_counter() - t0
                c["wall_s"] += dt
                rec = self._record(m, stats, collector, dt)
                collector.reset_stats()
                u = c["update"]
                if u % int(cfg["league"]["snapshot_every"]) == 0:
                    sid = self.league.add_snapshot(self.policy, u)
                    log.info("league snapshot %s", sid)
                ev_every = int(cfg["eval"]["every"])
                if ev_every and u % ev_every == 0:
                    report = self.evaluator.run(self.policy, u)
                    rec["eval"] = _eval_summary(report)
                    self.evaluator.maybe_export_best(self.policy, report, u)
                    log.info("eval @%d: %s", u, json.dumps(rec["eval"]))
                if u % int(rc["log_every"]) == 0:
                    self.metrics.log(u, rec)
                    log.info(_fmt(rec))
                self.history.append(rec)
                if u % int(rc["checkpoint_every"]) == 0:
                    self.save()
        finally:
            try:
                if self.counters["update"] > 0:
                    self.save()
            finally:
                pool.close()
                self.metrics.close()
        return self.history

    def _record(self, m: dict, stats: dict, collector: Collector, dt: float) -> dict:
        c = self.counters
        ld = max(stats.get("learner_decisions", 0.0), 1.0)
        rets = list(collector.recent_returns)
        results = {}
        for opp, dq in collector.recent_results.items():
            if dq:
                n = len(dq)
                results[opp] = round((sum(r == "win" for r in dq) + 0.5 * sum(r == "draw" for r in dq)) / n, 4)
        rec = {
            "update": c["update"],
            "agent_steps": c["agent_steps"],
            "env_steps": c["env_steps"],
            "reward_per_decision": stats.get("learner_reward", 0.0) / ld,
            "episode_return": float(np.mean(rets[-200:])) if rets else None,
            "learner_decisions_per_s": stats.get("learner_decisions", 0.0) / max(dt, 1e-9),
            "env_steps_per_s": stats.get("env_steps", 0.0) / max(dt, 1e-9),
            "collect_s": stats.get("collect_s", 0.0),
            "worker_wait_s": stats.get("wait_s", 0.0),
            "iteration_s": dt,
            "worker_crashes": stats.get("worker_crashes", 0.0),
            "score_vs": results,
            **{k: v for k, v in m.items()},
        }
        if c["update"] % 10 == 0:
            rec["league"] = self.league.summary()
        return rec


def _eval_summary(report: dict) -> dict:
    out = {"score_vs_baseline": report.get("score_vs_baseline")}
    for mode, opps in report["modes"].items():
        for opp, r in opps.items():
            out[f"{mode}/{opp}"] = {"w": r["win"], "d": r["draw"], "l": r["loss"], "score": round(r["score"], 3), "ci": [round(x, 3) for x in r["score_ci"]]}
    out["elo"] = {k: round(v) for k, v in report.get("elo", {}).items()}
    return out


def _fmt(rec: dict) -> str:
    keys = [
        ("update", "{}"),
        ("agent_steps", "{}"),
        ("reward_per_decision", "{:.4f}"),
        ("episode_return", "{:.3f}"),
        ("pg_loss", "{:.4f}"),
        ("v_loss", "{:.4f}"),
        ("entropy", "{:.3f}"),
        ("approx_kl", "{:.4f}"),
        ("explained_var", "{:.3f}"),
        ("learner_decisions_per_s", "{:.0f}"),
        ("iteration_s", "{:.2f}"),
    ]
    parts = []
    for k, f in keys:
        v = rec.get(k)
        if v is not None:
            parts.append(f"{k}={f.format(v)}")
    if rec.get("score_vs"):
        parts.append("score_vs=" + json.dumps(rec["score_vs"]))
    return " ".join(parts)


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="QQTang recurrent PPO self-play trainer")
    ap.add_argument("--config", required=True, help="TOML (or JSON) config, e.g. configs/smoke.toml")
    ap.add_argument("--set", action="append", default=[], help="override: section.key=value (TOML value)")
    args = ap.parse_args(argv)
    over: dict = {}
    for s in args.set:
        over = _merge(over, parse_override(s), strict=False)
    cfg = load_config(args.config, over)
    run_dir = Path(resolve_path(cfg, cfg["run"]["dir"]))
    setup_logging(run_dir)
    trainer = Trainer(cfg)
    trainer.run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
