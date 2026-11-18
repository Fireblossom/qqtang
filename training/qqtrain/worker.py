"""Client for the environment worker protocol (training/README.md).

``WorkerPool`` spawns N worker processes, splits them into G pipelining groups
and owns one set of observation buffers covering every slot of every worker
(``[S_total, ...]`` torch tensors, pinned on CUDA). Responses are read with
``readinto`` straight into each worker's slice of those buffers: no
intermediate copy, no per-step allocation. One I/O thread per worker does the
blocking pipe reads (they release the GIL), so while the policy runs on group
A's observations, group B's workers are simulating.

Buffer ownership rule: a group's slice is overwritten by its next response, so
the caller must consume (copy to the rollout storage / device) the
observations of group g *before* calling ``submit(g, ...)`` again.

Crashes: an EOF, broken pipe, bad magic, malformed payload or timeout kills
the worker, restarts it (new seed) and sends RESET; the response then carries
``crashed=True`` for that worker's environments, and every slot of them starts
a new episode. More than ``max_restarts`` restarts of one worker abort training.
"""

from __future__ import annotations

import concurrent.futures as cf
import json
import logging
import os
import struct
import subprocess
import sys
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path

import numpy as np
import torch

from .policy_api import ACTIONS, CHANNELS, HEIGHT, SCALARS, WIDTH

REQ_MAGIC = 0x57545151  # "QQTW"
RESP_MAGIC = 0x52545151  # "QQTR"
OP_RESET, OP_STEP, OP_CLOSE = 1, 2, 3
PROTOCOL = 1

log = logging.getLogger(__name__)


class WorkerError(RuntimeError):
    pass


@dataclass
class Hello:
    envs: int
    agents: int
    spatial_dtype: str
    raw: dict

    @classmethod
    def parse(cls, line: bytes) -> "Hello":
        try:
            d = json.loads(line)
        except json.JSONDecodeError as exc:
            raise WorkerError(f"bad hello line {line[:200]!r}") from exc
        expect = {"protocol": PROTOCOL, "channels": CHANNELS, "height": HEIGHT, "width": WIDTH, "scalars": SCALARS, "actions": ACTIONS}
        for key, value in expect.items():
            if d.get(key) != value:
                raise WorkerError(f"worker hello {key}={d.get(key)!r}, expected {value}")
        if d.get("tensor_version", 18) != 18:
            raise WorkerError(f"tensor_version {d.get('tensor_version')} != 18")
        dtype = d.get("spatial_dtype", "f32")
        if dtype not in ("f16", "f32"):
            raise WorkerError(f"spatial_dtype {dtype!r}")
        return cls(int(d["envs"]), int(d["agents"]), dtype, d)


def field_sizes(n: int, spatial_itemsize: int) -> list[tuple[str, int]]:
    """Byte size of each fixed response field for n = E*P slots, in wire order."""
    return [
        ("spatial", n * CHANNELS * HEIGHT * WIDTH * spatial_itemsize),
        ("scalars", n * SCALARS * 4),
        ("legal", n * ACTIONS),
        ("active", n),
        ("reset", n),
        ("team", n),
        ("reward", n * 4),
        ("done", n),
    ]


def _read_exact_into(f, view: memoryview) -> None:
    pos, n = 0, len(view)
    while pos < n:
        got = f.readinto(view[pos:])
        if not got:
            raise WorkerError("worker closed its stdout (EOF)")
        pos += got


def _read_exact(f, n: int) -> bytes:
    buf = bytearray(n)
    _read_exact_into(f, memoryview(buf))
    return bytes(buf)


def _grow_pipe(f, size: int = 1 << 20) -> None:
    """Linux: enlarge the pipe buffer (default 64 KiB) to cut read syscalls per response."""
    if not sys.platform.startswith("linux"):
        return
    try:
        import fcntl

        fcntl.fcntl(f.fileno(), 1031, size)  # F_SETPIPE_SZ; capped by /proc/sys/fs/pipe-max-size
    except OSError:
        pass


def _write_all(f, data: bytes) -> None:
    view = memoryview(data)
    while view:
        n = f.write(view)
        if n is None:
            raise WorkerError("non-blocking write to worker stdin")
        view = view[n:]


class Worker:
    """One worker process. Not thread-safe; the pool gives each its own thread."""

    def __init__(self, index: int, command: list[str], client_root: str, config: dict, run_dir: Path, env: dict | None = None):
        self.index = index
        self.command = [sys.executable if c == "{python}" else c for c in command]
        self.client_root = client_root
        self.config = dict(config)
        self.run_dir = Path(run_dir)
        self.env = env or {}
        self.proc: subprocess.Popen | None = None
        self.hello: Hello | None = None
        self.restarts = 0
        self.log_path = self.run_dir / f"worker_{index:03d}.log"
        self.config_path = self.run_dir / f"worker_{index:03d}.json"

    def start(self) -> Hello:
        self.run_dir.mkdir(parents=True, exist_ok=True)
        self.config_path.write_text(json.dumps(self.config, indent=1))
        cmd = self.command + ["-client-root", self.client_root, "-config", str(self.config_path)]
        env = dict(os.environ)
        env.update({k: str(v) for k, v in self.env.items()})
        with open(self.log_path, "ab") as logf:
            logf.write(f"\n=== start {time.strftime('%F %T')} restarts={self.restarts}: {' '.join(cmd)}\n".encode())
            logf.flush()
            self.proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=logf, bufsize=0, env=env)
        _grow_pipe(self.proc.stdout)
        line = self.proc.stdout.readline()
        if not line:
            raise WorkerError(f"worker {self.index} exited before hello; log tail:\n{self.log_tail()}")
        self.hello = Hello.parse(line)
        return self.hello

    def log_tail(self, n: int = 2000) -> str:
        try:
            data = self.log_path.read_bytes()
        except OSError:
            return ""
        return data[-n:].decode(errors="replace")

    def send(self, op: int, payload: bytes = b"") -> None:
        _write_all(self.proc.stdin, struct.pack("<IBI", REQ_MAGIC, op, len(payload)) + payload)

    def recv(self, views: list[memoryview]) -> dict:
        """Read one response; fixed fields go into ``views`` (wire order).

        Returns the info JSON: ``episodes`` (list) and, with the server
        decision cadence, ``ticks`` (engine ticks each env advanced).
        """
        f = self.proc.stdout
        magic, size = struct.unpack("<II", _read_exact(f, 8))
        if magic != RESP_MAGIC:
            raise WorkerError(f"bad response magic {magic:#010x}")
        fixed = 0
        for v in views:
            _read_exact_into(f, v)
            fixed += len(v)
        (info_len,) = struct.unpack("<I", _read_exact(f, 4))
        # Accept payload sizes that count the info block or only the fixed fields.
        if size not in (fixed + 4 + info_len, fixed):
            raise WorkerError(f"payload size {size} does not match fields {fixed} + info {info_len}")
        if info_len > 64 << 20:
            raise WorkerError(f"info length {info_len} too large")
        info = _read_exact(f, info_len) if info_len else b""
        if not info:
            return {"episodes": []}
        try:
            doc = json.loads(info)
            doc["episodes"] = list(doc.get("episodes") or [])
            return doc
        except (json.JSONDecodeError, AttributeError, TypeError) as exc:
            raise WorkerError(f"bad info JSON {info[:200]!r}") from exc

    def kill(self) -> None:
        if self.proc is not None and self.proc.poll() is None:
            self.proc.kill()
        if self.proc is not None:
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                pass

    def close(self, timeout: float = 5.0) -> None:
        if self.proc is None:
            return
        try:
            if self.proc.poll() is None:
                self.send(OP_CLOSE)
                self.proc.stdin.close()
                self.proc.wait(timeout=timeout)
        except (OSError, WorkerError, subprocess.TimeoutExpired):
            pass
        self.kill()
        for stream in (self.proc.stdout, self.proc.stdin):
            try:
                stream.close()
            except OSError:
                pass


@dataclass
class GroupResult:
    group: int
    slots: slice  # global slot range of this group
    episodes: list[dict]  # info entries; "env" rewritten to the global env index
    crashed_envs: np.ndarray  # global env indices whose worker restarted (all start new episodes)
    reset_op: bool  # response to RESET (every env starts a new episode)
    wait_s: float = 0.0
    # Engine ticks (20 ms) each env of the group advanced during this STEP: the
    # worker's "ticks" list with the server cadence, else 5 (0 after RESET).
    ticks: np.ndarray | None = None


@dataclass
class Buffers:
    spatial: torch.Tensor
    scalars: torch.Tensor
    legal: torch.Tensor
    active: torch.Tensor
    reset: torch.Tensor
    team: torch.Tensor
    reward: torch.Tensor
    done: torch.Tensor
    np: dict = field(default_factory=dict)  # numpy views of the same memory


def _alloc(shape, dtype, pin: bool) -> torch.Tensor:
    t = torch.zeros(shape, dtype=dtype)
    return t.pin_memory() if pin else t


class WorkerPool:
    def __init__(
        self,
        command: list[str],
        client_root: str,
        configs: list[dict],
        run_dir: str | Path,
        groups: int = 2,
        pin_memory: bool = False,
        step_timeout_s: float = 120.0,
        max_restarts: int = 20,
        env: dict | None = None,
        seed_restart_stride: int = 7919,
    ):
        if not configs:
            raise ValueError("need at least one worker")
        self.run_dir = Path(run_dir)
        self.workers = [Worker(i, command, client_root, c, self.run_dir, env) for i, c in enumerate(configs)]
        self.num_groups = max(1, min(groups, len(self.workers)))
        self.pin_memory = pin_memory
        self.step_timeout_s = step_timeout_s
        self.max_restarts = max_restarts
        self.seed_restart_stride = seed_restart_stride
        self.executor = cf.ThreadPoolExecutor(max_workers=len(self.workers), thread_name_prefix="qqt-worker")
        self.pending: dict[int, tuple[list[cf.Future], bool]] = {}
        self._lock = threading.Lock()
        self.total_restarts = 0
        self.started = False

    # ------------------------------------------------------------------ setup
    def start(self) -> None:
        hellos = list(self.executor.map(lambda w: w.start(), self.workers))
        P = {h.agents for h in hellos}
        dt = {h.spatial_dtype for h in hellos}
        if len(P) != 1 or len(dt) != 1:
            raise WorkerError(f"workers disagree on agents {P} or spatial dtype {dt}")
        self.P = P.pop()
        self.spatial_dtype = dt.pop()
        self.env_counts = [h.envs for h in hellos]
        self.env_offsets = np.concatenate([[0], np.cumsum(self.env_counts)]).astype(np.int64)
        self.num_envs = int(self.env_offsets[-1])
        self.num_slots = self.num_envs * self.P
        # Contiguous groups balanced by env count.
        n = len(self.workers)
        bounds = np.linspace(0, n, self.num_groups + 1).round().astype(int)
        self.group_workers = [list(range(bounds[g], bounds[g + 1])) for g in range(self.num_groups)]
        self.group_slots = [
            slice(int(self.env_offsets[ws[0]]) * self.P, int(self.env_offsets[ws[-1] + 1]) * self.P) for ws in self.group_workers
        ]
        S, pin = self.num_slots, self.pin_memory
        sdt = torch.float16 if self.spatial_dtype == "f16" else torch.float32
        b = Buffers(
            spatial=_alloc((S, CHANNELS, HEIGHT, WIDTH), sdt, pin),
            scalars=_alloc((S, SCALARS), torch.float32, pin),
            legal=_alloc((S, ACTIONS), torch.bool, pin),
            active=_alloc((S,), torch.bool, pin),
            reset=_alloc((S,), torch.bool, pin),
            team=_alloc((S,), torch.uint8, pin),
            reward=_alloc((S,), torch.float32, pin),
            done=_alloc((S,), torch.bool, pin),
        )
        for name in ("spatial", "scalars", "legal", "active", "reset", "team", "reward", "done"):
            b.np[name] = getattr(b, name).numpy()
        self.buffers = b
        self.itemsize = 2 if self.spatial_dtype == "f16" else 4
        self.started = True
        log.info("worker pool: %d workers, %d envs, %d agents/env, %d groups, spatial %s", n, self.num_envs, self.P, self.num_groups, self.spatial_dtype)

    def _views(self, i: int) -> list[memoryview]:
        a = int(self.env_offsets[i]) * self.P
        z = int(self.env_offsets[i + 1]) * self.P
        out = []
        for name, _size in field_sizes(z - a, self.itemsize):
            arr = self.buffers.np[name][a:z]
            out.append(memoryview(arr.reshape(-1).view(np.uint8)))
        return out

    # ------------------------------------------------------------------ exchange
    def _exchange(self, i: int, op: int, payload: bytes) -> tuple[dict, bool]:
        w = self.workers[i]
        try:
            w.send(op, payload)
            return w.recv(self._views(i)), False
        except (WorkerError, OSError, struct.error, ValueError) as exc:
            err = exc
        while True:
            with self._lock:
                self.total_restarts += 1
            w.restarts += 1
            log.error("worker %d failed (%s); restart %d. log tail:\n%s", i, err, w.restarts, w.log_tail(800))
            if w.restarts > self.max_restarts:
                raise WorkerError(f"worker {i} exceeded {self.max_restarts} restarts; last error: {err}")
            w.kill()
            if "seed" in w.config:
                w.config["seed"] = int(w.config["seed"]) + self.seed_restart_stride
            try:
                hello = w.start()
                if hello.envs != self.env_counts[i] or hello.agents != self.P or hello.spatial_dtype != self.spatial_dtype:
                    raise WorkerError("restarted worker changed its shape")
                w.send(OP_RESET)
                return w.recv(self._views(i)), True
            except (WorkerError, OSError, struct.error, ValueError) as exc:
                err = exc

    def submit(self, group: int, op: int, actions: np.ndarray | None = None) -> None:
        """Start a RESET or STEP for every worker of ``group`` (non-blocking).

        ``actions``: int16 [slots of this group] (-1 for inactive slots).
        """
        if group in self.pending:
            raise RuntimeError(f"group {group} already has a request in flight")
        futures = []
        base = self.group_slots[group].start
        for i in self.group_workers[group]:
            payload = b""
            if op == OP_STEP:
                a = int(self.env_offsets[i]) * self.P - base
                z = int(self.env_offsets[i + 1]) * self.P - base
                payload = np.ascontiguousarray(actions[a:z], dtype="<i2").tobytes()
            futures.append(self.executor.submit(self._exchange, i, op, payload))
        self.pending[group] = (futures, op == OP_RESET)

    def wait(self, group: int) -> GroupResult:
        futures, reset_op = self.pending.pop(group)
        t0 = time.perf_counter()
        episodes: list[dict] = []
        crashed: list[np.ndarray] = []
        g0 = int(self.env_offsets[self.group_workers[group][0]])
        g1 = int(self.env_offsets[self.group_workers[group][-1] + 1])
        ticks = np.full(g1 - g0, 0 if reset_op else 5, np.int64)
        for i, fut in zip(self.group_workers[group], futures):
            try:
                info, was_crash = fut.result(timeout=self.step_timeout_s)
            except cf.TimeoutError:
                log.error("worker %d timed out after %.0fs; killing it", i, self.step_timeout_s)
                self.workers[i].kill()  # unblocks the reader thread, which restarts the worker
                info, was_crash = fut.result(timeout=self.step_timeout_s * 2)
            off = int(self.env_offsets[i])
            if was_crash:
                crashed.append(np.arange(off, off + self.env_counts[i]))
                ticks[off - g0 : off - g0 + self.env_counts[i]] = 0
                continue
            for ep in info["episodes"]:
                ep = dict(ep)
                ep["env"] = off + int(ep.get("env", 0))
                ep["worker"] = i
                episodes.append(ep)
            wt = info.get("ticks")
            if wt is not None and len(wt) == self.env_counts[i]:
                ticks[off - g0 : off - g0 + self.env_counts[i]] = wt
        crashed_envs = np.concatenate(crashed) if crashed else np.zeros(0, np.int64)
        return GroupResult(group, self.group_slots[group], episodes, crashed_envs, reset_op, time.perf_counter() - t0, ticks)

    def close(self) -> None:
        for g in list(self.pending):
            futures, _ = self.pending.pop(g)
            for fut in futures:
                try:
                    fut.result(timeout=self.step_timeout_s)
                except Exception:  # noqa: BLE001 - shutting down
                    pass
        list(self.executor.map(lambda w: w.close(), self.workers))
        self.executor.shutdown(wait=True)

    def __enter__(self):
        if not self.started:
            self.start()
        return self

    def __exit__(self, *exc):
        self.close()


def worker_configs(cfg: dict, num_workers: int | None = None, base_config: dict | None = None, seed: int | None = None) -> list[dict]:
    """Per-worker JSON configs: the opaque ``worker.config`` table plus a distinct seed."""
    wcfg = cfg["worker"]
    n = num_workers or int(wcfg["num_workers"])
    base = dict(base_config if base_config is not None else wcfg["config"])
    seed0 = int(seed if seed is not None else cfg["run"]["seed"]) * 1_000_003
    out = []
    for i in range(n):
        c = dict(base)
        c["seed"] = seed0 + i * int(wcfg["seed_stride"]) + int(base.get("seed", 0))
        out.append(c)
    return out
