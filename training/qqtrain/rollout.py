"""Rollout collection for recurrent PPO with a self-play league.

Layout
------
Every worker slot (env x agent) is a column ``s`` of ``[R, S]`` storage with
``R = T + 2`` rows. Row ``t`` (1..T) holds the decision made at rollout step
``t``; row ``T+1`` holds the first decision of the next rollout, which also
provides the bootstrap value; row 0 holds at most one *carried* entry per slot
(see below). A slot has an entry in a row only if it was active and controlled
by the learner at that step (``valid``); inactive slots (eliminated agents,
unused tensor capacity) and opponent slots have none.

Transitions are per slot, from one decision to the slot's next decision.
Rewards that arrive while the slot is inactive (e.g. an eliminated agent's
team winning later, or between decisions with the server cadence) are added
to its pending entry. Each env keeps a clock in 100 ms units (5 engine ticks
per STEP with the fixed cadence, the worker's per-env ``ticks`` with the
server cadence) and the discount between two decisions of a slot is
``gamma ** (clock difference)``, so unequal step lengths are handled. ``done`` closes the pending entry
(terminal; no bootstrap). The GRU memory is only advanced on the slot's own
decisions, exactly like the server, and is cleared on ``reset``/``done``.

An entry still pending at the end of a rollout (slot inactive at step T+1
and its episode not over) is not trained yet: it is carried into row 0 of the
next rollout, where its delayed rewards/terminal arrive; earlier entries use
its value as bootstrap. A worker crash truncates pending entries (bootstrap
with their own value).

Pipelining
----------
Workers are split into G groups. For each step the collector waits for group
g's observations, runs the learner and opponents on them, stores the learner
entries, and immediately sends the actions back, so the other groups'
workers keep simulating while the GPU works. The step for the first decision
of the next rollout is sent before the PPO update starts, so workers also
simulate during the update (one step, recorded with the behaviour policy's
log-probabilities, so the importance ratio stays correct).
"""

from __future__ import annotations

import logging
import time
from collections import defaultdict, deque
from contextlib import nullcontext

import numpy as np
import torch
import torch.nn as nn

from .league import LATEST, League, episode_teams, outcome_for_teams
from .policy_api import ACTIONS, CHANNELS, HEIGHT, MEMORY, SCALARS, WIDTH, deployed_sample, padded_forward, sample_actions
from .worker import OP_RESET, OP_STEP, GroupResult, WorkerPool

log = logging.getLogger(__name__)


class RolloutStorage:
    def __init__(self, T: int, S: int, device: torch.device, spatial_dtype=torch.float16):
        self.T, self.S, self.R = T, S, T + 2
        R = self.R
        self.device = device
        self.spatial = torch.zeros((R, S, CHANNELS, HEIGHT, WIDTH), dtype=spatial_dtype, device=device)
        self.scalars = torch.zeros((R, S, SCALARS), dtype=torch.float32, device=device)
        self.legal = torch.zeros((R, S, ACTIONS), dtype=torch.bool, device=device)
        self.memory = torch.zeros((R, S, MEMORY), dtype=torch.float32, device=device)
        self.reset = torch.zeros((R, S), dtype=torch.bool, device=device)
        # Small per-entry bookkeeping lives in host memory (numpy).
        self.action = np.zeros((R, S), np.int64)
        self.logp = np.zeros((R, S), np.float32)
        self.value = np.zeros((R, S), np.float32)
        self.reward = np.zeros((R, S), np.float32)
        self.done = np.zeros((R, S), bool)
        self.valid = np.zeros((R, S), bool)
        self.train = np.zeros((R, S), bool)
        self.gtime = np.zeros((R, S), np.float64)  # decision time in 100 ms units (env clock)

    def nbytes(self) -> int:
        return sum(t.numel() * t.element_size() for t in (self.spatial, self.scalars, self.legal, self.memory, self.reset))

    def clear_rows(self, rows: slice | np.ndarray) -> None:
        self.valid[rows] = False
        self.train[rows] = False
        self.reward[rows] = 0
        self.done[rows] = False

    def copy_entries(self, src_rows: np.ndarray, dst_row: int, slots: np.ndarray) -> None:
        """Move entries (src_rows[i], slots[i]) to (dst_row, slots[i])."""
        if len(slots) == 0:
            return
        sr = torch.as_tensor(src_rows, device=self.device)
        sl = torch.as_tensor(slots, device=self.device)
        for t in (self.spatial, self.scalars, self.legal, self.memory, self.reset):
            t[dst_row, sl] = t[sr, sl]
        for a in (self.action, self.logp, self.value, self.reward, self.done, self.valid, self.train, self.gtime):
            a[dst_row, slots] = a[src_rows, slots]


def compute_gae(st: RolloutStorage, gamma: float, lam: float) -> tuple[np.ndarray, np.ndarray]:
    """Per-slot GAE over the valid entries of [R, S] storage.

    Entries with ``train`` are learning targets; valid non-train entries
    (row T+1 and carried pending entries) only provide bootstrap values.
    Returns (advantages, returns), zero where not trainable.
    """
    R, S = st.valid.shape
    adv = np.zeros((R, S), np.float32)
    next_val = np.zeros(S, np.float64)
    next_adv = np.zeros(S, np.float64)
    next_g = np.zeros(S, np.float64)
    has_next = np.zeros(S, bool)
    for r in range(R - 1, -1, -1):
        v = st.valid[r]
        if not v.any():
            continue
        tr = v & st.train[r]
        boot = v & ~st.train[r]
        if tr.any():
            idx = np.nonzero(tr)[0]
            term = st.done[r, idx]
            missing = ~term & ~has_next[idx]
            if missing.any():
                raise AssertionError(f"trainable entries without successor at row {r}: slots {idx[missing][:8]}")
            gap = next_g[idx] - st.gtime[r, idx]
            disc = np.where(term, 0.0, gamma ** gap)
            delta = st.reward[r, idx] + disc * next_val[idx] - st.value[r, idx]
            a = delta + disc * lam * next_adv[idx]
            adv[r, idx] = a
            next_val[idx] = st.value[r, idx]
            next_adv[idx] = a
            next_g[idx] = st.gtime[r, idx]
            has_next[idx] = True
        if boot.any():
            idx = np.nonzero(boot)[0]
            next_val[idx] = st.value[r, idx]
            next_adv[idx] = 0.0
            next_g[idx] = st.gtime[r, idx]
            has_next[idx] = True
    trainable = st.valid & st.train
    ret = np.where(trainable, adv + st.value, 0.0).astype(np.float32)
    adv = np.where(trainable, adv, 0.0).astype(np.float32)
    return adv, ret


def build_chunks(trainable: np.ndarray, seq_len: int) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Cut each slot's trainable entries (in time order) into chunks of ``seq_len``.

    Returns ``rows [L, N]``, ``slots [L, N]`` (padding points at (0, 0)) and
    ``mask [L, N]``. Within a chunk, consecutive entries are consecutive
    decisions of the same slot, so the GRU can be replayed through them.
    """
    s_idx, r_idx = np.nonzero(trainable.T)  # slot-major, rows ascending
    L = seq_len
    if len(s_idx) == 0:
        z = np.zeros((L, 0), np.int64)
        return z, z.copy(), np.zeros((L, 0), bool)
    S = trainable.shape[1]
    counts = np.bincount(s_idx, minlength=S)
    starts = np.cumsum(counts) - counts
    pos = np.arange(len(s_idx)) - starts[s_idx]
    nchunks = (counts + L - 1) // L
    choff = np.cumsum(nchunks) - nchunks
    chunk = choff[s_idx] + pos // L
    t = pos % L
    N = int(nchunks.sum())
    rows = np.zeros((L, N), np.int64)
    slots = np.zeros((L, N), np.int64)
    mask = np.zeros((L, N), bool)
    rows[t, chunk] = r_idx
    slots[t, chunk] = s_idx
    mask[t, chunk] = True
    return rows, slots, mask


class Collector:
    """Drives the worker pool, the league and the rollout storage."""

    def __init__(
        self,
        cfg: dict,
        pool: WorkerPool,
        policy: nn.Module,
        league: League,
        device: torch.device,
        storage_device: torch.device | None = None,
    ):
        self.cfg = cfg
        self.ppo = cfg["ppo"]
        self.lcfg = cfg["league"]
        self.pool = pool
        self.policy = policy
        self.league = league
        self.device = device
        self.T = int(self.ppo["rollout_steps"])
        self.gamma = float(self.ppo["gamma"])
        S, E, P = pool.num_slots, pool.num_envs, pool.P
        self.S, self.E, self.P = S, E, P
        self.storage = RolloutStorage(self.T, S, storage_device or device)
        self.memory = torch.zeros((S, MEMORY), dtype=torch.float32, device=device)
        self.need_reset = np.ones(S, bool)
        self.last_row = np.full(S, -1, np.int64)
        self.slot_env = np.repeat(np.arange(E), P)
        self.env_opp = [LATEST] * E
        self.env_learner = np.zeros((E, 256), bool)  # learner team mask per env
        self.ep_return = np.zeros(S, np.float64)
        self.env_clock = np.zeros(E, np.float64)  # per env, in 100 ms decision units
        self._staging: dict[tuple[int, str], torch.Tensor] = {}
        self.pad = device.type != "cpu"  # bucketed batch sizes on accelerators
        self.rollouts = 0
        self.autocast = device.type == "cuda" and bool(self.ppo.get("bf16", True))
        self.reset_stats()
        self.recent_returns: deque = deque(maxlen=1000)
        self.recent_results: dict[str, deque] = defaultdict(lambda: deque(maxlen=500))
        self.started = False

    # ------------------------------------------------------------------ stats
    def reset_stats(self) -> None:
        self.stats = defaultdict(float)
        self.ep_infos: list[dict] = []

    # ------------------------------------------------------------------ helpers
    def _is_learner(self, sl: slice) -> np.ndarray:
        team = self.pool.buffers.np["team"][sl]
        return self.env_learner[self.slot_env[sl], team]

    def _close_pending(self, slots: np.ndarray, truncate: bool) -> None:
        slots = slots[self.last_row[slots] >= 0]
        if len(slots) == 0:
            return
        rows = self.last_row[slots]
        st = self.storage
        if truncate:  # bootstrap with the entry's own value
            st.reward[rows, slots] += self.gamma * st.value[rows, slots]
        st.done[rows, slots] = True
        self.last_row[slots] = -1

    # ------------------------------------------------------------------ response
    def _process(self, res: GroupResult) -> None:
        sl = res.slots
        b = self.pool.buffers.np
        gs = np.arange(sl.start, sl.stop)
        reward = b["reward"][sl]
        done = b["done"][sl]
        # Advance each env's clock: 5 ticks per STEP with the fixed cadence, the
        # worker's per-env "ticks" with the server cadence (discount is per 100 ms).
        envs_sl = np.arange(sl.start // self.P, sl.stop // self.P)
        if res.ticks is not None:
            self.env_clock[envs_sl] += res.ticks / 5.0
        elif not res.reset_op:
            self.env_clock[envs_sl] += 1.0
        # 1. Rewards go to each slot's pending learner entry.
        pend = self.last_row[gs] >= 0
        if pend.any():
            ps = gs[pend]
            self.storage.reward[self.last_row[ps], ps] += reward[pend]
            self.stats["learner_reward"] += float(reward[pend].sum())
        learner_now = self._is_learner(sl)  # assignment of the episode that produced these rewards
        self.ep_return[gs] += np.where(learner_now, reward, 0.0)
        # 2. Episode results, judged with the assignment the episode was played under.
        for ep in res.episodes:
            e = int(ep["env"])
            teams = set(np.nonzero(self.env_learner[e])[0].tolist())
            opp = self.env_opp[e]
            result = outcome_for_teams(ep, teams)
            if result is not None and not (opp == LATEST and self.lcfg.get("train_both_sides", True)):
                self.league.record(opp, result)
                self.recent_results[opp].append(result)
            info = {"env": e, "opponent": opp, "result": result, "length": ep.get("length")}
            self.ep_infos.append(info)
        # 3. Terminals close pending entries; memory resets.
        d = gs[done]
        if len(d):
            self._close_pending(d, truncate=False)
            fin = d[learner_now[d - sl.start]]
            self.recent_returns.extend(self.ep_return[fin].tolist())
            self.stats["episodes_slots"] += len(fin)
            self.ep_return[d] = 0.0
        self.need_reset[gs[b["reset"][sl] | done]] = True
        # 4. Crashed workers: truncate and start fresh episodes.
        if len(res.crashed_envs):
            cs = (res.crashed_envs[:, None] * self.P + np.arange(self.P)[None, :]).reshape(-1)
            self._close_pending(cs, truncate=True)
            self.need_reset[cs] = True
            self.ep_return[cs] = 0.0
            self.stats["worker_crashes"] += 1
        # 5. New episodes -> league assignment.
        if res.reset_op:
            new_envs = envs_sl
        else:
            new_envs = np.unique(np.concatenate([self.slot_env[d], res.crashed_envs])).astype(np.int64)
        if len(new_envs):
            self._assign(new_envs)

    def _assign(self, envs: np.ndarray) -> None:
        b = self.pool.buffers.np
        opps = self.league.sample_opponents(len(envs))
        for e, opp in zip(envs, opps):
            s0 = e * self.P
            # Any entry still pending in this env belongs to a finished episode.
            self._close_pending(np.arange(s0, s0 + self.P), truncate=True)
            q = slice(s0, s0 + self.P)
            teams = episode_teams(b["active"][q], b["reset"][q], b["team"][q])
            self.env_opp[e] = opp
            self.env_learner[e] = False
            for t in self.league.assign_teams(teams, opp):
                self.env_learner[e, t] = True
            self.stats[f"games_vs_{opp}"] += 1

    # ------------------------------------------------------------------ acting
    def _stage(self, g: int, name: str, src: torch.Tensor, idx: torch.Tensor) -> torch.Tensor:
        """Gather rows ``idx`` of ``src`` into a reusable (pinned on CUDA) host buffer."""
        key = (g, name)
        buf = self._staging.get(key)
        if buf is None or buf.shape[0] < src.shape[0]:
            buf = torch.empty_like(src, pin_memory=self.device.type == "cuda")
            self._staging[key] = buf
        out = buf[: len(idx)]
        torch.index_select(src, 0, idx, out=out)
        return out

    def _act(self, g: int, row: int) -> None:
        sl = self.pool.group_slots[g]
        b = self.pool.buffers
        bn = b.np
        active = bn["active"][sl]
        legal_np = bn["legal"][sl]
        bad = active & ~legal_np.any(axis=1)
        if bad.any():  # never let an active slot have no legal action
            legal_np[bad, 0] = True
            self.stats["empty_legal"] += int(bad.sum())
        learner = active & self._is_learner(sl)
        actions = np.full(sl.stop - sl.start, -1, np.int16)
        base = sl.start
        act_idx = np.nonzero(active)[0]
        if len(act_idx) == 0:
            self.pool.submit(g, OP_STEP, actions)
            return
        # Only active rows go to the device (inactive slots are zero padding); they
        # are gathered on the host into (pinned) staging buffers.
        nb = self.device.type == "cuda"
        n = len(act_idx)
        ai = torch.from_numpy(act_idx)
        spatial = self._stage(g, "spatial", b.spatial[sl], ai).to(self.device, non_blocking=nb)
        scalars = self._stage(g, "scalars", b.scalars[sl], ai).to(self.device, non_blocking=nb)
        legal = self._stage(g, "legal", b.legal[sl], ai).to(self.device, non_blocking=nb)
        reset = torch.from_numpy(self.need_reset[sl][act_idx]).to(self.device, non_blocking=nb)
        pos = np.full(sl.stop - sl.start, -1, np.int64)
        pos[act_idx] = np.arange(n)
        ac = torch.autocast("cuda", dtype=torch.bfloat16) if self.autocast else nullcontext()
        with torch.inference_mode(), ac:
            # learner
            li = np.nonzero(learner)[0]
            if len(li):
                lt = torch.as_tensor(pos[li], device=self.device)
                gi = torch.as_tensor(li + base, device=self.device)
                mem_in = self.memory[gi]
                logits, value, nmem = padded_forward(self.policy, spatial[lt], scalars[lt], legal[lt], mem_in, reset[lt], self.pad)
                a, logp = sample_actions(logits, legal[lt])
                self.memory[gi] = nmem.float()
                gsl = li + base
                st = self.storage
                sdev = st.device
                gst = torch.as_tensor(gsl, device=sdev)
                st.spatial[row, gst] = spatial[lt].to(sdev, st.spatial.dtype)
                st.scalars[row, gst] = scalars[lt].to(sdev)
                st.legal[row, gst] = legal[lt].to(sdev)
                st.memory[row, gst] = mem_in.to(sdev)
                st.reset[row, gst] = reset[lt].to(sdev)
                host = torch.stack([a.float(), logp.float(), value.float()]).cpu().numpy()  # one sync
                a_np = host[0].astype(np.int64)
                st.action[row, gsl] = a_np
                st.logp[row, gsl] = host[1]
                st.value[row, gsl] = host[2]
                st.reward[row, gsl] = 0.0
                st.done[row, gsl] = False
                st.valid[row, gsl] = True
                st.train[row, gsl] = True
                st.gtime[row, gsl] = self.env_clock[self.slot_env[gsl]]
                self.last_row[gsl] = row
                actions[li] = a_np
                self.stats["learner_decisions"] += len(li)
            # opponents, batched per policy
            oi = np.nonzero(active & ~learner)[0]
            if len(oi):
                envs = self.slot_env[oi + base]
                groups: dict[str, list[int]] = defaultdict(list)
                for k, e in enumerate(envs):
                    groups[self.env_opp[e]].append(k)
                for opp, ks in groups.items():
                    idx = oi[np.asarray(ks)]
                    ot = torch.as_tensor(pos[idx], device=self.device)
                    gi = torch.as_tensor(idx + base, device=self.device)
                    net = self.policy if opp == LATEST else self.league.model(opp)
                    logits, _v, nmem = padded_forward(net, spatial[ot], scalars[ot], legal[ot], self.memory[gi], reset[ot], self.pad)
                    if opp == LATEST:
                        a, _ = sample_actions(logits, legal[ot])
                    else:  # frozen members play with the server's decoder
                        a = deployed_sample(
                            logits,
                            legal[ot],
                            scalars[ot],
                            min_p=float(self.lcfg["opponent_min_p"]),
                            temperature=float(self.lcfg["opponent_temperature"]),
                        )
                    self.memory[gi] = nmem.float()
                    actions[idx] = a.cpu().numpy()
                self.stats["opponent_decisions"] += len(oi)
        self.need_reset[np.nonzero(active)[0] + base] = False
        if nb:  # the pinned group buffer is about to be overwritten by the next response
            torch.cuda.current_stream(self.device).synchronize()
        self.pool.submit(g, OP_STEP, actions)

    # ------------------------------------------------------------------ driver
    def start(self) -> None:
        """RESET every worker and issue the first decision (row 1)."""
        self.league.choose_resident()
        for g in range(self.pool.num_groups):
            self.pool.submit(g, OP_RESET)
        for g in range(self.pool.num_groups):
            res = self.pool.wait(g)
            self._process(res)
            self._act(g, 1)
        self.started = True

    def collect(self) -> dict:
        """Run T steps for every group; returns timing stats. Rows 1..T+1 filled."""
        if not self.started:
            self.start()
        t0 = time.perf_counter()
        wait_s = 0.0
        for t in range(1, self.T + 1):
            for g in range(self.pool.num_groups):
                res = self.pool.wait(g)
                wait_s += res.wait_s
                self._process(res)
                self._act(g, t + 1)
        self.stats["collect_s"] += time.perf_counter() - t0
        self.stats["wait_s"] += wait_s
        self.stats["env_steps"] += self.T * self.pool.num_envs
        self._finalize()
        return dict(self.stats)

    def _finalize(self) -> None:
        """Mark entries that cannot be trained yet (bootstrap row, pending carries)."""
        st = self.storage
        T = self.T
        st.train[T + 1] = False
        lr = self.last_row
        pending = np.nonzero((lr >= 0) & (lr <= T))[0]
        st.train[lr[pending], pending] = False
        self._carry = pending

    def advance(self) -> None:
        """After the update: row T+1 -> row 1, pending carries -> row 0."""
        st = self.storage
        T = self.T
        carry = self._carry
        carry_rows = self.last_row[carry]
        st.copy_entries(carry_rows, 0, carry)  # before row 1 is overwritten; row 0 -> row 0 is fine
        others = np.ones(self.S, bool)
        others[carry] = False
        st.clear_rows((0, np.nonzero(others)[0]))
        st.train[0, carry] = True
        all_slots = np.arange(self.S)
        st.clear_rows(1)
        st.copy_entries(np.full(self.S, T + 1), 1, all_slots)
        st.train[1] = st.valid[1]
        st.clear_rows(slice(2, T + 2))
        self.last_row[carry] = 0
        moved = self.last_row == T + 1
        self.last_row[moved] = 1
        self.rollouts += 1
        self.league.choose_resident()
