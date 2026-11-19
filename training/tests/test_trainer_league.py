"""League sampling, outcome parsing, evaluation statistics and Elo."""

from __future__ import annotations

from collections import Counter

import numpy as np
import pytest
import torch
from trainer_helpers import MOCK_CMD, OraclePolicy, mock_cfg

from qqtrain.evaluate import fit_elo, play_matches
from qqtrain.league import BASELINE, LATEST, League, outcome_for_teams, wilson_interval
from qqtrain.policy_api import RandomPolicy, StandInPolicy
from qqtrain.worker import WorkerPool


def _league(tmp_path, **lc):
    cfg = mock_cfg(tmp_path, league={"max_snapshots": 3, "resident": 3, **lc})
    return League(cfg, tmp_path / "run", lambda: StandInPolicy(4, 16), RandomPolicy, torch.device("cpu"), np.random.default_rng(0))


def test_pfsp_prefers_opponents_the_learner_loses_to(tmp_path):
    lg = _league(tmp_path, baseline_prob=0.0)
    pol = StandInPolicy(4, 16)
    a = lg.add_snapshot(pol, 10)
    b = lg.add_snapshot(pol, 20)
    for _ in range(50):
        lg.record(a, "win")  # learner beats a
        lg.record(b, "loss")  # learner loses to b
    p = lg.frozen_probs()
    assert p[b] > p[BASELINE] > p[a]
    assert abs(sum(p.values()) - 1) < 1e-9


def test_baseline_floor_and_snapshot_eviction(tmp_path):
    lg = _league(tmp_path, baseline_prob=0.3)
    pol = StandInPolicy(4, 16)
    for u in range(1, 6):
        lg.add_snapshot(pol, u * 10)
    for _ in range(200):
        lg.record(BASELINE, "win")
    ids = [m.id for m in lg.snapshots()]
    assert ids == ["snap_000030", "snap_000040", "snap_000050"]
    assert not (tmp_path / "run" / "league" / "snap_000010.pt").exists()
    assert lg.frozen_probs()[BASELINE] == pytest.approx(0.3)


def test_episode_sampling_and_team_assignment(tmp_path):
    lg = _league(tmp_path, self_play_prob=0.5)
    pol = StandInPolicy(4, 16)
    lg.add_snapshot(pol, 10)
    c = Counter()
    for _ in range(400):
        lg.choose_resident()
        assert set(lg.resident) <= {BASELINE, "snap_000010"}
        c.update(lg.sample_opponents(20))
    assert 0.45 < c[LATEST] / 8000 < 0.55 and c[BASELINE] > 0 and c["snap_000010"] > 0
    assert lg.assign_teams([1, 2], LATEST) == {1, 2}
    picks = Counter(frozenset(lg.assign_teams([1, 2], BASELINE)) for _ in range(400))
    assert set(picks) == {frozenset({1}), frozenset({2})}
    # A loaded snapshot reproduces the learner's outputs.
    net = lg.model("snap_000010")
    x = (torch.zeros(2, 117, 13, 15), torch.randn(2, 187), torch.ones(2, 45, dtype=torch.bool), torch.zeros(2, 256), torch.ones(2, dtype=torch.bool))
    torch.testing.assert_close(net(*x)[0], pol(*x)[0])


def test_league_state_round_trip(tmp_path):
    lg = _league(tmp_path)
    lg.add_snapshot(StandInPolicy(4, 16), 10)
    lg.record("snap_000010", "loss")
    st = lg.state_dict()
    lg2 = _league(tmp_path)
    lg2.load_state_dict(st)
    assert lg2.summary() == lg.summary()
    assert lg2.sample_opponents(5) == lg.sample_opponents(5)


@pytest.mark.parametrize(
    "entry,teams,expect",
    [
        ({"teams": {"1": "win", "2": "loss"}}, {1}, "win"),
        ({"teams": {"1": "win", "2": "loss"}}, {2}, "loss"),
        ({"teams": [{"team": 2, "result": "draw"}]}, {2}, "draw"),
        ({"outcome": {"draw": False, "winner_team": 2}}, {1}, "loss"),
        ({"outcome": {"draw": True, "winner_team": 0}}, {1}, "draw"),
        ({"winner_team_id": 1}, {1}, "win"),
        ({"length": 4}, {1}, None),
    ],
)
def test_outcome_parsing(entry, teams, expect):
    assert outcome_for_teams(entry, teams) == expect


def test_episode_teams_includes_delayed_players():
    from qqtrain.league import episode_teams

    active = np.array([1, 0, 0, 0], bool)  # random phase: team 2 not due yet
    reset = np.array([1, 1, 0, 0], bool)
    team = np.array([1, 2, 0, 0], np.uint8)
    assert episode_teams(active, reset, team) == [1, 2]
    assert episode_teams(np.zeros(4, bool), np.zeros(4, bool), team) == [1, 2]


def test_wilson_and_elo():
    lo, hi = wilson_interval(50, 100)
    assert lo < 0.5 < hi and hi - lo < 0.21
    r = fit_elo({("a", "baseline"): [75, 0, 25], ("b", "baseline"): [25, 0, 75]})
    assert r["baseline"] == pytest.approx(1000)
    # 75 % expected score ~ +191 Elo (shrunk slightly by the one-draw prior).
    assert 150 < r["a"] - 1000 < 200 and 150 < 1000 - r["b"] < 200


def test_play_matches_oracle_beats_random(tmp_path):
    cfgs = [{"envs": 4, "agents": 4, "seed": 3 + i, "player_counts": [2], "elim_prob": 0.0, "min_len": 10, "max_len": 12} for i in range(2)]
    with WorkerPool(MOCK_CMD, "", cfgs, tmp_path / "ev", groups=1) as pool:
        res = play_matches(pool, OraclePolicy(), RandomPolicy(), 16, torch.device("cpu"), seed=1)
    assert res["n"] == 16 and res["win"] >= 14
    assert res["score_ci"][0] > 0.75
    with WorkerPool(MOCK_CMD, "", cfgs, tmp_path / "ev2", groups=1) as pool:
        res2 = play_matches(pool, RandomPolicy(), OraclePolicy(), 16, torch.device("cpu"), seed=1)
    assert res2["loss"] >= 14


def test_frozen_opponents_are_played_with_their_pfsp_share(tmp_path):
    lg = _league(tmp_path, self_play_prob=0.0, resident=2, baseline_prob=0.3)
    pol = StandInPolicy(4, 16)
    for u in (10, 20, 30):
        lg.add_snapshot(pol, u)
    for _ in range(30):
        lg.record("snap_000010", "loss")
        lg.record("snap_000030", "win")
    probs = lg.frozen_probs()
    c, n = Counter(), 0
    for _ in range(3000):
        lg.choose_resident()
        c.update(lg.sample_opponents(10))
        n += 10
    for mid, p in probs.items():
        assert abs(c[mid] / n - p) < 0.02, (mid, c[mid] / n, p)


def test_an_evicted_snapshot_stays_playable_while_in_use(tmp_path):
    lg = _league(tmp_path)
    pol = StandInPolicy(4, 16)
    playing = {"snap_000010"}
    lg.in_use = lambda: set(playing)
    for u in range(1, 5):
        lg.add_snapshot(pol, u * 10)
    assert "snap_000010" not in lg.members
    lg.model("snap_000010")  # an episode in flight still plays it
    lg.record("snap_000010", "win")
    path = tmp_path / "run" / "league" / "snap_000010.pt"
    lg.choose_resident()
    assert path.exists()
    playing.clear()
    lg.choose_resident()
    assert not path.exists()


def test_an_evicted_resident_snapshot_is_not_drawn_again(tmp_path):
    lg = _league(tmp_path, self_play_prob=0.0, resident=8)
    pol = StandInPolicy(4, 16)
    for u in range(1, 4):
        lg.add_snapshot(pol, u * 10)
    for _ in range(50):
        lg.record("snap_000010", "loss")  # make it likely to be drawn
    lg.choose_resident()
    assert "snap_000010" in lg.resident
    lg.add_snapshot(pol, 40)  # evicts snap_000010 before any episode plays it
    assert "snap_000010" not in lg.sample_opponents(500)
