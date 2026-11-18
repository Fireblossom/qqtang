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

## Model

`training/qqtrain/model.py` is a node-by-node reconstruction of U2392's ONNX
graph. `QQTPolicy.from_onnx("configs/models/qqtang-rule1.onnx")` loads all
137 initializers (3,241,805 parameters); the 31 whose names the exporter
dropped (`onnx::MatMul_*`, `onnx::Add_*`, `onnx::Expand_*`) are placed by
graph position: each transposed Linear weight by the named bias of the Add
that follows its MatMul, the three action embeddings by their width.

Setup (from `training/`): `uv sync` (add `--system-certs` behind a TLS
proxy). On Linux the lock takes torch 2.11.0+cu126 from the PyTorch index
(PyPI's Linux wheels are CUDA 13 since 2.11); macOS uses the PyPI wheel.
torch is pinned to 2.11 because U2392 was written by its TorchScript ONNX
exporter, which `export_onnx` reuses. Tests: `uv run pytest`.

### Interface

```python
from qqtrain.model import QQTPolicy
from qqtrain.export import export_onnx

policy = QQTPolicy.from_onnx("configs/models/qqtang-rule1.onnx")   # or QQTPolicy() from scratch
logits, value, next_memory = policy(spatial, scalars, legal, memory, reset)
# rollout segments for recurrent PPO, time-major; memory0 is the state before step 0
logits, values, last_memory = policy.forward_sequence(spatial_TB, scalars_TB, legal_TB, memory0_B, reset_TB)
export_onnx(policy, "runs/x/qqtang-rule1.onnx", {"sampling": {"enabled": True, "min_p": 0.1, "temperature": 1.0}})
```

* `spatial` may be float16 (the worker's `f16` stream); it is cast to
  float32. `legal` and `reset` are bool, `reset[t]` clears the memory before
  step `t`.
* Deviation from the interface above: illegal logits are
  `torch.finfo(float32).min` (-3.4028235e38), not `-inf`. This is exactly what
  the deployed graph does (`Where(Not(legal), -3.4028234663852886e38, logits)`)
  and keeps `log_softmax`, entropy and KL free of NaN. Logits are float32 even
  under autocast.
* `forward_sequence` runs the two CNN encoders and all heads once on the
  flattened `T*B` batch; only the GRU cell loops over `T`. It equals
  step-by-step `forward` (tested).
* `value_head` (192 -> 128 -> SiLU -> 1 on the temporal feature, last layer
  initialised near zero) is the only addition. It is outside the `model.`
  prefix, never exported, and starts untrained when loading U2392: warm it up
  (or freeze the policy for the first updates) before PPO changes the policy.
* `export_onnx(policy, path, metadata=None)` writes the deployment graph
  (TorchScript exporter, opset 18, dynamic `batch`, same input/output names and
  dtypes as U2392, no value) and `path + ".json"` in the format of
  `configs/models/qqtang-rule1.onnx.json`, with `onnx_sha256` and the parity
  errors (max/mean |PyTorch - ONNX Runtime| over legal logits of 256 synthetic
  observations). `metadata` overrides fields such as `sampling`.
  CLI: `uv run python -m qqtrain.export SOURCE.onnx|SOURCE.pt OUT.onnx [--min-p P]`.
* `training/tools/check-onnx-go.sh OUT.onnx` loads an export through the
  server's Go ONNX Runtime path (`internal/game/battleai`, `-tags onnxruntime`).
* `training/tools/dump_observations` (Go) plays battles on real maps through
  the server's inference path and records the exact tensors handed to the
  actor (and, with `-tags onnxruntime -model ...`, Go ONNX Runtime's outputs;
  `-compare other.onnx` evaluates a second model on the same inputs).
  `tests/data/real_observations.npz` is a 96-record excerpt.

### Graph details the architecture doc leaves out

Recovered from the ONNX nodes; all are reproduced in `model.py`.

* Relative coordinates: the self mask is `spatial[7:9].sum > 0`; its mean
  row/column in `linspace(-1,1)` coordinates (count clipped to >= 1, so no self
  cell gives absolute coordinates) is subtracted from the row/column grids.
  The graph's linspace constants are exactly symmetric; `torch.linspace` on
  arm64 differs by up to 4e-8, so the constants are hard-coded.
* Behaviour router input (8): the mean of channels 64..71 (public behaviour
  rates) over cells occupied by enemies (channels 11..12).
* Multiplayer gate (doc: "public team/player-count state"): 1 if any ally
  plane (channels 9..10) is non-zero or more than one cell holds an enemy
  (channels 11..12); it multiplies both `multiplayer_residual` and the
  separate context encoder's `multiplayer_context_actor` (which reads the
  context encoder's 192-dim output directly, not the temporal feature).
* Movement candidate of each joint action: 0 for wait/bubble/item-only, 1..4
  for up/right/down/left (also for move+bubble and move+item). Bubble intent is
  1 only for actions 5..9; item intent is 1..7 for the item actions.
* 24-dim candidate score: temporal (192->24) + all 187 scalars (187->24) +
  the mean of the 117 raw channels at the candidate cell (self, or the
  neighbour in the action's direction; 117->24) + 14 direction facts (scalars
  112..136 current reach, 137..161 reach after a bubble, 162..181 refuge
  found/time now and after a bubble, for the action's direction; 14->24) +
  a 45x24 action embedding, then SiLU and 24->1.
* 32 consequence features shared by the 48- and 96-dim scores, per action:
  reach (5) and refuge (2) taken from the bubble tables for actions 5..9 and
  from the current tables otherwise (exported as `lerp` with constant 0/1
  weights), the current reach (5) and refuge (2) again, the bubble flag (1),
  and scalars 94..110 (bubble-placement facts) times the bubble flag (17).
* 96-dim interaction: concat(temporal->96, consequences->96, 45x96 action
  embedding) -> 288->128 SiLU -> 128->96 SiLU -> 96->1.
* Logit sum order: actor + router-weighted experts + gated multiplayer
  residual + gated context actor + tactical + 24-dim + 48-dim + 96-dim +
  (direction + bubble + item factors), then masking.
* U2392 was traced with a `[batch, time=1]` axis (inputs unsqueezed at dim 1),
  which is why its Linear layers on 3-D activations lost their names; the
  re-export has no time axis (550 instead of 1,008 nodes, same initializers).

### Verification (Apple M4 Max, onnxruntime 1.32 CPU, Go ORT 1.29)

| Check | max abs logit error | mean | next_memory |
| --- | --- | --- | --- |
| PyTorch vs U2392, synthetic batches 1/7/256 | 8.6e-6 | 1.3e-6 | 1.9e-6 |
| PyTorch vs Go ORT (U2392), 6,833 real decisions on 4 maps, per call | 1.3e-5 | 1.8e-6 | 4.7e-6 |
| same, memory chained in PyTorch over 16 streams (~430 steps) | 1.5e-5 | | |
| PyTorch on MPS vs Go ORT, 96 real decisions | 8.6e-6 | | 4.1e-6 |
| re-exported U2392 vs U2392 (Python ORT, synthetic + 20-step chain) | 5.7e-6 | 7e-7 | 1.2e-6 |
| re-exported U2392 vs U2392 in Go on 6,833 real decisions | 7.6e-6 | | 3.2e-6, 0 argmax changes |

U2392's own metadata states `maximum_parity_error` 1.0e-5. Masked logits are
bit-identical in every case. The Go loader tests
(`TestONNXRuntimeRunnerLoadsContractAndRuns`,
`TestDeploymentPolicyUsesRequestedONNXRuntimeBackend`) pass on the export.

Under bfloat16 autocast the logits move by up to 0.1 (argmax unchanged on the
real excerpt); logits, values and next_memory are returned as float32.

### Throughput

`training/tools/bench_model.py` (one PPO-like step: `forward_sequence` on
`T=16` segments, policy/value/entropy loss, backward, Adam), samples/s:

| Device | batch 1024 | batch 4096 |
| --- | --- | --- |
| M4 Max CPU (16 cores), fp32 | 431 | 432 |
| M4 Max MPS, fp32 | 9,900 | 10,000 |
| M4 Max MPS, bf16 autocast | | 16,400 |
| H100, bf16 autocast (estimate, not measured) | | 80,000-200,000 |

Inference only (`torch.no_grad`, one decision): CPU 1,400-1,500/s, MPS
57,000-60,000/s. The forward pass is about 57 MFLOP per decision (two
119-channel CNN encoders dominate; the 45-candidate heads add about 5 MFLOP),
so fwd+bwd is about 170 MFLOP. The H100 figure assumes 10-25% of bf16 dense
throughput for these small convolutions; measure with
`bench_model.py --device cuda --bf16 [--compile]` on the node.

On CPU the dilated depthwise convolutions use nine shifted multiply-adds
instead of `nn.Conv2d` (PyTorch's CPU `slow_conv_dilated2d` made a training
step 9x slower); CUDA, MPS and ONNX export use the convolution itself.
