# QQTang reinforcement-learning training

Self-play training for the battle policy that the server runs through ONNX
(`configs/models/qqtang-rule1.onnx`, model U2392, see
`docs/onnx-network-architecture.md`). Ordinary rule 1 comes first; the other
rules are added after the stack reproduces and beats U2392.

The stack has three parts that only meet at the interfaces below:

| Part | Where | Role |
| --- | --- | --- |
| Environment worker | `cmd/qqt-train-worker` (Go, uses `internal/game/battleenv`) | Runs many battles headlessly; encodes observations exactly like the server (`battleenv.EncodeActorAtDecision`, tensor version 18); applies actions; computes rewards |
| Policy network | `training/qqtrain/model.py` | PyTorch `unified-candidate-v1` with an added value head; loads U2392 from ONNX; exports the deployment ONNX contract v2 |
| Trainer | `training/qqtrain/` | Recurrent PPO self-play with a league (snapshots + frozen U2392), multi-process rollouts, evaluation, checkpoints |

Target hardware: one NVIDIA H100 node (Linux x86-64) on the hessian.AI 42
cluster, reached with `ssh 42`; local development and smoke runs on an Apple
Silicon Mac (CPU/MPS). The Go worker is pure Go and is cross-compiled
(`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`), so the cluster needs no Go
toolchain. Python dependencies are managed with `uv` (`training/pyproject.toml`).

## Fixed dimensions (deployment contract v2)

| Name | Value |
| --- | --- |
| Spatial | `[117, 13, 15]` float32 (channels, rows, columns) |
| Scalars | `187` float32 |
| Actions | `45` (0 wait, 1-4 up/right/down/left, 5 bomb, 6-9 move+bomb, 10-44 held-item actions, see the architecture doc) |
| Recurrent memory | `256` float32, reset at episode start |
| Decision interval | 100 ms (5 engine ticks of 20 ms); the chosen action is held between decisions as the server does |

## Worker protocol (stdin/stdout)

One worker process owns `E` environments with `P` agent slots each (`P` is
the fixed tensor capacity; unused slots are inactive). All integers and floats
are little-endian. The worker writes nothing else to stdout; logs go to stderr.

### Start

The worker is started as
`qqt-train-worker -client-root <dir> -config <json file>` and first writes one
line of JSON, then the binary stream begins:

```json
{"protocol": 1, "envs": 64, "agents": 4, "channels": 117, "height": 13, "width": 15,
 "scalars": 187, "actions": 45, "tensor_version": 18, "spatial_dtype": "f16"}
```

The config JSON selects maps, player counts, team layouts, rule, curriculum
shares, reward weights, seed and `spatial_dtype` (`f16` or `f32`). Its schema
is defined in `cmd/qqt-train-worker/config.go` and documented there.

### Requests (Python -> worker)

Header: `u32 magic 0x57545151 ("QQTW")`, `u8 op`, `u32 payload bytes`.

| op | Payload | Meaning |
| --- | --- | --- |
| 1 RESET | none | Reset every environment, return observations |
| 2 STEP | `i16[E*P]` actions | Apply one decision for every active agent (`-1` for inactive slots), advance 100 ms, return observations. Environments that ended during the step are reset automatically before observing |
| 3 CLOSE | none | Exit |

### Response (worker -> Python), after RESET and STEP

Header `u32 magic 0x52545151 ("QQTR")`, `u32 payload bytes`, then in order:

| Field | Type / shape | Meaning |
| --- | --- | --- |
| spatial | `f16 or f32 [E,P,117,13,15]` | Observation for the next decision |
| scalars | `f32 [E,P,187]` | |
| legal | `u8 [E,P,45]` | Legal action mask (all zero for inactive slots) |
| active | `u8 [E,P]` | 1 if this slot must choose an action now |
| reset | `u8 [E,P]` | 1 if the slot's recurrent memory must be cleared before this decision (new episode) |
| team | `u8 [E,P]` | Team ID, for league assignment and evaluation |
| reward | `f32 [E,P]` | Reward earned since the previous decision (0 after RESET) |
| done | `u8 [E,P]` | 1 if the slot's previous episode ended during this step (its reward includes the terminal reward); the observation is already from the new episode |
| info length | `u32` | Byte length of the following JSON |
| info | UTF-8 JSON | `{"episodes": [...]}` with one entry per episode finished during this step: env index, map, rule, length, outcome, per-team result, per-agent metrics |

Rewards follow the old batch environment (`git show 3c72bf2^:internal/game/battleenv/batch.go`):
terminal win/loss/draw plus small, reversible shaping for traps and
eliminations against other teams; weights come from the config.

## Model interface (Python)

```python
logits, value, next_memory = policy(spatial, scalars, legal, memory, reset)
# spatial [B,117,13,15] float, scalars [B,187], legal [B,45] bool,
# memory [B,256], reset [B] bool  ->  logits [B,45] (illegal = -inf), value [B], next_memory [B,256]
```

`export_onnx(policy, path)` writes the deployment graph (inputs `spatial`,
`scalars`, `legal`, `memory`, `reset`; outputs `logits`, `next_memory`; no
value) plus the metadata JSON in the format of
`configs/models/qqtang-rule1.onnx.json`.

## Status

See the sections each part adds below as it lands.
