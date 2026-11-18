"""GAE with dones, inactive gaps, carried entries; chunking; collector bookkeeping."""

from __future__ import annotations

import numpy as np
import pytest
import torch

from qqtrain.rollout import RolloutStorage, build_chunks, compute_gae

G, LAM = 0.9, 0.8


def _st(T, S):
    return RolloutStorage(T, S, torch.device("cpu"))


def _entry(st, row, slot, value, reward=0.0, done=False, train=True, gtime=None):
    st.valid[row, slot] = True
    st.train[row, slot] = train
    st.value[row, slot] = value
    st.reward[row, slot] = reward
    st.done[row, slot] = done
    st.gtime[row, slot] = row if gtime is None else gtime


def test_plain_sequence_with_bootstrap():
    st = _st(3, 1)
    _entry(st, 1, 0, 0.5, 1.0)
    _entry(st, 2, 0, 0.2, 0.0)
    _entry(st, 3, 0, 0.1, 2.0)
    _entry(st, 4, 0, 0.4, train=False)  # row T+1 bootstrap
    adv, ret = compute_gae(st, G, LAM)
    d3 = 2.0 + G * 0.4 - 0.1
    d2 = 0.0 + G * 0.1 - 0.2
    d1 = 1.0 + G * 0.2 - 0.5
    a3 = d3
    a2 = d2 + G * LAM * a3
    a1 = d1 + G * LAM * a2
    np.testing.assert_allclose(adv[1:4, 0], [a1, a2, a3], rtol=1e-6)
    np.testing.assert_allclose(ret[1:4, 0], [a1 + 0.5, a2 + 0.2, a3 + 0.1], rtol=1e-6)
    assert adv[4, 0] == 0 and ret[4, 0] == 0


def test_done_cuts_bootstrap_and_lambda():
    st = _st(3, 1)
    _entry(st, 1, 0, 0.5, 0.3)
    _entry(st, 2, 0, 0.2, -1.0, done=True)  # terminal reward included
    _entry(st, 3, 0, 0.7, 0.0)  # new episode
    _entry(st, 4, 0, 0.6, train=False)
    adv, _ = compute_gae(st, G, LAM)
    a3 = 0.0 + G * 0.6 - 0.7
    a2 = -1.0 - 0.2
    a1 = 0.3 + G * 0.2 - 0.5 + G * LAM * a2
    np.testing.assert_allclose(adv[1:4, 0], [a1, a2, a3], rtol=1e-6)


def test_inactive_gap_discounts_by_steps():
    """Slot acts at rows 1 and 4 (inactive in 2-3): gamma ** 3 between them."""
    st = _st(4, 1)
    _entry(st, 1, 0, 0.5, 1.0)
    _entry(st, 4, 0, 0.3, 0.0)
    _entry(st, 5, 0, 0.2, train=False)
    adv, _ = compute_gae(st, G, LAM)
    a4 = G * 0.2 - 0.3
    a1 = 1.0 + G**3 * 0.3 - 0.5 + G**3 * LAM * a4
    np.testing.assert_allclose(adv[[1, 4], 0], [a1, a4], rtol=1e-6)


def test_carried_pending_entry_truncates_lambda():
    """An entry still pending at rollout end is not trained; earlier ones bootstrap on its value."""
    st = _st(3, 1)
    _entry(st, 1, 0, 0.5, 0.1)
    _entry(st, 2, 0, 0.9, 0.0, train=False)  # eliminated afterwards, episode still running
    adv, ret = compute_gae(st, G, LAM)
    np.testing.assert_allclose(adv[1, 0], 0.1 + G * 0.9 - 0.5, rtol=1e-6)
    assert adv[2, 0] == 0 and ret[2, 0] == 0


def test_carried_entry_gets_delayed_terminal():
    """Row-0 carry from an earlier rollout (gtime before row 1) closed by a done."""
    st = _st(3, 1)
    _entry(st, 0, 0, 0.4, reward=-1.0, done=True, gtime=-5)
    _entry(st, 2, 0, 0.1, 0.0)
    _entry(st, 4, 0, 0.3, train=False)
    adv, _ = compute_gae(st, G, LAM)
    np.testing.assert_allclose(adv[0, 0], -1.0 - 0.4, rtol=1e-6)


def test_inactive_and_independent_slots():
    st = _st(2, 3)
    _entry(st, 1, 0, 0.0, 1.0, done=True)
    _entry(st, 1, 2, 0.5, 0.0)
    _entry(st, 3, 2, 1.0, train=False)
    adv, ret = compute_gae(st, G, LAM)
    assert (adv[:, 1] == 0).all() and (ret[:, 1] == 0).all()  # never-active slot
    np.testing.assert_allclose(adv[1, 0], 1.0)
    np.testing.assert_allclose(adv[1, 2], G**2 * 1.0 - 0.5, rtol=1e-6)


def test_unequal_step_lengths_discount_by_time():
    """Server cadence: decisions 0.4 and 1.0 decision units (100 ms) apart."""
    st = _st(3, 1)
    _entry(st, 1, 0, 0.5, 1.0, gtime=0.0)
    _entry(st, 2, 0, 0.2, 0.0, gtime=0.4)
    _entry(st, 3, 0, 0.3, train=False, gtime=1.4)
    adv, _ = compute_gae(st, G, LAM)
    a2 = G**1.0 * 0.3 - 0.2
    a1 = 1.0 + G**0.4 * 0.2 - 0.5 + G**0.4 * LAM * a2
    np.testing.assert_allclose(adv[1:3, 0], [a1, a2], rtol=1e-6)


def test_missing_successor_is_an_error():
    st = _st(2, 1)
    _entry(st, 1, 0, 0.5)
    with pytest.raises(AssertionError):
        compute_gae(st, G, LAM)


def test_build_chunks():
    tr = np.zeros((6, 3), bool)
    tr[[0, 1, 2, 4, 5], 0] = True  # 5 entries -> chunks of 2: 2,2,1
    tr[[3], 2] = True
    rows, slots, mask = build_chunks(tr, 2)
    assert rows.shape == (2, 4)
    assert mask.sum() == 6
    assert rows[:, 0].tolist() == [0, 1] and rows[:, 1].tolist() == [2, 4]
    assert rows[0, 2] == 5 and not mask[1, 2]
    assert slots[0, 3] == 2 and rows[0, 3] == 3
    assert (slots[:, :3][mask[:, :3]] == 0).all()
