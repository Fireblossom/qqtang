"""PPO loss and update sanity."""

from __future__ import annotations

import math

import numpy as np
import torch
from trainer_helpers import mock_cfg

from qqtrain.policy_api import StandInPolicy, masked_entropy, masked_log_probs, sample_actions, sequence_forward
from qqtrain.ppo import PPO, entropy_coef_at, lr_at, ppo_loss
from qqtrain.rollout import RolloutStorage, compute_gae

KW = dict(clip=0.2, value_clip=0.2, value_coef=0.5, entropy_coef=0.0)


def _inputs(B=6, A=45):
    torch.manual_seed(0)
    legal = torch.zeros(B, A, dtype=torch.bool)
    legal[:, :5] = True
    logits = torch.randn(B, A).masked_fill(~legal, float("-inf"))
    action = torch.randint(0, 5, (B,))
    logp = masked_log_probs(logits, legal).gather(-1, action[:, None]).squeeze(-1)
    return logits, legal, action, logp


def test_ratio_one_gives_minus_mean_advantage():
    logits, legal, action, logp = _inputs()
    adv = torch.tensor([1.0, -2.0, 0.5, 0.0, 3.0, -1.0])
    value = torch.zeros(6)
    loss, info = ppo_loss(logits, value, legal, action, logp, value, adv, value, torch.ones(6, dtype=torch.bool), **KW)
    assert math.isclose(float(info["pg_loss"]), -float(adv.mean()), rel_tol=1e-6)
    assert float(info["approx_kl"]) < 1e-9 and float(info["clipfrac"]) == 0
    assert float(info["v_loss"]) == 0


def test_clipping_stops_gradient_beyond_trust_region():
    logits, legal, action, logp = _inputs(1)
    old = logp - 1.0  # ratio = e > 1.2
    logits = logits.clone().requires_grad_(True)
    loss, info = ppo_loss(logits, torch.zeros(1), legal, action, old, torch.zeros(1), torch.ones(1), torch.zeros(1), torch.ones(1, dtype=torch.bool), **KW)
    loss.backward()
    assert float(info["clipfrac"]) == 1.0
    assert torch.nan_to_num(logits.grad).abs().max() == 0  # positive advantage, ratio above 1+clip
    # Negative advantage with the same ratio is not clipped (pessimistic bound).
    logits.grad = None
    loss, _ = ppo_loss(logits, torch.zeros(1), legal, action, old, torch.zeros(1), -torch.ones(1), torch.zeros(1), torch.ones(1, dtype=torch.bool), **KW)
    loss.backward()
    assert torch.nan_to_num(logits.grad).abs().max() > 0


def test_value_clipping_takes_the_pessimistic_error():
    logits, legal, action, logp = _inputs(1)
    m = torch.ones(1, dtype=torch.bool)
    # old 0, new 1.0, target 1.0: unclipped error 0, clipped (0.2) error 0.64 -> max = 0.64
    _, info = ppo_loss(logits, torch.ones(1), legal, action, logp, torch.zeros(1), torch.zeros(1), torch.ones(1), m, **KW)
    assert math.isclose(float(info["v_loss"]), 0.5 * 0.64, rel_tol=1e-5)


def test_entropy_and_mask_padding_are_finite():
    legal = torch.zeros(2, 45, dtype=torch.bool)
    legal[0, :4] = True
    legal[1, 0] = True
    logits = torch.zeros(2, 45, requires_grad=True)
    ent = masked_entropy(masked_log_probs(logits, legal), legal)
    assert torch.allclose(ent, torch.tensor([math.log(4), 0.0]))
    ent.sum().backward()
    assert torch.isfinite(logits.grad).all()


def test_sampler_respects_mask_and_min_p():
    legal = torch.zeros(1000, 45, dtype=torch.bool)
    legal[:, [0, 3, 7]] = True
    logits = torch.tensor([2.0, 0, 0, 0.0, 0, 0, 0, -3.0] + [0.0] * 37).expand(1000, 45)
    a, lp = sample_actions(logits, legal)
    assert set(a.tolist()) <= {0, 3, 7} and torch.isfinite(lp).all()
    a, _ = sample_actions(logits, legal, min_p=0.1)
    # p(7)/p(0) = e^-5 < 0.1 is removed by MinP; p(3)/p(0) = e^-2 = 0.135 is kept.
    assert 7 not in set(a.tolist()) and {0, 3} <= set(a.tolist())


def test_schedules():
    c = {"lr": 1e-3, "lr_final": 1e-4, "lr_warmup_updates": 4, "lr_schedule": "cosine", "entropy_coef": 0.01, "entropy_coef_final": 0.0}
    assert math.isclose(lr_at(c, 0, 0.0), 1e-3 / 4)
    assert math.isclose(lr_at(c, 10, 0.0), 1e-3)
    assert math.isclose(lr_at(c, 10, 1.0), 1e-4)
    assert math.isclose(entropy_coef_at(c, 0.5), 0.005)


def test_sequence_forward_matches_stepwise():
    torch.manual_seed(1)
    p = StandInPolicy(4, 16)
    L, B = 5, 3
    sp = torch.randn(L, B, 117, 13, 15)
    sc = torch.randn(L, B, 187)
    lg = torch.ones(L, B, 45, dtype=torch.bool)
    rs = torch.zeros(L, B, dtype=torch.bool)
    rs[2, 1] = True
    m0 = torch.randn(B, 256)
    lo, v, h = sequence_forward(p, sp, sc, lg, m0, rs)
    h2 = m0
    for t in range(L):
        lt, vt, h2 = p(sp[t], sc[t], lg[t], h2, rs[t])
        torch.testing.assert_close(lt, lo[t], rtol=1e-4, atol=1e-5)
        torch.testing.assert_close(vt, v[t], rtol=1e-4, atol=1e-5)
    torch.testing.assert_close(h, h2, rtol=1e-4, atol=1e-5)


def test_update_moves_policy_towards_advantage(tmp_path):
    """Synthetic rollout: action 1 has positive advantage, action 2 negative."""
    cfg = mock_cfg(tmp_path, ppo={"epochs": 4, "minibatches": 2, "seq_len": 4, "lr": 3e-3, "lr_warmup_updates": 0, "target_kl": 0.0, "entropy_coef": 0.0})
    torch.manual_seed(0)
    pol = StandInPolicy(4, 16)
    T, S = 8, 6
    st = RolloutStorage(T, S, torch.device("cpu"))
    st.legal[:, :, :3] = True
    st.scalars.normal_()
    with torch.no_grad():
        for r in range(1, T + 2):
            lo, v, _ = pol(st.spatial[r], st.scalars[r], st.legal[r], st.memory[r], st.reset[r])
            a, lp = sample_actions(lo, st.legal[r])
            st.action[r] = a.numpy()
            st.logp[r] = lp.numpy()
            st.value[r] = v.numpy()
    st.valid[1:] = True
    st.train[1 : T + 1] = True
    st.gtime[:] = np.arange(T + 2)[:, None]
    st.reward[:] = np.where(st.action == 1, 1.0, np.where(st.action == 2, -1.0, 0.0))
    adv, ret = compute_gae(st, 0.0, 0.0)  # gamma 0: advantage = reward - value
    ppo = PPO(pol, cfg, torch.device("cpu"))

    @torch.no_grad()
    def mean_prob(a):
        lo, _, _ = pol(st.spatial[1], st.scalars[1], st.legal[1], st.memory[1], st.reset[1])
        return float(torch.softmax(lo, -1)[:, a].mean())

    before = (mean_prob(1), mean_prob(2))
    m = ppo.update(st, adv, ret, 0.0)
    after = (mean_prob(1), mean_prob(2))
    assert m["samples"] == T * S and np.isfinite(m["loss"])
    assert after[0] > before[0] and after[1] < before[1]
