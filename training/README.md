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
