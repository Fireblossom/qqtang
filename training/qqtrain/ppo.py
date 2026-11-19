"""Recurrent PPO update with truncated BPTT over per-slot sequence chunks.

* Chunks (``rollout.build_chunks``) hold up to ``seq_len`` consecutive
  decisions of one agent slot; replay starts from the GRU memory recorded at
  the chunk's first decision (stored-state strategy) and applies the stored
  ``reset`` flags, through ``policy_api.sequence_forward``.
* Clipped surrogate, clipped value loss (max of clipped/unclipped squared
  error), entropy bonus over the legal actions, approximate-KL early stop,
  advantage normalization over the whole rollout, global gradient clipping,
  warm-up + cosine/linear LR schedule and a linearly decayed entropy
  coefficient, both driven by training progress (agent steps / total).
* bf16 autocast on CUDA (losses in fp32), TF32 matmuls, optional torch.compile.
"""

from __future__ import annotations

import math
import time
from contextlib import nullcontext

import numpy as np
import torch
import torch.nn as nn

from .policy_api import bucket_size, masked_entropy, masked_log_probs, pad_dim, sequence_forward
from .rollout import RolloutStorage, build_chunks


def lr_at(cfg: dict, update: int, progress: float) -> float:
    lr0, lr1 = float(cfg["lr"]), float(cfg["lr_final"])
    warm = int(cfg.get("lr_warmup_updates", 0))
    progress = min(max(progress, 0.0), 1.0)
    sched = cfg.get("lr_schedule", "cosine")
    if sched == "constant":
        lr = lr0
    elif sched == "linear":
        lr = lr0 + (lr1 - lr0) * progress
    else:
        lr = lr1 + 0.5 * (lr0 - lr1) * (1 + math.cos(math.pi * progress))
    if warm > 0 and update < warm:
        lr *= (update + 1) / warm
    return lr


def entropy_coef_at(cfg: dict, progress: float) -> float:
    e0 = float(cfg["entropy_coef"])
    e1 = float(cfg.get("entropy_coef_final", e0))
    return e0 + (e1 - e0) * min(max(progress, 0.0), 1.0)


def ppo_loss(
    logits: torch.Tensor,
    value: torch.Tensor,
    legal: torch.Tensor,
    action: torch.Tensor,
    old_logp: torch.Tensor,
    old_value: torch.Tensor,
    adv: torch.Tensor,
    ret: torch.Tensor,
    mask: torch.Tensor,
    clip: float,
    value_clip: float,
    value_coef: float,
    entropy_coef: float,
    pg_scale: float = 1.0,
) -> tuple[torch.Tensor, dict]:
    """All inputs share the leading shape (e.g. [L, B]); ``mask`` selects real entries."""
    m = mask.float()
    n = m.sum().clamp_min(1.0)
    logp_all = masked_log_probs(logits, legal)
    logp = logp_all.gather(-1, action.unsqueeze(-1)).squeeze(-1)
    log_ratio = logp - old_logp
    ratio = log_ratio.exp()
    pg1 = -adv * ratio
    pg2 = -adv * ratio.clamp(1 - clip, 1 + clip)
    pg_loss = (torch.maximum(pg1, pg2) * m).sum() / n
    value = value.float()
    if value_clip > 0:
        v_clipped = old_value + (value - old_value).clamp(-value_clip, value_clip)
        v_loss = 0.5 * (torch.maximum((value - ret) ** 2, (v_clipped - ret) ** 2) * m).sum() / n
    else:
        v_loss = 0.5 * (((value - ret) ** 2) * m).sum() / n
    ent = (masked_entropy(logp_all, legal) * m).sum() / n
    loss = pg_scale * pg_loss + value_coef * v_loss - entropy_coef * ent
    with torch.no_grad():
        approx_kl = (((ratio - 1) - log_ratio) * m).sum() / n
        clipfrac = (((ratio - 1).abs() > clip).float() * m).sum() / n
    return loss, {
        "pg_loss": pg_loss.detach(),
        "v_loss": v_loss.detach(),
        "entropy": ent.detach(),
        "approx_kl": approx_kl,
        "clipfrac": clipfrac,
    }


class PPO:
    def __init__(self, policy: nn.Module, cfg: dict, device: torch.device):
        self.cfg = cfg["ppo"]
        self.policy = policy
        self.device = device
        c = self.cfg
        self.opt = torch.optim.AdamW(
            policy.parameters(), lr=float(c["lr"]), eps=float(c["adam_eps"]), weight_decay=float(c["weight_decay"])
        )
        # During the value warm-up only these learn: the value head sits on
        # the shared temporal feature, and value gradients through the trunk
        # would move the (deployed) policy with no KL check.
        self.trunk_params = [p for name, p in policy.named_parameters() if "value_head" not in name]
        self.autocast = device.type == "cuda" and bool(c.get("bf16", True))
        if device.type == "cuda" and c.get("tf32", True):
            torch.backends.cuda.matmul.allow_tf32 = True
            torch.backends.cudnn.allow_tf32 = True
        self.seq_fn = sequence_forward
        if cfg["model"].get("compile"):
            self.seq_fn = torch.compile(sequence_forward, dynamic=True)
        self.updates = 0
        self.rng = np.random.default_rng(int(cfg["run"]["seed"]) + 17)

    def state_dict(self) -> dict:
        return {"opt": self.opt.state_dict(), "updates": self.updates, "rng": self.rng.bit_generator.state}

    def load_state_dict(self, state: dict) -> None:
        self.opt.load_state_dict(state["opt"])
        self.updates = int(state["updates"])
        self.rng.bit_generator.state = state["rng"]

    def update(self, st: RolloutStorage, adv: np.ndarray, ret: np.ndarray, progress: float) -> dict:
        c = self.cfg
        t0 = time.perf_counter()
        trainable = st.valid & st.train
        n_samples = int(trainable.sum())
        if n_samples == 0:
            return {"samples": 0}
        # The learning-rate warm-up restarts when the policy loss switches on.
        value_warmup = int(c.get("value_warmup_updates", 0))
        lr = lr_at(c, self.updates - value_warmup if self.updates >= value_warmup else self.updates, progress)
        for g in self.opt.param_groups:
            g["lr"] = lr
        ent_coef = entropy_coef_at(c, progress)
        # Value warm-up (e.g. after initialising from U2392, whose value head is new):
        # only the value head learns until its estimates are usable.
        warmup = self.updates < int(c.get("value_warmup_updates", 0))
        pg_scale = 0.0 if warmup else 1.0
        if warmup:
            ent_coef = 0.0
        if c.get("norm_adv", True):
            a = adv[trainable]
            adv = np.where(trainable, (adv - a.mean()) / (a.std() + 1e-8), 0.0).astype(np.float32)
        rows, slots, mask = build_chunks(trainable, int(c["seq_len"]))
        L, N = rows.shape
        sdev = st.device
        dev = self.device
        rows_t = torch.as_tensor(rows, device=sdev)
        slots_t = torch.as_tensor(slots, device=sdev)
        # Small per-entry arrays gathered once on the host, moved to the device.
        def gather_np(arr):
            return torch.as_tensor(arr[rows, slots], device=dev)

        mask_t = torch.as_tensor(mask, device=dev)
        # Padding entries point at (0, 0); neutralise them (action 0 is made legal there).
        action_t = torch.as_tensor(np.where(mask, st.action[rows, slots], 0), device=dev)
        old_logp_t = torch.as_tensor(np.where(mask, st.logp[rows, slots], 0.0).astype(np.float32), device=dev)
        old_value_t = gather_np(st.value)
        adv_t = gather_np(adv)
        ret_t = gather_np(ret)
        nmb = max(1, min(int(c["minibatches"]), N))
        epochs = int(c["epochs"])
        target_kl = float(c.get("target_kl", 0) or 0)
        sums: dict[str, float] = {}
        count = 0
        stopped_early = False
        ac = torch.autocast("cuda", dtype=torch.bfloat16) if self.autocast else nullcontext()
        self.policy.train()
        for epoch in range(epochs):
            perm = self.rng.permutation(N)
            for mb in np.array_split(perm, nmb):
                if len(mb) == 0:
                    continue
                mbt = torch.as_tensor(mb, device=sdev)
                r, s = rows_t[:, mbt], slots_t[:, mbt]
                spatial = st.spatial[r, s].to(dev, non_blocking=True)
                scalars = st.scalars[r, s].to(dev, non_blocking=True)
                legal = st.legal[r, s].to(dev, non_blocking=True)
                reset = st.reset[r, s].to(dev, non_blocking=True)
                mem0 = st.memory[r[0], s[0]].to(dev, non_blocking=True)
                mbd = torch.as_tensor(mb, device=dev)
                m = mask_t[:, mbd]
                action, old_logp, old_value = action_t[:, mbd], old_logp_t[:, mbd], old_value_t[:, mbd]
                adv_mb, ret_mb = adv_t[:, mbd], ret_t[:, mbd]
                if dev.type != "cpu":  # bucketed chunk counts: no per-shape recompilation
                    B = bucket_size(len(mb), minimum=8)
                    spatial, scalars, legal = pad_dim(spatial, B, 1), pad_dim(scalars, B, 1), pad_dim(legal, B, 1, False)
                    reset, mem0, m = pad_dim(reset, B, 1, True), pad_dim(mem0, B, 0), pad_dim(m, B, 1, False)
                    action, old_logp, old_value = pad_dim(action, B, 1), pad_dim(old_logp, B, 1), pad_dim(old_value, B, 1)
                    adv_mb, ret_mb = pad_dim(adv_mb, B, 1), pad_dim(ret_mb, B, 1)
                legal = legal.clone()
                legal[..., 0] |= ~m  # padding: keep the softmax finite
                with ac:
                    logits, value, _ = self.seq_fn(self.policy, spatial, scalars, legal, mem0, reset)
                loss, info = ppo_loss(
                    logits.float(),
                    value.float(),
                    legal,
                    action,
                    old_logp,
                    old_value,
                    adv_mb,
                    ret_mb,
                    m,
                    float(c["clip"]),
                    float(c["value_clip"]),
                    float(c["value_coef"]),
                    ent_coef,
                    pg_scale,
                )
                self.opt.zero_grad(set_to_none=True)
                loss.backward()
                if warmup:
                    for p in self.trunk_params:
                        p.grad = None  # AdamW skips them: no step, no weight decay
                gn = nn.utils.clip_grad_norm_(self.policy.parameters(), float(c["max_grad_norm"]))
                self.opt.step()
                info["loss"] = loss.detach()
                info["grad_norm"] = gn.detach() if torch.is_tensor(gn) else torch.tensor(gn)
                for k, v in info.items():
                    sums[k] = sums.get(k, 0.0) + float(v)
                count += 1
                if target_kl > 0 and not warmup and float(info["approx_kl"]) > target_kl:
                    stopped_early = True
                    break
            if stopped_early:
                break
        self.updates += 1
        out = {k: v / max(count, 1) for k, v in sums.items()}
        # Explained variance of the value targets before the update.
        v_old = st.value[trainable]
        rr = ret[trainable]
        var = float(np.var(rr))
        out.update(
            {
                "samples": n_samples,
                "chunks": N,
                "minibatch_steps": count,
                "epochs_done": epoch + 1,
                "kl_stop": float(stopped_early),
                "value_warmup": float(warmup),
                "lr": lr,
                "entropy_coef": ent_coef,
                "explained_var": float(1 - np.var(rr - v_old) / var) if var > 1e-8 else 0.0,
                "update_s": time.perf_counter() - t0,
            }
        )
        return out
