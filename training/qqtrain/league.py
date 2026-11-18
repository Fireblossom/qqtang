"""Self-play league: which policy controls the non-learner teams.

Pool members
* ``latest``    the current learner itself (pure self-play; with
                ``train_both_sides`` every team of such an episode is trained);
* ``baseline``  the frozen U2392 network (``policy_api.load_baseline``);
* ``snap_XXXXXX`` learner snapshots saved every ``snapshot_every`` updates;
* ``random``    optional uniform-random policy (debugging).

Sampling is prioritized fictitious self-play: ``self_play_prob`` of the
episodes go to ``latest``; the rest are spread over the frozen members with
weight ``(1 - p_win) ** pfsp_power`` where ``p_win`` is the learner's
(decayed, Beta(1,1)-smoothed) score against that member, so opponents the
learner loses to are played more. The baseline keeps a floor share of
``baseline_prob`` of the frozen games.

To keep the number of distinct networks evaluated per environment step small,
``resident`` frozen members are drawn once per rollout and kept on the device;
environments that start an episode draw among ``latest`` + the resident set.
Assignment is per environment and episode, by team: one team (chosen
uniformly) is the learner, all other teams are played by the drawn opponent.
"""

from __future__ import annotations

import logging
import math
import os
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Callable

import numpy as np
import torch
import torch.nn as nn

from .policy_api import RandomPolicy

log = logging.getLogger(__name__)

LATEST = "latest"
BASELINE = "baseline"
RANDOM = "random"


@dataclass
class Member:
    id: str
    kind: str  # baseline | snapshot | random
    path: str = ""
    update: int = 0
    wins: float = 0.0
    draws: float = 0.0
    losses: float = 0.0
    games: int = 0  # undecayed count

    def score(self) -> float:
        """Learner's smoothed score against this member (win=1, draw=0.5)."""
        n = self.wins + self.draws + self.losses
        return (self.wins + 0.5 * self.draws + 1.0) / (n + 2.0)


class League:
    def __init__(
        self,
        cfg: dict,
        run_dir: str | Path,
        policy_factory: Callable[[], nn.Module],
        baseline_loader: Callable[[], nn.Module],
        device: torch.device,
        rng: np.random.Generator | None = None,
    ):
        self.cfg = cfg["league"]
        self.dir = Path(run_dir) / "league"
        self.dir.mkdir(parents=True, exist_ok=True)
        self.policy_factory = policy_factory
        self.baseline_loader = baseline_loader
        self.device = device
        self.rng = rng or np.random.default_rng(0)
        self.decay = 0.995
        self.members: dict[str, Member] = {BASELINE: Member(BASELINE, "baseline")}
        if self.cfg.get("include_random"):
            self.members[RANDOM] = Member(RANDOM, "random")
        self.resident: list[str] = []
        self._models: dict[str, nn.Module] = {}

    # ------------------------------------------------------------------ pool
    def snapshots(self) -> list[Member]:
        return sorted((m for m in self.members.values() if m.kind == "snapshot"), key=lambda m: m.update)

    def add_snapshot(self, policy: nn.Module, update: int) -> str:
        mid = f"snap_{update:06d}"
        path = self.dir / f"{mid}.pt"
        state = {k: v.detach().to("cpu", copy=True) for k, v in _unwrap(policy).state_dict().items()}
        tmp = path.with_suffix(".tmp")
        torch.save(state, tmp)
        os.replace(tmp, path)
        self.members[mid] = Member(mid, "snapshot", str(path), update)
        snaps = self.snapshots()
        while len(snaps) > int(self.cfg["max_snapshots"]):
            old = snaps.pop(0)
            self.members.pop(old.id, None)
            self._models.pop(old.id, None)
            try:
                Path(old.path).unlink()
            except OSError:
                pass
        return mid

    def frozen_probs(self) -> dict[str, float]:
        """Distribution over frozen members (PFSP with a baseline floor)."""
        power = float(self.cfg["pfsp_power"])
        weights = {mid: max(1.0 - m.score(), 1e-3) ** power for mid, m in self.members.items()}
        total = sum(weights.values())
        probs = {mid: w / total for mid, w in weights.items()}
        floor = float(self.cfg["baseline_prob"])
        if BASELINE in probs and probs[BASELINE] < floor and len(probs) > 1:
            rest = 1.0 - probs[BASELINE]
            scale = (1.0 - floor) / rest if rest > 0 else 0.0
            probs = {mid: (floor if mid == BASELINE else p * scale) for mid, p in probs.items()}
        return probs

    def choose_resident(self) -> list[str]:
        """Draw the frozen members used during the next rollout (without replacement)."""
        probs = self.frozen_probs()
        ids = list(probs)
        k = min(int(self.cfg["resident"]), len(ids))
        if k <= 0:
            self.resident = []
        else:
            p = np.array([probs[i] for i in ids], dtype=np.float64)
            p /= p.sum()
            pick = self.rng.choice(len(ids), size=k, replace=False, p=p)
            self.resident = [ids[i] for i in sorted(pick)]
        for mid in list(self._models):
            if mid not in self.resident and mid != BASELINE:
                del self._models[mid]
        return self.resident

    def episode_probs(self) -> tuple[list[str], np.ndarray]:
        """Opponent choices for a starting episode: latest + resident members."""
        sp = float(self.cfg["self_play_prob"]) if self.resident else 1.0
        probs = self.frozen_probs()
        ids = [LATEST] + list(self.resident)
        w = np.array([sp] + [probs[m] for m in self.resident], dtype=np.float64)
        if len(ids) > 1:
            w[1:] *= (1.0 - sp) / max(w[1:].sum(), 1e-12)
        return ids, w / w.sum()

    def sample_opponents(self, n: int) -> list[str]:
        ids, p = self.episode_probs()
        return [ids[i] for i in self.rng.choice(len(ids), size=n, p=p)]

    def model(self, mid: str) -> nn.Module:
        if mid in self._models:
            return self._models[mid]
        m = self.members[mid]
        if m.kind == "baseline":
            net = self.baseline_loader()
        elif m.kind == "random":
            net = RandomPolicy()
        else:
            net = self.policy_factory()
            net.load_state_dict(torch.load(m.path, map_location="cpu", weights_only=True))
        net = net.to(self.device).eval()
        for p in net.parameters():
            p.requires_grad_(False)
        self._models[mid] = net
        return net

    # ------------------------------------------------------------------ results
    def record(self, mid: str, result: str) -> None:
        """Result of one finished episode from the learner team's point of view."""
        m = self.members.get(mid)
        if m is None:
            return
        m.wins *= self.decay
        m.draws *= self.decay
        m.losses *= self.decay
        if result == "win":
            m.wins += 1
        elif result == "loss":
            m.losses += 1
        else:
            m.draws += 1
        m.games += 1

    def summary(self) -> dict:
        probs = self.frozen_probs()
        return {
            mid: {"score": round(m.score(), 4), "games": m.games, "p": round(probs.get(mid, 0.0), 4)}
            for mid, m in self.members.items()
        }

    # ------------------------------------------------------------------ assignment
    def assign_teams(self, teams: list[int], opponent: str) -> set[int]:
        """Learner teams for an episode with these team IDs against ``opponent``."""
        if not teams:
            return set()
        if opponent == LATEST and self.cfg.get("train_both_sides", True):
            return set(teams)
        return {teams[int(self.rng.integers(len(teams)))]}

    # ------------------------------------------------------------------ state
    def state_dict(self) -> dict:
        return {
            "members": [asdict(m) for m in self.members.values()],
            "rng": self.rng.bit_generator.state,
        }

    def load_state_dict(self, state: dict) -> None:
        self.members = {}
        for d in state["members"]:
            m = Member(**d)
            if m.kind == "snapshot" and not Path(m.path).exists():
                alt = self.dir / Path(m.path).name  # run dir moved
                if not alt.exists():
                    log.warning("snapshot %s missing; dropped from the league", m.path)
                    continue
                m.path = str(alt)
            self.members[m.id] = m
        if BASELINE not in self.members:
            self.members[BASELINE] = Member(BASELINE, "baseline")
        self.rng.bit_generator.state = state["rng"]
        self._models = {k: v for k, v in self._models.items() if k == BASELINE}
        self.resident = []


def _unwrap(policy: nn.Module) -> nn.Module:
    return getattr(policy, "_orig_mod", policy)


def outcome_for_teams(entry: dict, learner_teams: set[int]) -> str | None:
    """'win' | 'loss' | 'draw' for the learner side of a finished episode entry.

    Accepts the mock worker's format and a few likely variants of the real
    worker's info JSON: ``teams: {"1": "win", ...}``, ``outcome: {"draw",
    "winner_team"|"winner_team_id"|"winner"}``, or top-level ``winner_team`` /
    ``draw``. Returns None when the entry carries no recognisable result.
    """
    if not learner_teams:
        return None
    teams = entry.get("teams") or entry.get("team_results")
    if isinstance(teams, dict):
        res = {str(teams.get(str(t), teams.get(t, ""))).lower() for t in learner_teams}
        res.discard("")
        if res:
            return "win" if "win" in res else ("draw" if "draw" in res else "loss")
    if isinstance(teams, list):
        res = {str(d.get("result", "")).lower() for d in teams if isinstance(d, dict) and d.get("team") in learner_teams}
        res.discard("")
        if res:
            return "win" if "win" in res else ("draw" if "draw" in res else "loss")
    out = entry.get("outcome")
    src = out if isinstance(out, dict) else entry
    draw = src.get("draw")
    winner = next((src[k] for k in ("winner_team", "winner_team_id", "winner", "WinnerTeamID") if k in src), None)
    if draw is True or (draw is None and isinstance(out, str) and out.lower() == "draw"):
        return "draw"
    if winner is not None:
        if int(winner) == 0 and draw is None:
            return "draw"
        return "win" if int(winner) in learner_teams else "loss"
    return None


def wilson_interval(successes: float, n: int, z: float = 1.96) -> tuple[float, float]:
    if n <= 0:
        return (0.0, 1.0)
    p = successes / n
    denom = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / denom
    half = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / denom
    return (max(0.0, centre - half), min(1.0, centre + half))
