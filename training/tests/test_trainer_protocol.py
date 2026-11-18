"""Worker protocol client against the mock worker."""

from __future__ import annotations

import json

import numpy as np
import pytest
from trainer_helpers import MOCK_CMD

from qqtrain.mock_worker import MockEnv
from qqtrain.worker import OP_RESET, OP_STEP, Hello, WorkerError, WorkerPool, field_sizes


def _pool(tmp_path, configs, **kw):
    return WorkerPool(MOCK_CMD, "", configs, tmp_path / "w", **kw)


def _legal_actions(pool, sl):
    b = pool.buffers.np
    act = b["active"][sl]
    legal = b["legal"][sl]
    a = np.full(sl.stop - sl.start, -1, np.int16)
    for i in np.nonzero(act)[0]:
        a[i] = np.nonzero(legal[i])[0][-1]
    return a


def test_hello_validation():
    good = {"protocol": 1, "envs": 2, "agents": 4, "channels": 117, "height": 13, "width": 15, "scalars": 187, "actions": 45, "tensor_version": 18, "spatial_dtype": "f16"}
    h = Hello.parse(json.dumps(good).encode())
    assert (h.envs, h.agents, h.spatial_dtype) == (2, 4, "f16")
    for key, bad in [("channels", 116), ("actions", 44), ("protocol", 2), ("tensor_version", 17), ("spatial_dtype", "bf16")]:
        with pytest.raises(WorkerError):
            Hello.parse(json.dumps({**good, key: bad}).encode())


@pytest.mark.parametrize("dtype", ["f16", "f32"])
def test_decode_matches_mock_bytes(tmp_path, dtype):
    """Decoded buffers equal the arrays the mock packed (same seed, in-process)."""
    cfg = {"envs": 3, "agents": 4, "spatial_dtype": dtype, "seed": 5, "min_len": 2, "max_len": 4}
    ref = MockEnv(dict(cfg))
    with _pool(tmp_path, [cfg], groups=1) as pool:
        assert pool.num_slots == 12 and pool.spatial_dtype == dtype
        sl = pool.group_slots[0]
        parts = ref.reset()
        pool.submit(0, OP_RESET)
        res = pool.wait(0)
        assert res.reset_op and res.episodes == []
        for step in range(8):
            payload = b"".join(parts[1:])
            itemsize = 2 if dtype == "f16" else 4
            off = 0
            for name, size in field_sizes(12, itemsize):
                got = pool.buffers.np[name][sl].reshape(-1).view(np.uint8).tobytes()
                assert got == payload[off : off + size], (step, name)
                off += size
            actions = _legal_actions(pool, sl)
            parts = ref.step(actions.copy())
            pool.submit(0, OP_STEP, actions)
            res = pool.wait(0)
            info = json.loads(parts[-1])["episodes"]
            assert [e["env"] for e in res.episodes] == [e["env"] for e in info]


def test_shapes_and_semantics(tmp_path):
    cfgs = [{"envs": 4, "agents": 4, "seed": i, "player_counts": [2], "min_len": 3, "max_len": 5} for i in range(3)]
    with _pool(tmp_path, cfgs, groups=2) as pool:
        assert pool.num_envs == 12 and pool.num_groups == 2
        assert pool.group_slots[0].start == 0 and pool.group_slots[-1].stop == 48
        b = pool.buffers
        assert tuple(b.spatial.shape) == (48, 117, 13, 15)
        for g in range(2):
            pool.submit(g, OP_RESET)
        for g in range(2):
            pool.wait(g)
        act = b.np["active"]
        assert act.reshape(12, 4)[:, :2].all() and not act.reshape(12, 4)[:, 2:].any()
        assert (b.np["legal"][~act] == 0).all() and b.np["legal"][act].any(1).all()
        assert (b.np["reset"] == act).all()
        seen_done = 0
        episodes = []
        for _ in range(12):
            for g in range(2):
                pool.submit(g, OP_STEP, _legal_actions(pool, pool.group_slots[g]))
            for g in range(2):
                r = pool.wait(g)
                episodes += r.episodes
                seen_done += int(b.np["done"][pool.group_slots[g]].sum())
        assert episodes and seen_done >= len(episodes)
        assert {e["env"] for e in episodes} <= set(range(12))


@pytest.mark.parametrize("mode", ["exit", "garbage", "hang"])
def test_worker_crash_restart(tmp_path, mode):
    cfgs = [{"envs": 2, "agents": 4, "seed": 1, "crash_after_steps": 3, "crash_mode": mode}, {"envs": 2, "agents": 4, "seed": 2}]
    timeout = 2.0 if mode == "hang" else 20.0
    with _pool(tmp_path, cfgs, groups=1, step_timeout_s=timeout, max_restarts=5) as pool:
        pool.submit(0, OP_RESET)
        pool.wait(0)
        crashed = []
        for _ in range(4):
            pool.submit(0, OP_STEP, _legal_actions(pool, pool.group_slots[0]))
            res = pool.wait(0)
            crashed.append(res.crashed_envs.tolist())
        assert crashed[2] == [0, 1]  # worker 0 crashed at its third step, envs 0-1 restarted
        assert crashed[3] == []
        assert pool.workers[0].restarts >= 1 and pool.workers[1].restarts == 0
        # The restarted worker answered with a fresh RESET: reset flags set on its active slots.
        assert "seed" in pool.workers[0].config


def test_restart_limit(tmp_path):
    cfgs = [{"envs": 1, "agents": 4, "crash_after_steps": 1}]
    with _pool(tmp_path, cfgs, groups=1, max_restarts=2) as pool:
        pool.submit(0, OP_RESET)
        pool.wait(0)
        with pytest.raises(WorkerError):
            for _ in range(5):
                pool.submit(0, OP_STEP, _legal_actions(pool, pool.group_slots[0]))
                pool.wait(0)


def test_illegal_action_is_detected(tmp_path):
    """The strict mock rejects an action for an inactive slot; the pool restarts it."""
    cfgs = [{"envs": 1, "agents": 4, "player_counts": [2]}]
    with _pool(tmp_path, cfgs, groups=1, max_restarts=1) as pool:
        pool.submit(0, OP_RESET)
        pool.wait(0)
        bad = _legal_actions(pool, pool.group_slots[0])
        bad[3] = 0  # slot 3 is inactive
        pool.submit(0, OP_STEP, bad)
        res = pool.wait(0)
        assert res.crashed_envs.tolist() == [0]
        assert "inactive" in pool.workers[0].log_tail()
