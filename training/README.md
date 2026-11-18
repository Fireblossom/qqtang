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

## Worker

`cmd/qqt-train-worker` implements the protocol above on top of
`internal/game/battleenv` (the vectorized environment restored from
`3c72bf2^`, ported to today's engine and tensor schema 18). Rule 1 only.

### Build and run

```sh
training/tools/build-worker.sh        # GO=/path/to/go if go is not on PATH
# -> training/bin/qqt-train-worker-darwin-arm64, training/bin/qqt-train-worker-linux-amd64 (static)
training/bin/qqt-train-worker-darwin-arm64 -client-root <client-patched> -config worker.json
training/bin/qqt-train-worker-darwin-arm64 -client-root <client-patched> -config worker.json -bench 1000  # in-process throughput
python3 training/tools/worker_client_smoke.py --worker training/bin/qqt-train-worker-darwin-arm64 \
    --client-root <client-patched> --envs 64 --steps 300          # protocol check + throughput (numpy only)
```

On the cluster copy `qqt-train-worker-linux-amd64` and the client's map data
(the worker reads only the map catalog of `-client-root`). The configuration
schema with every key and default is the comment on `Config` in
`cmd/qqt-train-worker/config.go`; unknown keys are rejected. Defaults: 64
envs, `player_counts` [2, 4] (agents = 4), two equal teams, every eligible map
(170 rule-1 maps fit 13x15), no curriculum, historical reward weights, f16.

### Protocol details and deviations

These fill in what the spec above leaves open; nothing in the binary layout
differs.

* `active` = the slot decides now. Inactive slots (padding, eliminated
  actors, and with the server cadence actors still holding) have all-zero
  spatial, scalar and legal rows; their action value is ignored (send -1).
* `reward` is everything paid to the slot since the previous response,
  including on inactive slots: an eliminated actor still receives its team's
  terminal reward when the episode ends. Add it to the slot's last decision.
* `done` is set on every real slot of the finished episode (also eliminated
  ones); the same row then already carries the next episode, flagged by
  `reset` (and `team`, which may change: the player count is redrawn).
* The hello line has extra keys: `rule`, `maps`, `decision_ms`,
  `decision_cadence`.
* Episode info: `{"episodes": [{"env", "map", "rule", "curriculum", "length"
  (STEPs), "ticks", "ended_at_ms", "outcome" ("win"/"draw"), "winner_team",
  "timed_out", "teams": [{"team", "result", "size", "alive"}], "agents":
  [{"slot", "team", "return", "survived", <battleenv.AgentMetrics>}]}]}`;
  `return` is the undiscounted sum of the slot's rewards.
* Observations are bit-identical to the server's live inference input for
  the same state (`TestWorkerObservationMatchesServerInference`: every live
  actor, every decision, all eligible maps): observation from the engine;
  danger timeline (3500 ms), tactical consequences (100 ms decision) and
  legal mask from the actor's `Engine.PolicySnapshot`, as
  `battleai.NeuralCandidates` does.
* Decision cadence. Default `"fixed"` is the spec: every live actor decides
  every STEP and the action is held 5 ticks (bomb/item pulse on the first).
  The live server instead re-decides an actor on the next 20 ms tick when its
  held move turns illegal or `Engine.NeedsImmediatePolicyDecision` fires
  (native wall pass, a blast within 400 ms); in random play about 1 in 5
  decisions is such an early one. `"decision_cadence": "server"` reproduces
  that exactly (`TestServerCadenceMatchesLiveDecisionCadence` replays it
  against a port of the server's cadence policy): a STEP then advances each
  environment only to its next due actor (1..5 ticks, listed in the info
  JSON as `"ticks"`), and only due actors are active; `random_phase` adds the
  server's 0..4-tick per-player start phase. Recurrent memory then advances
  per decision, as in deployment.
* Not reproduced from the live server: the runtime's joint-bubble guard
  (`Runtime.suppressMarginallyUnsafeVirtualBombs`, which cancels a placement
  of a server AI that would trap itself together with another AI's bubble in
  the same tick), and everything human-specific (arbitrator confirmations,
  recorded GAME_BEGIN items; the worker rolls items from `ItemSeed` like an
  offline engine).
* Rule-1 spawns use the client's interleaved table also on two-group maps
  (a432672); `battleenv.Config.TeamSpawnPermille` therefore has no effect
  for rule 1.

### Rewards and curricula

`battleenv.RewardWeights` (JSON key `rewards`, keys in snake case) holds every
coefficient of the original design: terminal outcome normalized by the
squared initial team sizes and scaled up for early decisions; reversible trap
credit against other teams (rolled back on rescue), trap assist pool,
elimination, debuff, transformation, rescue budget; finite development
shaping (walls, pickups); enemy-approach potential; late-round anti-stall and
blocked-cell shaping. `terminal_scale` and `shaping_scale` scale the two
parts. Curriculum shares (`curriculum`: duel, blockade, chain_finisher,
bomb_escape, development, late_duel_replay) are fractions of resets and apply
to 2-player episodes.

### Throughput (Apple M4 Max, 16 cores, while two other agents were working)

Random legal actions (episodes are short: random players eliminate
themselves), default config unless noted. Bytes per STEP response =
E·P·(46,431 f16 | 92,061 f32) + 4 + info; the STEP request is 2·E·P + 9.

| Setup | decisions/s |
| --- | --- |
| in-process `-bench`, E=64 P=4 (2/4 mix), f16 / f32 | 114k-145k / 168k |
| in-process, E=256 P=4, f16 | 144k |
| in-process, E=64 P=8 (2/4/8 mix), f16 | 150k |
| in-process, E=64, server cadence (+random phase), f16 | 90k (fewer decisions per step) |
| Python client, 1 process GOMAXPROCS=16, E=64 P=4, f16 / f32 | 57k / 37k |
| Python client, 2 / 4 / 8 processes (GOMAXPROCS 8 / 4 / 2), f16 | 82k / 100k / 125k |
| Python client, 4 processes, f32 | 79k |
| Python client, 4 processes E=32 P=8 (8 players) / (2/4/8 mix), f16 | 114k / 95k |

A single process is limited by the stdout pipe (about 4 GB/s through 64 KiB
pipe writes on macOS), not by simulation; run several workers per trainer
(e.g. 4-8 per 16 cores) and prefer f16. Shared memory would remove this
limit if it ever matters.

### Special rules (not implemented)

`battleenv.RuleModule` / `RuleEpisode` (internal/game/battleenv/rules.go) is
the extension point: map eligibility, engine options before
`ConfigFromCompetitiveMap`, per-tick rule logic and a rule-decided outcome.
Today the special rules' logic lives in `cmd/qqt-battle-sidecar` (online
arbitrator code driven by client messages). Every special rule needs, besides
that port: `AllowSpecialRules` plus its `battleengine.Rules` switches (see
`ruleOptions` in `cmd/qqt-battle-sidecar/online.go`), an offline generator for
the GAME_BEGIN item lists that the sidecar records from the server, the
two-team spawn consumer, tensor channels/scalars for its objects and scores
(schema 18 has none, so a new tensor schema and model contract), its own
reward terms, and server support for AI in that rule (the server fills AI
only on rule-1 no-item maps).

| Rule | What the module must provide |
| --- | --- |
| 2 kick-bomb | no own bubbles, universal wrapping kick, 2 px corner slide; the arbitrator's airdrop schedule (kickbomb.go) and kick requests; elimination outcome |
| 3 bun | two houses (element 8009) per team, bun take/put-down requests, carrier death handoff/loose drop, relive after 10 s, score conclusion |
| 4 wrestle | kill-count score, relive after 5 s when the spawn is free, death-drop recycling; skills are not ported anywhere |
| 5 treasure | gems as objective scene IDs, death drops, departure recycle, relive after 5 s, gem-score conclusion |
| 6 sculpture | statues (element 12008), fragments 161-165 as loose objects, item 66 put-down, deposit requests, relive after 5 s, score conclusion |
| 7 machine | team launchers with shot patterns (machine.go), machine supply, durability layers, no bubbles, external conclusion |
| 8 box | box physics and pushing (box*.go), box NPC AI, durability layers, no bubbles, external conclusion |
| 13 tank | two bases (element 19031) with HP, repair/armor pickups, relive after 5 s, base-destroyed conclusion |
| 12 adventure | PvE: decrypted level data, NPC controllers/skills and bosses (adventure*.go, npc_*.go, boss_*.go), co-op outcome; a different task altogether |

## Trainer

Recurrent PPO with a self-play league, in `training/qqtrain/` (everything
except `model.py`, `export.py`, `synthetic.py`, `dump.py`):

| File | Role |
| --- | --- |
| `worker.py` | Worker processes and the binary protocol client |
| `mock_worker.py` | Pure-Python worker with the same protocol (tests, smoke runs) |
| `rollout.py` | Storage, per-slot transitions, GAE, BPTT chunks, the collector |
| `ppo.py` | PPO loss and update |
| `league.py` | Opponent pool, PFSP sampling, team assignment, result parsing |
| `evaluate.py` | Fixed-seed matches, confidence intervals, Elo, best-policy export |
| `checkpoint.py` | Checkpoints, metrics (JSON lines, optional TensorBoard) |
| `policy_api.py` | Adapter to `QQTPolicy`, samplers, stand-in networks |
| `config.py`, `train.py` | TOML configs, CLI, training loop |

### Running

```sh
cd training
uv run python -m qqtrain.train --config configs/smoke.toml      # mock worker, stand-in net, CPU, 2 min
uv run python -m qqtrain.train --config configs/mac.toml        # real worker + QQTPolicy (U2392 init), MPS, 3 min
uv run python -m qqtrain.train --config configs/mac.toml --set model.init_from_baseline=false   # from scratch
uv run python -m qqtrain.train --config configs/h100.toml       # cluster (see below)
uv run python -m qqtrain.evaluate --config configs/h100.toml --checkpoint RUN/checkpoints/ckpt_000500.pt
uv run pytest tests/test_trainer_*.py
```

Every key can be overridden with `--set section.key=value` (TOML value).
`worker.config` and `eval.modes.<name>.worker_config` are passed unchanged to
the worker as its JSON config (schema in `cmd/qqt-train-worker/config.go`);
the trainer only sets `seed` per worker (`run.seed * 1000003 + i * 1000`,
shifted after a restart or resume). The worker executable defaults to
`bin/qqt-train-worker-{platform}` (built by `tools/build-worker.sh`).
`tests/test_trainer_integration.py::test_real_worker` runs the trainer
against that binary when it and the client data (`QQT_CLIENT_ROOT`, default
`~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched`) exist.

The run directory holds `train.log`, `metrics.jsonl` (one record per
update), `eval.jsonl`, `checkpoints/ckpt_NNNNNN.pt` (model, optimizer, league,
evaluator table, RNG states, counters, config), `league/snap_*.pt`,
`export/` (best policy: `best_policy.pt`, `best.onnx` + `best.onnx.json` via
`export_onnx`, `best_eval.json`) and `workers/` (per-worker JSON config and
stderr log). With `run.resume = "auto"` the same command continues from the
latest checkpoint; SIGTERM/SIGINT finish the update, checkpoint and exit.

### Rollout pipeline

* `WorkerPool` starts `worker.num_workers` processes and splits them into
  `worker.groups` (2) pipelining groups. One I/O thread per worker reads each
  response with `readinto` straight into that worker's slice of shared
  observation buffers (`[all slots, ...]` torch tensors; 1 MiB pipes on
  Linux). Per step and group: wait for its observations, gather the active
  rows into (pinned) staging buffers, run the learner and the opponents on the
  GPU, store the learner entries, send the actions; the other group's workers
  simulate meanwhile. Forward batches are padded to a few bucket sizes (64,
  96, 128, 192, ...), also the PPO minibatches: per-step varying batch sizes
  made MPS recompile its graphs (~150 ms per forward, 6x slower collection),
  and the same holds for cuDNN autotuning and `torch.compile`. The first step of
  the next rollout is sent before the PPO update, so workers also run during
  the update (its actions carry the old policy's log-probabilities).
* A worker that exits, hangs (`worker.step_timeout_s`), writes a bad magic or
  malformed payload, or rejects a request is killed, restarted with a new seed
  and RESET; its pending transitions are truncated (bootstrapped with their
  own value) and its environments start new episodes. More than
  `worker.max_restarts` restarts of one worker abort the run.
* Storage is `[T+2, slots]` on the GPU (spatial f16). A transition is per slot,
  from one of its decisions to its next one; entries exist only for active,
  learner-controlled slots. Rewards that arrive while a slot is inactive (an
  eliminated agent's terminal win/loss, or between decisions with the server
  cadence) go to its last decision. Each environment has a clock in 100 ms
  units, advanced by 5 ticks per STEP with the fixed cadence and by the
  worker's per-env `ticks` with the server cadence, and the discount between
  two decisions of a slot is `gamma ** (clock difference)`, so STEPs of
  unequal length and holding agents are handled. A decision still waiting for its reward
  at the end of a rollout (agent eliminated, episode running) is carried into
  row 0 of the next rollout instead of being trained with a wrong target. The
  GRU memory advances only on the slot's own decisions and is cleared on
  `reset` or `done`, as on the server.

### PPO

Clipped surrogate (0.2), clipped value loss (max of clipped/unclipped
squared error), entropy bonus over legal actions (linearly decayed),
advantage normalisation over the rollout, approximate-KL early stop
(`ppo.target_kl`), AdamW with global gradient clipping, warm-up plus cosine
LR schedule driven by `agent_steps / total_agent_steps`. Truncated BPTT:
each slot's decisions are cut into chunks of `ppo.seq_len`, replayed with
`QQTPolicy.forward_sequence` from the memory recorded at the chunk start
(stored state, no burn-in) with the stored reset flags. bf16 autocast and
TF32 on CUDA; `model.compile` wraps the sequence forward in `torch.compile`
(not yet tried on CUDA). `model.init_from_baseline` starts from U2392 and
`ppo.value_warmup_updates` first trains only the new value head (policy loss
and entropy off, no KL stop; the shared trunk still receives value
gradients).

Sampling: the learner samples from the full masked softmax (temperature 1),
so the stored log-probabilities are exactly the behaviour policy. The server
decodes differently (`battleai.SampledCandidatePolicy`): the best action is
always eligible, others only if `exp(logit - best) >= min_p` (0.1) and they
keep a refuge according to `scalars[162:167]`, `scalars[172:177]` and
`scalars[95]` (item actions always). `policy_api.deployed_sample` reproduces
that decoder; frozen league members and all evaluation games use it, so U2392
plays as deployed and evaluation measures the deployed behaviour.

### League

Pool: `latest` (the learner itself), `baseline` (U2392,
`QQTPolicy.from_onnx(baseline.onnx)`), snapshots every
`league.snapshot_every` updates (oldest evicted beyond `max_snapshots`).
Each environment draws its opponent when an episode starts: `latest` with
`self_play_prob`, otherwise a frozen member with PFSP weight
`(1 - score) ** pfsp_power` (score = the learner's decayed, smoothed score
against it), the baseline keeping at least `baseline_prob` of those games.
`resident` frozen members are drawn per rollout and kept on the device, so a
step runs at most `1 + resident` networks, each batched over its slots. Roles
are per team: against a frozen member one team (uniformly chosen) is the
learner and the others the opponent; against `latest` every team is trained
(`train_both_sides`). Only learner entries are stored and trained. Results
come from the worker's episode info (`teams[].result`, or `outcome` /
`winner_team`).

### Evaluation

Every `eval.every` updates, for each mode in `eval.modes` (1v1, 2v2) and
each opponent (`baseline`, the last N snapshots): a fresh pool with the mode's
worker config (curriculum removed) and a fixed seed, so every opponent gets
the same starting states; each environment plays a fixed number of episodes
and the learner side alternates between teams. Reported: W/D/L, win rate with
a Wilson 95 % interval, score with a normal 95 % interval, mean length; the
accumulated match table gives Bradley-Terry ratings on the Elo scale with
U2392 anchored at 1000. A new best mean score against U2392 saves
`export/best_policy.pt` and exports `export/best.onnx` (+ `.json`).
Training pauses during evaluation.

### H100 configuration (`configs/h100.toml`)

The comments in the file give the arithmetic. In short: 8 worker processes
(`GOMAXPROCS=4`) x 64 environments x 4 slots, 2 groups, server decision
cadence with random phase; `T=128`, chunks of 32, 3 epochs x 8 minibatches
(~16k samples each); gamma 0.995 per 100 ms (~20 s horizon), lambda 0.95;
LR 2.5e-4 -> 2.5e-5; start from U2392 with 30 value warm-up updates; league
50 % self-play, at least 30 % U2392 among the rest. The worker section above
measured 100k-125k decisions/s into one Python process with 4-8 workers;
every env step ships 182 KB (four f16 slots, empty slots of 2-player
episodes included), so the observation stream rather than the core count
bounds useful workers. Rollout storage is ~12 GB. Check
`learner_decisions_per_s`, `collect_s`, `worker_wait_s` and `update_s` in
`metrics.jsonl` on the first run and adjust workers/epochs.

### Cluster (42, `ssh 42`)

1. On the Mac: `training/scripts/push_to_cluster.sh` builds the Linux worker
   (`tools/build-worker.sh`) and rsyncs `training/` (with the binary),
   `configs/models/` and the client data the worker reads (`map/`, `config/`,
   `object/mapElem/` of `runtime/client-patched`, plus
   `data/qqt_competitive_map_rules.json`) to `/mlde/qqtang-rl/`.
2. In the container: `bash /mlde/qqtang-rl/repo/training/scripts/cluster_run.sh`
   installs uv if needed, `uv sync`s into `/mlde/qqtang-rl/venv` (cache on
   /mlde), picks `min(config, (nproc - 4) / 4)` workers and starts training in tmux
   session `qqtrain-$RUN` (nohup fallback), everything under
   `/mlde/qqtang-rl/runs/$RUN`. `--status` and `--stop` (SIGTERM, checkpoint)
   are included. After the container is reclaimed, re-running the script
   resumes from the last checkpoint. Neither script has been run against the
   cluster yet.

### Interface notes and deviations

* Response `payload bytes`: the client accepts the size with or without the
  info block (the worker counts it).
* The episode info is read for `env`, `length` and the result (`teams` list
  or map, else `outcome`/`winner_team`/`draw`); other fields are logged only.
* Memory is also cleared after `done` (not only on `reset`), and an active
  slot with an all-zero legal mask gets "wait" made legal (counted as
  `empty_legal`) instead of crashing the sampler.
* Team IDs index a 256-entry mask, so any `u8` team value works; roles are
  reassigned at every new episode (player counts and teams are redrawn).
* Both decision cadences work; the training configs use `"server"` (what the
  deployed model experiences), `"fixed"` stays available.
* `StandInPolicy` remains for CPU tests and the mock smoke run (QQTPolicy
  trains at ~400 samples/s on CPU); everything else uses `QQTPolicy`.
