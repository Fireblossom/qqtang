"""Mock environment worker speaking the exact binary protocol of training/README.md.

Stands in for ``cmd/qqt-train-worker`` during trainer development and tests::

    python -m qqtrain.mock_worker -client-root <ignored> -config <json file>

The mock is deliberately strict: an action that is illegal for an active slot,
or a non -1 action for an inactive slot, is a protocol error (exit status 3), so
trainer bugs surface immediately.

Game (trivially learnable, so a smoke run can show learning):
* each env holds 2 or 4 players (``player_counts`` list) in two teams (IDs 1 and 2);
  unused tensor slots are inactive with team 0;
* every decision each active agent sees a target action as a one-hot in
  ``scalars[0:45]`` (always legal); choosing it earns ``hit_reward`` and a point
  for its team; legal masks are random (wait is always legal);
* each step an agent is eliminated with ``elim_prob`` (``elim_reward``) and stays
  inactive until the episode ends; a team with nobody left loses;
* after a random length in [min_len, max_len] the team with more points wins;
  every participant (also the eliminated ones) receives the terminal reward
  (+win_reward / -win_reward / draw_reward) with ``done=1``.

Info JSON per finished episode::

    {"env": e, "map": 0, "rule": 1, "length": n, "players": k,
     "outcome": {"draw": bool, "winner_team": t, "timed_out": bool},
     "teams": {"1": "win", "2": "loss"},
     "agents": [{"slot": p, "team": t, "return": r, "hits": h}, ...]}

Config keys (all optional): envs, agents, spatial_dtype, seed, player_counts (as the real worker),
min_len, max_len, hit_reward, elim_prob, elim_reward, win_reward, draw_reward,
legal_prob, crash_after_steps, crash_mode ("exit" | "garbage" | "hang"), step_sleep_ms.
"""

from __future__ import annotations

import argparse
import json
import struct
import sys
import time

import numpy as np

C, H, W, S, A = 117, 13, 15, 187, 45
REQ_MAGIC = 0x57545151
RESP_MAGIC = 0x52545151
OP_RESET, OP_STEP, OP_CLOSE = 1, 2, 3


class MockEnv:
    def __init__(self, cfg: dict):
        self.E = int(cfg.get("envs", 8))
        self.P = int(cfg.get("agents", 4))
        self.dtype = np.float16 if cfg.get("spatial_dtype", "f16") == "f16" else np.float32
        self.rng = np.random.default_rng(int(cfg.get("seed", 0)))
        self.players = [int(x) for x in cfg.get("player_counts", cfg.get("players", [2, 4]))]
        assert all(2 <= p <= self.P and p % 2 == 0 for p in self.players)
        self.min_len = int(cfg.get("min_len", 20))
        self.max_len = int(cfg.get("max_len", 60))
        self.hit_reward = float(cfg.get("hit_reward", 0.05))
        self.elim_prob = float(cfg.get("elim_prob", 0.005))
        self.elim_reward = float(cfg.get("elim_reward", -0.3))
        self.win_reward = float(cfg.get("win_reward", 1.0))
        self.draw_reward = float(cfg.get("draw_reward", -0.5))
        self.legal_prob = float(cfg.get("legal_prob", 0.3))
        self.crash_after = int(cfg.get("crash_after_steps", 0))
        self.crash_mode = cfg.get("crash_mode", "exit")
        self.step_sleep = float(cfg.get("step_sleep_ms", 0)) / 1000.0
        E, P = self.E, self.P
        self.team = np.zeros((E, P), np.uint8)
        self.alive = np.zeros((E, P), bool)
        self.length = np.zeros(E, np.int64)
        self.t = np.zeros(E, np.int64)
        self.score = np.zeros((E, 3), np.int64)
        self.ret = np.zeros((E, P), np.float64)
        self.hits = np.zeros((E, P), np.int64)
        self.nplayers = np.zeros(E, np.int64)
        self.target = np.zeros((E, P), np.int64)
        self.legal = np.zeros((E, P, A), np.uint8)
        self.spatial = np.zeros((E, P, C, H, W), self.dtype)
        self.steps = 0

    def _reset_env(self, e: int):
        n = int(self.rng.choice(self.players))
        self.nplayers[e] = n
        self.team[e] = 0
        self.team[e, :n] = [1 + (i % 2) for i in range(n)]
        self.alive[e] = False
        self.alive[e, :n] = True
        self.length[e] = self.rng.integers(self.min_len, self.max_len + 1)
        self.t[e] = 0
        self.score[e] = 0
        self.ret[e] = 0
        self.hits[e] = 0

    def _observe(self):
        E, P = self.E, self.P
        legal = (self.rng.random((E, P, A)) < self.legal_prob).astype(np.uint8)
        legal[:, :, 0] = 1
        target = self.rng.integers(0, A, size=(E, P))
        legal[np.arange(E)[:, None], np.arange(P)[None, :], target] = 1
        legal[~self.alive] = 0
        self.legal = legal
        self.target = target
        scalars = np.zeros((E, P, S), np.float32)
        ee, pp = np.nonzero(self.alive)
        scalars[ee, pp, target[ee, pp]] = 1.0
        scalars[:, :, 45] = (self.t / np.maximum(self.length, 1))[:, None]
        scalars[:, :, 46] = self.team
        scalars[~self.alive] = 0
        self.spatial[:] = 0
        self.spatial[:, :, 0] = (self.team.astype(self.dtype) / 2.0)[:, :, None, None]
        self.spatial[~self.alive] = 0
        return scalars

    def reset(self):
        for e in range(self.E):
            self._reset_env(e)
        scalars = self._observe()
        z = np.zeros((self.E, self.P), np.uint8)
        return self._pack(scalars, self.alive.astype(np.uint8), self.alive.astype(np.uint8), np.zeros((self.E, self.P), np.float32), z, [])

    def step(self, actions: np.ndarray):
        E, P = self.E, self.P
        self.steps += 1
        if self.crash_after and self.steps >= self.crash_after:
            if self.crash_mode == "garbage":
                sys.stdout.buffer.write(b"garbage!" * 4)
                sys.stdout.buffer.flush()
                time.sleep(5)
            elif self.crash_mode == "hang":
                time.sleep(3600)
            sys.exit(17)
        if self.step_sleep:
            time.sleep(self.step_sleep)
        actions = actions.reshape(E, P).astype(np.int64)
        if np.any(actions[~self.alive] != -1):
            die("action for an inactive slot")
        ee, pp = np.nonzero(self.alive)
        a = actions[ee, pp]
        if np.any(a < 0) or np.any(a >= A) or np.any(self.legal[ee, pp, np.clip(a, 0, A - 1)] == 0):
            die("illegal or missing action for an active slot")
        reward = np.zeros((E, P), np.float32)
        hit = a == self.target[ee, pp]
        reward[ee, pp] += np.where(hit, self.hit_reward, 0.0).astype(np.float32)
        self.hits[ee, pp] += hit
        np.add.at(self.score, (ee, self.team[ee, pp]), hit.astype(np.int64))
        elim = self.rng.random((E, P)) < self.elim_prob
        elim &= self.alive
        reward[elim] += self.elim_reward
        self.alive &= ~elim
        self.t += 1
        done = np.zeros((E, P), np.uint8)
        reset = np.zeros((E, P), np.uint8)
        episodes = []
        self.ret += reward
        for e in range(E):
            n = self.nplayers[e]
            alive_teams = {int(self.team[e, p]) for p in range(n) if self.alive[e, p]}
            ended = self.t[e] >= self.length[e] or len(alive_teams) < 2
            if not ended:
                continue
            if len(alive_teams) == 1:
                winner, draw = alive_teams.pop(), False
            elif len(alive_teams) == 0:
                winner, draw = 0, True
            else:
                s1, s2 = self.score[e, 1], self.score[e, 2]
                winner, draw = (0, True) if s1 == s2 else ((1 if s1 > s2 else 2), False)
            for p in range(n):
                tr = self.draw_reward if draw else (self.win_reward if self.team[e, p] == winner else -self.win_reward)
                reward[e, p] += tr
                self.ret[e, p] += tr
                done[e, p] = 1
            episodes.append(
                {
                    "env": e,
                    "map": 0,
                    "rule": 1,
                    "length": int(self.t[e]),
                    "players": int(n),
                    "outcome": {"draw": draw, "winner_team": winner, "timed_out": bool(self.t[e] >= self.length[e])},
                    "teams": {str(t): ("draw" if draw else ("win" if t == winner else "loss")) for t in (1, 2)},
                    "agents": [
                        {"slot": p, "team": int(self.team[e, p]), "return": float(self.ret[e, p]), "hits": int(self.hits[e, p])}
                        for p in range(n)
                    ],
                }
            )
            self._reset_env(e)
            reset[e, :] = self.alive[e]
        scalars = self._observe()
        return self._pack(scalars, self.alive.astype(np.uint8), reset, reward, done, episodes)

    def _pack(self, scalars, active, reset, reward, done, episodes) -> list[bytes]:
        info = json.dumps({"episodes": episodes}).encode()
        parts = [
            self.spatial.tobytes(),
            scalars.astype("<f4").tobytes(),
            self.legal.tobytes(),
            active.tobytes(),
            reset.tobytes(),
            self.team.tobytes(),
            reward.astype("<f4").tobytes(),
            done.tobytes(),
            struct.pack("<I", len(info)),
            info,
        ]
        payload = sum(len(p) for p in parts)
        return [struct.pack("<II", RESP_MAGIC, payload)] + parts


def die(msg: str):
    print(f"mock_worker: protocol error: {msg}", file=sys.stderr, flush=True)
    sys.exit(3)


def read_exact(f, n: int) -> bytes:
    buf = bytearray()
    while len(buf) < n:
        chunk = f.read(n - len(buf))
        if not chunk:
            sys.exit(0)  # parent went away
        buf += chunk
    return bytes(buf)


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("-client-root", default="")
    ap.add_argument("-config", required=True)
    args = ap.parse_args(argv)
    with open(args.config) as f:
        cfg = json.load(f)
    env = MockEnv(cfg)
    out = sys.stdout.buffer
    hello = {
        "protocol": 1,
        "envs": env.E,
        "agents": env.P,
        "channels": C,
        "height": H,
        "width": W,
        "scalars": S,
        "actions": A,
        "tensor_version": 18,
        "spatial_dtype": "f16" if env.dtype == np.float16 else "f32",
    }
    out.write((json.dumps(hello) + "\n").encode())
    out.flush()
    inp = sys.stdin.buffer
    while True:
        magic, op, size = struct.unpack("<IBI", read_exact(inp, 9))
        if magic != REQ_MAGIC:
            die(f"bad request magic {magic:#x}")
        payload = read_exact(inp, size) if size else b""
        if op == OP_RESET:
            parts = env.reset()
        elif op == OP_STEP:
            if size != 2 * env.E * env.P:
                die(f"STEP payload {size} != {2 * env.E * env.P}")
            parts = env.step(np.frombuffer(payload, "<i2"))
        elif op == OP_CLOSE:
            return 0
        else:
            die(f"unknown op {op}")
        for p in parts:
            out.write(p)
        out.flush()


if __name__ == "__main__":
    sys.exit(main())
