"""Collector bookkeeping on a scripted environment: delayed rewards, carries, league roles."""

from __future__ import annotations

import numpy as np
import torch
from trainer_helpers import mock_cfg

from qqtrain.league import League
from qqtrain.policy_api import RandomPolicy, StandInPolicy
from qqtrain.rollout import Collector, compute_gae
from qqtrain.worker import OP_RESET, OP_STEP, Buffers, GroupResult


class ScriptedPool:
    """One group, E envs x P slots; ``script`` is a list of per-response dicts."""

    def __init__(self, E, P, script):
        self.num_envs, self.P, self.num_slots, self.num_groups = E, P, E * P, 1
        self.group_slots = [slice(0, E * P)]
        S = E * P
        self.buffers = Buffers(
            spatial=torch.zeros((S, 117, 13, 15), dtype=torch.float16),
            scalars=torch.zeros((S, 187)),
            legal=torch.zeros((S, 45), dtype=torch.bool),
            active=torch.zeros(S, dtype=torch.bool),
            reset=torch.zeros(S, dtype=torch.bool),
            team=torch.zeros(S, dtype=torch.uint8),
            reward=torch.zeros(S),
            done=torch.zeros(S, dtype=torch.bool),
        )
        for k in ("spatial", "scalars", "legal", "active", "reset", "team", "reward", "done"):
            self.buffers.np[k] = getattr(self.buffers, k).numpy()
        self.script = list(script)
        self.sent: list[np.ndarray] = []
        self._op = None

    def submit(self, g, op, actions=None):
        self._op = op
        if op == OP_STEP:
            self.sent.append(actions.copy())

    def wait(self, g):
        step = self.script.pop(0)
        b = self.buffers.np
        S = self.num_slots
        for k in ("active", "reset", "done"):
            b[k][:] = np.asarray(step.get(k, [0] * S), bool)
        b["team"][:] = step.get("team", [1, 2] * (S // 2))
        b["reward"][:] = step.get("reward", [0.0] * S)
        b["legal"][:] = False
        b["legal"][b["active"], :3] = True
        return GroupResult(0, slice(0, S), step.get("episodes", []), np.zeros(0, np.int64), self._op == OP_RESET)


def _collector(tmp_path, script, self_play=1.0, T=2):
    cfg = mock_cfg(tmp_path, ppo={"rollout_steps": T, "gamma": 0.9}, league={"self_play_prob": self_play, "resident": 1})
    pool = ScriptedPool(1, 2, script)
    dev = torch.device("cpu")
    league = League(cfg, tmp_path / "run", lambda: StandInPolicy(4, 16), RandomPolicy, dev)
    return Collector(cfg, pool, StandInPolicy(4, 16), league, dev), pool


def test_eliminated_agent_carry_and_delayed_terminal(tmp_path):
    script = [
        dict(active=[1, 1], reset=[1, 1]),  # RESET -> row 1
        dict(active=[1, 0], reward=[0.1, -0.3]),  # slot 1 eliminated -> row 2
        dict(active=[1, 0], reward=[0.2, 0.0]),  # -> row 3 (= T+1)
        # rollout 2
        dict(active=[1, 1], reset=[1, 1], done=[1, 1], reward=[-1.0, 1.0], episodes=[{"env": 0, "teams": {"1": "loss", "2": "win"}}]),
        dict(active=[1, 1], reward=[0.0, 0.0]),
    ]
    col, pool = _collector(tmp_path, script)
    st = col.storage
    col.collect()
    # slot 0 acted at rows 1, 2, 3; slot 1 only at row 1 and is still pending.
    assert st.valid[1:4, 0].all() and st.valid[1, 1] and not st.valid[2:4, 1].any()
    assert st.train[1:3, 0].all() and not st.train[3, 0]  # row T+1 bootstraps only
    assert not st.train[1, 1]  # carried, not trained yet
    np.testing.assert_allclose(st.reward[1:3, 0], [0.1, 0.2])
    np.testing.assert_allclose(st.reward[1, 1], -0.3)
    adv, _ = compute_gae(st, 0.9, 0.95)
    assert adv[1, 1] == 0
    assert (pool.sent[0] >= 0).all() and pool.sent[1][1] == -1  # inactive slot sends -1
    col.advance()
    assert st.valid[0, 1] and st.train[0, 1] and st.gstep[0, 1] == 1
    assert st.valid[1, 0] and st.gstep[1, 0] == 3 and not st.valid[2:, :].any()
    col.collect()
    # The terminal arrived for both: slot 1's carried entry (row 0) and slot 0's row 1.
    np.testing.assert_allclose(st.reward[0, 1], -0.3 + 1.0)
    assert st.done[0, 1] and st.done[1, 0]
    np.testing.assert_allclose(st.reward[1, 0], -1.0)
    # New episode: both act again with reset flags set.
    assert st.valid[2, 0] and st.valid[2, 1] and st.reset[2, 0] and st.reset[2, 1]
    adv, ret = compute_gae(st, 0.9, 0.95)
    np.testing.assert_allclose(adv[0, 1], 0.7 - st.value[0, 1], rtol=1e-5)


def test_only_learner_team_is_stored(tmp_path):
    script = [dict(active=[1, 1], reset=[1, 1])] + [dict(active=[1, 1]) for _ in range(3)]
    col, pool = _collector(tmp_path, script, self_play=0.0)
    col.collect()
    st = col.storage
    learner_team = np.nonzero(col.env_learner[0])[0].tolist()
    assert len(learner_team) == 1 and col.env_opp[0] == "baseline"
    slot = learner_team[0] - 1  # teams 1, 2 -> slots 0, 1
    assert st.valid[1:4, slot].all() and not st.valid[:, 1 - slot].any()
    assert all((a >= 0).all() for a in pool.sent)  # the opponent also acted
    assert col.stats["opponent_decisions"] == 3 and col.stats["learner_decisions"] == 3
