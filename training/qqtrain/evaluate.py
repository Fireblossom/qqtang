"""Evaluation matches and Elo-style ratings.

Each evaluation mode (``[eval.modes.<name>]``, e.g. 1v1 and 2v2) starts its
own worker pool with the mode's worker config (no curricula) and a fixed seed,
once per opponent, so every opponent faces the same sequence of starting
states. Every environment plays a fixed quota of episodes; the learner side
alternates between the teams from one episode to the next (and across
environments), so map-side advantages cancel. Both sides decode like the
deployed server (MinP 0.1, temperature 1.0 by default) with a seeded
generator.

Reported per mode and opponent: wins/draws/losses, win rate with a Wilson
95 % interval, score (win=1, draw=0.5) with a normal 95 % interval. All
results accumulate in a match table from which Bradley-Terry/Elo ratings are
fitted (baseline U2392 anchored at 1000).

Standalone use::

    uv run python -m qqtrain.evaluate --config configs/smoke.toml --checkpoint runs/smoke/checkpoints/ckpt_000010.pt
"""

from __future__ import annotations

import argparse
import json
import logging
import math
import time
from contextlib import nullcontext
from pathlib import Path

import numpy as np
import torch
import torch.nn as nn

from .config import load_config, parse_override, resolve_path, worker_command
from .league import BASELINE, League, episode_teams, outcome_for_teams, wilson_interval
from .policy_api import MEMORY, deployed_sample, export_onnx, padded_forward
from .worker import OP_RESET, OP_STEP, WorkerPool, worker_configs

log = logging.getLogger(__name__)


def play_matches(
    pool: WorkerPool,
    learner: nn.Module,
    opponent: nn.Module,
    episodes: int,
    device: torch.device,
    min_p: float = 0.1,
    temperature: float = 1.0,
    seed: int = 0,
    max_steps: int = 1_000_000,
    bf16: bool = True,
) -> dict:
    """Play until every env has finished its quota; results from the learner's side."""
    E, P = pool.num_envs, pool.P
    quota = max(1, math.ceil(episodes / E))
    done_count = np.zeros(E, np.int64)
    learner_team = np.full(E, -1, np.int64)
    memory = torch.zeros((pool.num_slots, MEMORY), device=device)
    need_reset = np.ones(pool.num_slots, bool)
    slot_env = np.repeat(np.arange(E), P)
    gen = torch.Generator(device=device)
    gen.manual_seed(seed)
    counts = {"win": 0, "draw": 0, "loss": 0, "unknown": 0}
    lengths: list[int] = []
    b = pool.buffers
    bn = b.np
    ac = torch.autocast("cuda", dtype=torch.bfloat16) if (device.type == "cuda" and bf16) else nullcontext()

    def assign(envs):
        for e in envs:
            s0 = e * P
            q = slice(s0, s0 + P)
            teams = episode_teams(bn["active"][q], bn["reset"][q], bn["team"][q])
            learner_team[e] = teams[(e + done_count[e]) % len(teams)] if teams else -1

    pool.submit(0, OP_RESET)
    first = True
    steps = 0
    while (done_count < quota).any():
        steps += 1
        if steps > max_steps:
            raise RuntimeError("evaluation did not finish its episodes")
        res = pool.wait(0)
        for ep in res.episodes:
            e = int(ep["env"])
            if done_count[e] < quota:
                r = outcome_for_teams(ep, {int(learner_team[e])})
                counts[r or "unknown"] += 1
                if ep.get("length") is not None:
                    lengths.append(int(ep["length"]))
            done_count[e] += 1
        done = bn["done"]
        need_reset |= bn["reset"] | done
        if first:
            new_envs = np.arange(E)
            first = False
        else:
            new_envs = np.unique(np.concatenate([slot_env[done], res.crashed_envs])).astype(np.int64)
        if len(res.crashed_envs):
            need_reset[(res.crashed_envs[:, None] * P + np.arange(P)).reshape(-1)] = True
        assign(new_envs)
        active = bn["active"].copy()
        legal_np = bn["legal"]
        bad = active & ~legal_np.any(1)
        legal_np[bad, 0] = True
        is_learner = bn["team"].astype(np.int64) == learner_team[slot_env]
        actions = np.full(pool.num_slots, -1, np.int16)
        spatial = b.spatial.to(device)
        scalars = b.scalars.to(device)
        legal = b.legal.to(device)
        reset = torch.from_numpy(need_reset).to(device)
        with torch.inference_mode(), ac:
            for net, sel in ((learner, active & is_learner), (opponent, active & ~is_learner)):
                idx = np.nonzero(sel)[0]
                if len(idx) == 0:
                    continue
                it = torch.as_tensor(idx, device=device)
                logits, _v, nmem = padded_forward(net, spatial[it], scalars[it], legal[it], memory[it], reset[it], device.type != "cpu")
                a = deployed_sample(logits, legal[it], scalars[it], min_p=min_p, temperature=temperature, generator=gen)
                memory[it] = nmem.float()
                actions[idx] = a.cpu().numpy()
        need_reset[active] = False
        pool.submit(0, OP_STEP, actions)
    pool.wait(0)
    n = counts["win"] + counts["draw"] + counts["loss"]
    wlo, whi = wilson_interval(counts["win"], n)
    score = (counts["win"] + 0.5 * counts["draw"]) / max(n, 1)
    if n > 1:
        var = (counts["win"] * (1 - score) ** 2 + counts["draw"] * (0.5 - score) ** 2 + counts["loss"] * score**2) / (n - 1)
        half = 1.96 * math.sqrt(var / n)
    else:
        half = 0.5
    return {
        **counts,
        "n": n,
        "win_rate": counts["win"] / max(n, 1),
        "win_ci": [wlo, whi],
        "score": score,
        "score_ci": [max(0.0, score - half), min(1.0, score + half)],
        "mean_length": float(np.mean(lengths)) if lengths else 0.0,
    }


def fit_elo(table: dict[tuple[str, str], list[float]], anchor: str = BASELINE, anchor_rating: float = 1000.0, iters: int = 2000) -> dict[str, float]:
    """Bradley-Terry ratings on the Elo scale from (a, b) -> [wins, draws, losses] of a vs b.

    Draws count half. A weak prior (one virtual draw against the anchor) keeps
    ratings finite for perfect records.
    """
    players = sorted({p for k in table for p in k} | {anchor})
    ix = {p: i for i, p in enumerate(players)}
    games = [(ix[a], ix[b], w + 0.5 * d, w + d + l) for (a, b), (w, d, l) in table.items() if w + d + l > 0]
    games += [(ix[p], ix[anchor], 0.5, 1.0) for p in players if p != anchor]
    r = np.zeros(len(players))
    k = math.log(10) / 400.0
    for _ in range(iters):
        g = np.zeros_like(r)
        h = np.zeros_like(r)
        for a, b, s, n in games:
            e = 1.0 / (1.0 + math.exp(-k * (r[a] - r[b])))
            g[a] += s - n * e
            g[b] -= s - n * e
            h[a] += n * e * (1 - e)
            h[b] += n * e * (1 - e)
        # Damped per-player Newton step on the log-likelihood (ratings in Elo points).
        step = 0.5 * g / (k * np.maximum(h, 1e-6))
        step = np.clip(step, -100, 100)
        r += step
        r -= r[ix[anchor]]
        if np.abs(step).max() < 1e-3:
            break
    return {p: float(anchor_rating + r[ix[p]]) for p in players}


class Evaluator:
    def __init__(self, cfg: dict, run_dir: str | Path, league: League, device: torch.device):
        self.cfg = cfg
        self.ecfg = cfg["eval"]
        self.run_dir = Path(run_dir)
        self.league = league
        self.device = device
        self.table: dict[tuple[str, str], list[float]] = {}
        self.best_score = -1.0
        self.history: list[dict] = []

    def state_dict(self) -> dict:
        return {
            "table": [[a, b, *v] for (a, b), v in self.table.items()],
            "best_score": self.best_score,
            "history": self.history[-200:],
        }

    def load_state_dict(self, state: dict) -> None:
        self.table = {(a, b): [w, d, l] for a, b, w, d, l in state.get("table", [])}
        self.best_score = float(state.get("best_score", -1.0))
        self.history = list(state.get("history", []))

    def opponents(self, update: int | None = None) -> list[str]:
        """Evaluation opponents; snapshots taken at or after ``update`` hold
        the learner's own weights and are left out before taking the last k."""
        out: list[str] = []
        for spec in self.ecfg["opponents"]:
            if spec == BASELINE:
                out.append(BASELINE)
            elif spec.startswith("snapshots:"):
                k = int(spec.split(":", 1)[1])
                snaps = [m for m in self.league.snapshots() if update is None or m.update < update]
                out += [m.id for m in snaps[-k:]] if k > 0 else []
            elif spec in self.league.members:
                out.append(spec)
        return list(dict.fromkeys(out))

    def modes(self) -> dict[str, dict]:
        modes = self.ecfg.get("modes") or {}
        if not modes:
            modes = {"default": {"worker_config": {}}}
        return modes

    def run(self, policy: nn.Module, update: int) -> dict:
        t0 = time.perf_counter()
        tag = f"learner@{update}"
        was_training = policy.training
        policy.eval()
        report: dict = {"update": update, "modes": {}}
        base = dict(self.cfg["worker"]["config"])
        base.pop("curriculum", None)  # evaluation plays ordinary episodes only (a mode may set its own)
        scores = []
        for mode, mcfg in self.modes().items():
            wc = {**base, **dict(mcfg.get("worker_config", {}))}
            report["modes"][mode] = {}
            for opp in self.opponents(update):
                configs = worker_configs(self.cfg, int(self.ecfg["num_workers"]), wc, seed=int(self.ecfg["seed"]))
                pool = WorkerPool(
                    worker_command(self.cfg),
                    resolve_path(self.cfg, self.cfg["worker"]["client_root"]),
                    configs,
                    self.run_dir / "eval_workers",
                    groups=1,
                    step_timeout_s=float(self.cfg["worker"]["step_timeout_s"]),
                    max_restarts=int(self.cfg["worker"]["max_restarts"]),
                    env=self.cfg["worker"].get("env", {}),
                )
                try:
                    pool.start()
                    res = play_matches(
                        pool,
                        policy,
                        self.league.model(opp),
                        int(self.ecfg["episodes"]),
                        self.device,
                        float(self.ecfg["min_p"]),
                        float(self.ecfg["temperature"]),
                        seed=int(self.ecfg["seed"]),
                        bf16=bool(self.cfg["ppo"].get("bf16", True)),
                    )
                finally:
                    pool.close()
                report["modes"][mode][opp] = res
                row = self.table.setdefault((tag, opp), [0.0, 0.0, 0.0])
                row[0] += res["win"]
                row[1] += res["draw"]
                row[2] += res["loss"]
                if opp == BASELINE:
                    scores.append(res["score"])
        report["elo"] = fit_elo(self.table) if self.table else {}
        report["score_vs_baseline"] = float(np.mean(scores)) if scores else None
        report["eval_s"] = time.perf_counter() - t0
        if was_training:
            policy.train()
        self.history.append(report)
        out = self.run_dir / "eval.jsonl"
        with open(out, "a") as f:
            f.write(json.dumps(report) + "\n")
        return report

    def maybe_export_best(self, policy: nn.Module, report: dict, update: int) -> bool:
        score = report.get("score_vs_baseline")
        if score is None or score <= self.best_score:
            return False
        self.best_score = score
        exp = self.run_dir / "export"
        exp.mkdir(parents=True, exist_ok=True)
        torch.save({k: v.detach().cpu() for k, v in getattr(policy, "_orig_mod", policy).state_dict().items()}, exp / "best_policy.pt")
        meta = {"update": update, "score_vs_baseline": score, "report": report}
        (exp / "best_eval.json").write_text(json.dumps(meta, indent=1))
        if self.ecfg.get("export_best", True):
            if export_onnx(getattr(policy, "_orig_mod", policy), exp / "best.onnx"):
                log.info("exported best policy (score %.3f vs baseline) to %s", score, exp / "best.onnx")
        return True


def main(argv=None) -> int:
    from .checkpoint import load_checkpoint
    from .policy_api import load_baseline, make_policy
    from .train import pick_device

    ap = argparse.ArgumentParser(description="Evaluate a checkpoint against U2392 and league snapshots")
    ap.add_argument("--config", required=True)
    ap.add_argument("--checkpoint", required=True)
    ap.add_argument("--set", action="append", default=[], help="override, e.g. eval.episodes=200")
    ap.add_argument("--device", default="")
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")
    over: dict = {}
    from .config import _merge

    for s in args.set:
        over = _merge(over, parse_override(s), strict=False)
    cfg = load_config(args.config, over)
    device = pick_device(args.device or cfg["run"]["device"])
    ck = load_checkpoint(args.checkpoint, map_location="cpu")
    policy = make_policy(cfg).to(device)
    policy.load_state_dict(ck["policy"])
    run_dir = Path(resolve_path(cfg, cfg["run"]["dir"]))
    league = League(cfg, run_dir, lambda: make_policy(cfg), lambda: load_baseline(cfg), device)
    if "league" in ck:
        league.load_state_dict(ck["league"])
    ev = Evaluator(cfg, run_dir / "standalone_eval", league, device)
    report = ev.run(policy, int(ck.get("counters", {}).get("update", 0)))
    print(json.dumps(report, indent=1))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
