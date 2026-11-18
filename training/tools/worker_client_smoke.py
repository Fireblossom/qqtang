#!/usr/bin/env python3
"""Reference client and smoke test for qqt-train-worker (training/README.md).

Starts the worker, sends RESET and a few hundred STEPs with uniformly random
legal actions, checks shapes, flags and reward/episode consistency, and
prints throughput. Needs only the standard library and numpy.

    python3 training/tools/worker_client_smoke.py \
        --worker training/bin/qqt-train-worker-darwin-arm64 \
        --client-root ~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched \
        --envs 64 --player-counts 2,4 --steps 300 --dtype f16
"""

import argparse
import json
import os
import struct
import subprocess
import sys
import tempfile
import time

import numpy as np

REQUEST_MAGIC = 0x57545151  # "QQTW"
RESPONSE_MAGIC = 0x52545151  # "QQTR"
OP_RESET, OP_STEP, OP_CLOSE = 1, 2, 3


class WorkerClient:
    """Minimal protocol client: one worker process, synchronous requests."""

    def __init__(self, argv, env=None):
        self.process = subprocess.Popen(
            argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, env=env, bufsize=0
        )
        self.stdin, self.stdout = self.process.stdin, self.process.stdout
        line = self.stdout.readline()
        if not line:
            raise RuntimeError("worker exited before the hello line")
        self.hello = json.loads(line)
        h = self.hello
        if h["protocol"] != 1:
            raise RuntimeError(f"unsupported protocol {h['protocol']}")
        self.envs, self.agents = h["envs"], h["agents"]
        self.slots = self.envs * self.agents
        self.spatial_dtype = np.float16 if h["spatial_dtype"] == "f16" else np.float32
        self.spatial_shape = (self.envs, self.agents, h["channels"], h["height"], h["width"])
        self.bytes_received = 0

    def _send(self, op, payload=b""):
        self.stdin.write(struct.pack("<IBI", REQUEST_MAGIC, op, len(payload)) + payload)
        self.stdin.flush()

    def _read_exact(self, size, buffer=None):
        """Reads size bytes with readinto (no chunk joins); the pipe hands
        over at most its buffer size (64 KiB on macOS) per call."""
        if buffer is None or len(buffer) < size:
            buffer = bytearray(size)
        view = memoryview(buffer)[:size]
        filled = 0
        while filled < size:
            count = self.stdout.readinto(view[filled:])
            if not count:
                raise RuntimeError("worker closed its output")
            filled += count
        return view

    def _receive(self):
        magic, length = struct.unpack("<II", self._read_exact(8))
        if magic != RESPONSE_MAGIC:
            raise RuntimeError(f"bad response magic {magic:#x}")
        # A fresh buffer per response: the returned arrays alias it.
        payload = self._read_exact(length)
        self.bytes_received += 8 + length
        h, E, P = self.hello, self.envs, self.agents
        offset = 0

        def take(dtype, shape):
            nonlocal offset
            count = int(np.prod(shape))
            array = np.frombuffer(payload, dtype=dtype, count=count, offset=offset).reshape(shape)
            offset += count * np.dtype(dtype).itemsize
            return array

        out = {
            "spatial": take(self.spatial_dtype, self.spatial_shape),
            "scalars": take(np.float32, (E, P, h["scalars"])),
            "legal": take(np.uint8, (E, P, h["actions"])),
            "active": take(np.uint8, (E, P)),
            "reset": take(np.uint8, (E, P)),
            "team": take(np.uint8, (E, P)),
            "reward": take(np.float32, (E, P)),
            "done": take(np.uint8, (E, P)),
        }
        info_length = int(take(np.uint32, (1,))[0])
        out["info"] = json.loads(bytes(payload[offset:offset + info_length]).decode("utf-8"))
        offset += info_length
        if offset != length:
            raise RuntimeError(f"response has {length - offset} trailing bytes")
        return out

    def reset(self):
        self._send(OP_RESET)
        return self._receive()

    def step(self, actions):
        actions = np.ascontiguousarray(actions, dtype="<i2").reshape(self.slots)
        self._send(OP_STEP, actions.tobytes())
        return self._receive()

    def close(self):
        try:
            self._send(OP_CLOSE)
        finally:
            self.stdin.close()
            code = self.process.wait(timeout=30)
        if code != 0:
            raise RuntimeError(f"worker exited with {code}")


def random_legal_actions(rng, observation):
    legal = observation["legal"].reshape(-1, observation["legal"].shape[-1]).astype(bool)
    scores = rng.random(legal.shape)
    scores[~legal] = -1.0
    actions = scores.argmax(axis=1).astype(np.int16)
    actions[observation["active"].reshape(-1) == 0] = -1
    return actions


def check(condition, message):
    if not condition:
        raise AssertionError(message)


def check_observation(client, obs, after_reset):
    h = client.hello
    check(obs["spatial"].shape == client.spatial_shape, "spatial shape")
    check(np.isfinite(obs["spatial"]).all() and np.isfinite(obs["scalars"]).all(), "non-finite observation")
    has_legal = obs["legal"].any(axis=2)
    check((has_legal == (obs["active"] != 0)).all(), "active must equal 'has a legal action'")
    check(((obs["active"] == 0) | (obs["team"] != 0)).all(), "active slot without team")
    inactive = obs["active"] == 0
    check(not obs["spatial"][inactive].any(), "inactive slot has a non-zero spatial row")
    check(((obs["reset"] == 0) | (obs["team"] != 0)).all(), "reset on a padding slot")
    if after_reset:
        check(((obs["reset"] != 0) == (obs["team"] != 0)).all(), "RESET must flag every real slot")
        check(not obs["reward"].any() and not obs["done"].any(), "RESET returned reward/done")
    check(obs["legal"].shape[-1] == h["actions"], "legal shape")


def run(args):
    config = {
        "envs": args.envs,
        "player_counts": [int(x) for x in args.player_counts.split(",")],
        "spatial_dtype": args.dtype,
        "seed": args.seed,
        "workers": args.threads,
    }
    if args.agents:
        config["agents"] = args.agents
    if args.config:
        with open(args.config) as handle:
            config.update(json.load(handle))
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as handle:
        json.dump(config, handle)
        config_path = handle.name
    env = dict(os.environ)
    if args.gomaxprocs:
        env["GOMAXPROCS"] = str(args.gomaxprocs)
    client = WorkerClient([args.worker, "-client-root", args.client_root, "-config", config_path], env=env)
    try:
        h = client.hello
        print(f"hello: {json.dumps(h)}", file=sys.stderr)
        rng = np.random.default_rng(args.seed)
        obs = client.reset()
        check_observation(client, obs, after_reset=True)
        team = obs["team"].copy()
        returns = np.zeros((client.envs, client.agents), dtype=np.float64)
        decisions = finished = 0
        reset_bytes = client.bytes_received
        start = time.perf_counter()
        for step in range(args.steps):
            actions = random_legal_actions(rng, obs)
            decisions += int((obs["active"] != 0).sum())
            obs = client.step(actions)
            if args.no_check:
                finished += len(obs["info"]["episodes"])
                continue
            check_observation(client, obs, after_reset=False)
            check(not obs["reward"][team == 0].any(), "reward on a padding slot")
            returns += obs["reward"]
            ended = {episode["env"]: episode for episode in obs["info"]["episodes"]}
            finished += len(ended)
            for env_index in range(client.envs):
                episode = ended.get(env_index)
                done_row = obs["done"][env_index] != 0
                if episode is None:
                    check(not done_row.any() and not obs["reset"][env_index].any(),
                          f"step {step} env {env_index}: done/reset without episode info")
                    continue
                agents = episode["agents"]
                check(done_row[: len(agents)].all() and not done_row[len(agents):].any(),
                      f"step {step} env {env_index}: done flags do not match the episode")
                check(((obs["reset"][env_index] != 0) == (obs["team"][env_index] != 0)).all(),
                      f"step {step} env {env_index}: new episode not flagged")
                for agent in agents:
                    slot = agent["slot"]
                    check(abs(returns[env_index, slot] - agent["return"]) < 1e-3,
                          f"env {env_index} slot {slot}: summed rewards {returns[env_index, slot]} != return {agent['return']}")
                    check(agent["team"] == team[env_index, slot], "episode team mismatch")
                returns[env_index] = 0.0
            team = obs["team"].copy()
        elapsed = time.perf_counter() - start
        received = client.bytes_received - reset_bytes
        print(
            f"ok: {args.steps} steps, {decisions} decisions, {finished} episodes in {elapsed:.2f}s: "
            f"{decisions / elapsed:.0f} decisions/s, {args.steps / elapsed:.1f} steps/s, "
            f"{received / args.steps / 1e6:.2f} MB/step, {received / elapsed / 1e6:.0f} MB/s"
        )
    finally:
        client.close()
        os.unlink(config_path)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--worker", required=True, help="qqt-train-worker binary")
    parser.add_argument("--client-root", required=True, help="patched client directory (map data)")
    parser.add_argument("--config", help="extra worker config JSON merged over the flags")
    parser.add_argument("--envs", type=int, default=64)
    parser.add_argument("--agents", type=int, default=0, help="tensor slots per env (0 = max player count)")
    parser.add_argument("--player-counts", default="2,4")
    parser.add_argument("--dtype", choices=["f16", "f32"], default="f16")
    parser.add_argument("--steps", type=int, default=300)
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--threads", type=int, default=0, help="worker goroutines (0 = GOMAXPROCS)")
    parser.add_argument("--gomaxprocs", type=int, default=0)
    parser.add_argument("--no-check", action="store_true", help="skip consistency checks (pure throughput)")
    run(parser.parse_args())


if __name__ == "__main__":
    main()
