# QQTang battle engine, map data and rendering: notes for the Python native client

Repo: `/Users/cduan/qqtang` (Go module `qqtang`). Client assets: `~/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/client-patched` (called `$R` below).
No repo files were modified. Prototype tools live in `/tmp/qqt-tools/` and are built with `go build -overlay`, which adds a virtual `cmd/...` package without touching the tree.

> Update: the sidecar now lives in the repo as `cmd/qqt-battle-sidecar` (built by `client/native/tools/build-sidecar.sh`); the `/tmp/qqt-tools` paths below refer to the original prototype.

Status of claims: **[code]** means read in source. **[verified]** means I ran it against the real client data. **[inferred]** means deduced from assets only and still needs a screenshot or RE check.

---

## 0. TL;DR

* `internal/game/battleengine` is a pure, deterministic, single-threaded rule core. It has no sockets, clocks or goroutines (`engine.go:8-11`). It imports only `mapdata`, `rolecatalog` and `internal/clientdata/sceneelement`. There is **no coupling to `internal/server/probe`**, and probe is the only consumer.
* One call builds a full rule-1 match from the client's own files: `mapdata.LoadCatalog(clientRoot)` -> `catalog.CompetitiveMap(mapID)` -> `battleengine.NewRuntimeFromCompetitiveMap(entry, CompetitiveRuntimeOptions{...})`. Then call `runtime.Step([]Action)` every `TickMS`.
* **A working JSON-lines sidecar prototype is already done and tested**: `/tmp/qqt-tools/sidecar/main.go` (binary `qqt-battle-sidecar`). On map 211 (景阳岗, 15x13) it ran 1500 ticks of 20 ms. It produced bombs, explosions, flames, destroyed walls, revealed pickups, a trap and a kill, with team 2 winning [verified].
* Limitation: the engine only supports **native rule 1 ("ordinary" bubble battle)**. Maps of rule 2 (kick-bomb), 3 (bun), 4 (wrestle/`match`), box, tank, treasure, etc. are rejected by `ValidateNativeRuntimeMap` (`map_adapter.go:54`). Example: map 1007 fails with "uses native rule 4/wrestle, want ordinary rule 1".
* Map element sprites are `object/mapElem/<family>/elem<id%1000>_{stand,die,trigger}.img`, where `family = id/1000` comes from a fixed table (section 1.4). Every element used by all 380 maps resolves with this rule [verified]. The images are inside `data/object.pkg`, which `client/native/qqtclient/resources.py` already reads.
* Replays: `$R/Tutorial{1,2,3}.qbv` decode with `internal/game/qbv` [verified]. All three use non-rule-1 maps (311, 806 bun, 704 kick-bomb), so `battlereplay` refuses to re-simulate them. Their raw network events (0x0FA2 move, 0x0FA3 bomb, 0x0FA4 explode, ...) can still drive a renderer as if they came from the server.

---

## 1. Map data

### 1.1 Map ID -> file (`map/mapDesc.py`)

* `mapdata.LoadCatalogWithMapRules` reads `<root>/map/mapDesc.py`, decodes it as **GBK** and regex-parses the tuples (`internal/game/mapdata/catalog.go:355-380`; the regex `clientMapLine` is at `catalog.go:25`).
* Tuple columns, per the file's own header comment: `(0 ID, 1 type/family, 2 name, 3 width=15, 4 height=13, 5 player limit, 6 .map filename, 7 thumbnail .img, 8 music .ogg, 9 required points, 10 hint, 11 selectable, 12 item-map flag)`. For example, `(211, 'town', '景阳岗', 15, 13, 8, 'town11_8.map', 'town11_8.img', 'town.ogg', 0, '', 1, 0)`.
* The room's map ID is column 0. The file is `<root>/map/<column 6>`. Several IDs can share one file (1011 and 1001 both use `match01_2.map`).
* Families in the shipped mapDesc: rand, match, bomb, box, bun, desert, field, machine, mine, pig, practice, pve, sculpture, snow, tank, town, treasure, water.
* An optional extra rules JSON (`DefaultCompetitiveMapRulesPath`, `competitive_map_rules.go:16`) lives at `<root>/../../data/...`. If the file is missing it is ignored.

### 1.2 `.map` binary layout (little-endian) [code + verified]

Sources: `loadCompetitiveMapMetadata` `catalog.go:476-540`, `competitiveMapGeometry` `catalog.go:555-578`, `competitiveWallItemTableOffset` `catalog.go:543`, `parseCompetitiveWallItemRules` `catalog.go:607`, `parseCompetitiveCellTable` `catalog.go:636`.

| offset | type | meaning |
|---|---|---|
| 0 | u32 | version: 3 (fixed 15x13) or 4 (explicit size) |
| 4 | u32 | **native rule** (1 = ordinary battle; 2 kick-bomb; 3 bun; 4 wrestle; ...) |
| 8 | u32 | player count (equals the `_N` filename suffix in 354/380 maps; not parsed by Go) [verified] |
| 12 | u32,u32 | **v4 only**: width, height. The header is 12 bytes for v3 and 20 bytes for v4 |
| hdr | 3 x (W*H) x i32 | **three tile layers**, row-major, each cell an element ID |
| ... | u32 count + count x {u32 sceneID, u32 min, u32 max, f32 probability} | wall-item rules (max 100) |
| ... | u32 count + count x {u16 row, u16 col} | hidden-item cells (the candidate list for the ItemSeed shuffle) |
| ... | same | spawn group A (max 8) |
| ... | same | spawn group B (max 8) |
| ... | same | objective cells (special rules) |
| EOF | | Go rejects trailing bytes |

Layer semantics, verified on `town11_8.map`:
* **Layer 0**: top/special objects. Usually almost empty; town11 has a single 3005.
* **Layer 1**: walls, boxes and scenery (3004, 3007, 3009, 3010, 3015, 3017, 3018).
* **Layer 2**: floor tiles. Every cell is filled (3002/3003).
* Collision is built from **layers 0 and 1 only** (`parseCompetitiveBattlefield`, `competitive_battlefield.go:201`).
* Values `<= 0` are editor continuation markers for multi-cell elements (for example -11001) and are skipped (`:233`). A positive anchor stamps its `size=(w,h)` footprint to the right and down. When anchors overlap, the last writer wins.
* The map hash used by GAME_BEGIN is the uppercase hex MD5 of the file (`catalog.go:486`).

### 1.3 Element attributes: `object/mapElem/mapElem.py` [code + verified]

* This is a plain-text Python file (UTF-8, about 4.4k lines). The base class is `QQTMapElem` with `version=5, LifeTime=-1, canMove=0, canDoSpecial=0, offset=(0,0), size=(1,1), Level=256, GridAttr=(0,), canHide=0`. Subclasses are named `class QQTMapElem<ID>(QQTMapElem)` and override fields, with `imageID = <ID>`.
* The Go parser `loadCompetitiveMapElements` (`competitive_battlefield.go:94`) reads only `LifeTime`, `imageID`, `canMove`, `size` and `GridAttr` with regexes.
* For rendering you also need **`offset`** (the image offset), **`Level`** (draw order) and **`canHide`** (a top-layer element that hides players, e.g. 17105 with `GridAttr=(5397,)`). The Go code does not parse these three.
* `GridAttr` gives one value per footprint cell. Bits 0..3 are player traversal per direction (8=right, 4=up, 2=left, 1=down; `nativeTraversalBit` `battleengine/types.go:168`). Bits 8..11 are flame traversal in the same order shifted left by 8. `attr & 0xf == 0` means the cell is solid for players. `attr & 0xf00 != 0` means flames can pass.
* `LifeTime > 0` is the number of hits to destroy the element; such an element is breakable. Grass can be walkable and also destructible. `canMove & 2` on a 1x1 element means it can be pushed normally; `LifeTime >= 0` means the Panda avatar can push it (`:265-300`).
* `object/mapElem/mapElem.prop` is a binary sibling file. The Go code does not use it.

### 1.4 Element ID -> sprite [verified empirically; the repo does not document this]

`elementID = family*1000 + n` -> `object/mapElem/<dir>/elem<n>_<action>.img`, with these family directories:

```
1 desert  2 snow   3 town     4 mine   5 water   6 field   7 bomb      8 bun   9 pig   10 treasure
11 match 12 sculpture 13 machine 14 box 15 practice 16 exploration 17 common 18 pve 19 tank
```

* I checked every positive element ID in all 380 `$R/map/*.map` files against the archive. None were missing.
* Action suffixes: `stand` (861 files), `die` (98; the destruction animation, e.g. `town/elem4_die.img` has 2 frames), `trigger` (35).
* The files are not loose on disk (`$R/object/mapElem/town/` is empty). They are inside `data/object.pkg`, indexed by `data/object.idx`. The Python reader is `client/native/qqtclient/resources.py:18` (`ObjectArchive`, a port of `internal/game/itemcatalog/archive.go`). Paths are matched case-insensitively.
* Sprites use the DIMG format (`client/native/qqtclient/dimg.py`, a port of `internal/game/itemcatalog/dimg.go`). Each frame has an `x,y` offset inside the sprite canvas. Wall sprites are taller than 40 px (e.g. `town/elem4_stand` is 42x54, `elem5_stand` is 44x78), so they overhang upward.
* **[inferred]** Draw a sprite at the cell's top-left, plus the frame offset, minus `mapElem.py offset` (or some sign variant), with the bottom aligned to the cell. **[inferred]** Sort by row, then layer, then `Level`. Neither has been verified against Client.exe. Use the `qqt-binary-re` skill or a Wine screenshot to pin them down.
* Map thumbnails: `map/<name>.img` (column 7) and `map/bigIcon/`. Map type icons: `map/icon/<type>.img` (used by uiGame at `uiGame.py:402`).

### 1.5 Go functions to port (or just call through the sidecar)

* `mapdata.LoadCatalog(clientRoot) (*Catalog, error)` `catalog.go:349` and `LoadCatalogWithMapRules` `:355`
* `(*Catalog).CompetitiveMap(id) (CompetitiveMap, bool)` `:1057`, `CompetitiveMapMetadata` `:1065`, `CompetitiveIDs` `:1094`, `RandomOrdinaryCompetitive` `:1109`
* `loadCompetitiveMapMetadata` `:476`, `competitiveMapGeometry` `:555`, `parseCompetitiveWallItemRules` `:607`, `parseCompetitiveCellTable` `:636`
* `loadCompetitiveMapElements` `competitive_battlefield.go:94`, `parseCompetitiveBattlefield` `:201`, `parseCompetitiveAirborneCells` `catalog.go:580`
* Engine side: `battleengine.GridFromCompetitiveMap` `map_adapter.go:13`, `ValidateNativeRuntimeMap` `:54`, `ConfigFromCompetitiveMap` `:108`, `HiddenPickupsFromCompetitiveWallItems` `:274`, `PlaceNativeSpawns` `spawn_placement.go:51`, `NativeSpawnModeForMap` `spawn_placement.go:24`
* Struct: `mapdata.CompetitiveMap` `catalog.go:243` (`NativeRule`, `WallItemRules`, `HiddenItemCells`, `SpawnGroupA/B`, `Battlefield`, `MapHash`, ...)

For rendering alone, a ~60-line Python reader is enough: parse the header, read the 3 layers, and take `offset/size/Level/canHide` from mapElem.py. Rules should come from Go through the sidecar.

---

## 2. battleengine public API

### 2.1 Units and coordinates [code]
* Cell = **40 px** (`CellSizePixels`, `types.go:19`). The grid is usually 15 wide by 13 tall, so the playfield is 600x520 px.
* `Position{X,Y int32}` is in **pixels from the map top-left** and refers to the actor's **centre** (`PositionAtCellCenter` `types.go:202`). `Cell{Row,Col int16}` is `pos / 40`.
* Arithmetic is integer only, with no fixed point beyond a hidden `moveRemainder`. The engine is fully deterministic given `Config.Seed`.
* Time is in **milliseconds of the native scene clock**. The engine starts at `NativeRoundStartClockMS = 3000` (after Ready/Go). The round ends at an absolute 240000 ms (`StandardRoundTimeMS`). `ElapsedMS()` returns the absolute clock (`engine.go:270`) and `RoundElapsedMS()` returns the clock minus the start (`:280`).
* Tick length is caller-chosen (`Rules.TickMS`). **The live server uses 20 ms** (`competitiveAIWorldTickMS`, `internal/server/probe/competitive_ai_runtime.go:29`, used at `:445-490`).
* `Direction`: 0 none, 1 up, 2 right, 3 down, 4 left (`types.go:407`). **Sprite direction order is different**: 0 right, 1 up, 2 left, 3 down (`client/native/qqtclient/avatar.py:3-4`). QBV/wire `WalkAndDirection` also uses right/up/left/down (`battlereplay/timeline.go:56`).

### 2.2 Construction
```go
catalog, _ := mapdata.LoadCatalog(clientRoot)
entry, _   := catalog.CompetitiveMap(mapID)               // must be NativeRule 1
rt, err := battleengine.NewRuntimeFromCompetitiveMap(entry, battleengine.CompetitiveRuntimeOptions{
    SpawnSeed: begin.SpawnSeed, ItemSeed: begin.ItemSeed,   // from GAME_BEGIN 0x0FA1
    SpawnMode: battleengine.NativeSpawnModeForMap(entry, freeRule),
    TickMS: 20, TrapDurationMS: 6000,                        // 0 = never auto-die (live default for humans)
    RecordedWallItems: ..., RecordedDelayedItems: ..., UseRecordedWallItems: true, // GAME_BEGIN NewItems/Items
    Participants: []battleengine.NativeRuntimeParticipant{{PlayerID, RoleID, TeamID, Source: ParticipantHuman}, ...},
    Policies: map[uint16]battleengine.Policy{ /* only for ParticipantVirtualAI */ },
})
```
* `NewRuntimeFromCompetitiveMap` is at `native_runtime.go:54`; its options are at `:24-49`. It calls `ParticipantFromNativeRole` (`types.go:298`, which takes stats from rolecatalog), then `ConfigFromCompetitiveMap` (`map_adapter.go:108`, which binds the native constants, places spawns, rolls or uses the recorded wall items, and runs the ItemSeed coordinate shuffle), then `battleengine.New(Config)` (`engine.go:44`), then `NewRuntime(engine, policies)` (`policy.go:62`).
* The lower-level path is `battleengine.New(Config)` with a hand-built `Config{Seed, Grid, Rules, Participants, Pickups, ScheduledPickups}` (`types.go:370`). The engine tests use this.
* `SimulationSeed` falls back to `SpawnSeed<<32 | ItemSeed` when zero.
* `NativeOutcomeAuthority` must stay **false** for offline/local simulation. When true (live AI mode), the engine only emits `*Requested` events and waits for an original-client arbitrator to confirm them.

### 2.3 Stepping and input
* `Runtime.Step(humanActions []Action) ([]Event, error)` `policy.go:534`, `StepWithTrace` `:412`, `Engine.Step` `step.go:8`. Each call advances exactly one `TickMS`.
* `Action{PlayerID, Move Direction, PlaceBomb bool, UseActionID uint8}` `types.go:437`. `Move` is a **held** direction, so send it every tick while the key is down. `PlaceBomb` and `UseActionID` are one-tick pulses. Valid `UseActionID` values are 41, 42, 43, 44, 46, 63, 64 (`action.go:62`). Virtual players get their actions from `Policy.ChooseAction(Observation, legal)` (`policy.go:13`).
* External/remote facts arrive through the "Accept"/"ApplyVerified" boundary. This mirrors how the server mirrors original clients (`policy.go:182-410`, `replay_checkpoint.go`, `human_movement.go:25`):
  * Remote movement: `ReconcileHumanMovement(id, Position)`, `ReconcileHumanMovementState(id, pos, dir)`, `ReconcileHumanMovementPath(id, NativeHumanMovement{Sequence, TimeMS, Current, Corner, End, Direction, Speed, Moving, Forced})`. The last one consumes the 0x0FA2 PLAYER_MOVE path.
  * Remote bomb: `AcceptHumanBombPlacement(id, cell, wirePower, property, placedAtMS)`. Wire power is reach+1.
  * Arbitrator outcomes: `AcceptNativeBombExplosion[At]`, `AcceptNativePickup`, `AcceptNativeElimination`, `AcceptNativeRescue`, `AcceptNativeActorHitFrom`, `AcceptNativeBattleAction`, `AcceptNativeBombMovement`, `AcceptNativeMapElementMovement`, `AcceptNativePickupDispatch`, `AcceptNativeItemDestruction`, `AcceptHumanDeparture`; also `Engine.ConfirmTrappedDeath` `actor_state.go:358`.
  * **These only accept `ParticipantHuman` actors.** For a networked client, every player, including the local one, should be `ParticipantHuman`. Remote players are then driven by Reconcile/Accept calls, and the local player by `Step` actions.
* **Networking caveat**: QQTang battles are client-simulated. Each Client.exe simulates locally and sends 0x0FA2 moves and 0x0FA3 bombs. The server relays them. An elected **arbitrator** client (`GAME_BEGIN.ArbitratorPlayerID`, changed by 0x0FB1) produces authoritative explosion, hit, kill, rescue and pickup notifications (0x0FA4..0x0FBF, `internal/protocol/game/game_event.go:15-42`). The Python client must eventually be able to act as arbitrator: run the engine with `NativeOutcomeAuthority=false` and serialize its `EventBombExploded` / `EventActorTrapped` / ... results into those packets. That logic is the probe's live adapter (`internal/server/probe/competitive_ai_runtime.go`). It is server-coupled and should be read, not imported.

### 2.4 Output state
Accessors on `*Engine` return copies (`engine.go:270-371`): `Actors()`, `Bombs()`, `Flames()`, `Pickups()`, `FieldObjects()`, `ActionProjectiles()`, `Grid()`, `TileAt()`, `Terminal()`, `ElapsedMS()`. `Runtime.EngineSnapshot()` (`policy.go:172`) deep-copies the engine. `Observation(playerID)` (`observation.go:71`) is the fog-respecting view used by AI.
* `Actor` `types.go:507`: Position, State (0 active, 1 trapped, 2 eliminated), Facing, BombCapacity/Power/SpeedRate (+Max), TrapExpiresAt, transformation (`AvatarRoleID`, `TransformationExpiresAt`), `HarmProtectionExpiresAt`, `HeldActions[7]`, MovementStatus.
* `Bomb` `:558`: ID, OwnerID, Cell, Power (= arm reach), ExplodeAtMS, FlightUntilMS.
* `Flame` `:589`: one entry per cell, with `Cell`, `OwnerID`, `BombID`, `ImpactAtMS` and `ExpiresAtMS`. Flames carry **no direction**. To choose centre, arm and end sprites, combine flame cells with `EventBombExploded.Cell` and `BlastRowMin/Max`/`BlastColMin/Max` (`types.go:790-796`).
* `Pickup` `:638`: SceneID, Cell, State (1 hidden, 2 available, 3 collected).
* `Event` `:746` with `EventKind` `:682`: 1 BombPlaced, 2 ActorMoved, 3 BombExploded, 4 CellDamaged, 5 CellDestroyed, 6 ActorTrapped, 7 ActorRescued, 8 ActorEliminated, 9 MatchEnded, 10 PickupRevealed, 11 PickupCollected, 12 PickupDropped, 13 TransformationEnded, 14 BattleActionGranted, 15 BattleActionUsed, 16 FieldObjectPlaced, 17 FieldObjectTriggered, 18/19 MovementStatus, 20 MapElementMoved, 21 BombKicked, 22 NativePassStarted, 23-30 `*Requested` (live mode only), 31 PickupDestroyed, 32 PickupDispatched. The `Event` and state structs JSON-encode cleanly with Go field names.
* Wall destruction is delayed by a "map retirement" (`beginMapRetirement`, `expireMapRetirements`). Use `EventCellDestroyed` timing, or `Grid()` diffs, to switch the sprite to `_die` and then remove it.

### 2.5 Sidecar: yes, it is clean, and a prototype exists [verified]
* Location: `/tmp/qqt-tools/sidecar/main.go`, about 100 lines. Build command:
  ```sh
  cd /Users/cduan/qqtang && PATH=/tmp/qqt-go/go/bin:$PATH GOTOOLCHAIN=local \
    go build -overlay /tmp/qqt-tools/sidecar/overlay.json -o /tmp/qqt-tools/sidecar/qqt-battle-sidecar ./cmd/qqt-battle-sidecar
  ```
  The overlay maps the virtual `cmd/qqt-battle-sidecar/main.go` to the /tmp file. It has to sit inside the module because of Go's `internal/` import rule. For a permanent version, commit it as `cmd/qqt-battle-sidecar/`.
* Protocol: line 1 is `{"cmd":"init","client_root":..,"map_id":211,"spawn_seed":..,"item_seed":..,"tick_ms":20,"trap_ms":6000,"players":[{"id":1,"role":1,"team":1},...]}`. The reply contains `{map,file,grid,actors,pickups,clock}`. After that, each `{"cmd":"step","actions":[{"id":1,"move":2,"bomb":true,"use":0}]}` gets the reply `{clock,events,actors,bombs,flames,pickups,outcome}`.
* Test input and output: `/tmp/qqt-tools/sidecar/in.jsonl` and `out.jsonl`. Results: 1500 steps, start clock 3000, spawns at (140,60) and (460,60), role 1 = 160 px/s, 42 hidden pickups, event counts {moved 357, placed 5, exploded 5, destroyed 5, revealed 3, trapped 1, eliminated 1, ended 1}. The match ended at 15220 ms with team 2 as winner.
* Functions a sidecar calls: `mapdata.LoadCatalog`, `Catalog.CompetitiveMap`, `battleengine.NativeSpawnModeForMap`, `NewRuntimeFromCompetitiveMap`, `Runtime.Step`/`StepWithTrace`, `Runtime.EngineSnapshot` plus the accessors, and the `Reconcile*`/`Accept*` calls for network input. For AI opponents, add `battleai.LoadDeploymentPolicy` (`internal/game/battleai/deployment_policy.go:38`). That is optional and pulls in ONNX Runtime via cgo with the `onnxruntime` tag. Without it, use a scripted `PolicyFunc`.
* Coupling and limitations:
  1. Rule 1 only.
  2. `internal/` forces the sidecar to live in this module.
  3. Remote input must be `ParticipantHuman`.
  4. `TrapDurationMS=0` means trapped humans never die on a timer (the server's live setting). Offline, use 6000.
  5. `Engine.Step` errors on duplicate or unknown players, or on an item the player does not hold.
  6. With `NativeOutcomeAuthority=false`, the sidecar decides outcomes itself, so two clients running independent sidecars can diverge unless one of them is the arbitrator and the others accept its results.
  7. Dependencies are only `golang.org/x/text`, with no cgo unless battleai is used.

---

## 3. Core rule constants [code]

All in `internal/game/battleengine/types.go:13-71` unless noted.

| constant | value |
|---|---|
| cell size | 40 px |
| round | 240000 ms absolute, including 3000 ms Ready/Go (`NativeRoundStartClockMS`) |
| bomb fuse | `NativeBombFuseMS` = 3000 ms |
| flame lifetime | `NativeFlameDurationMS` = 500 ms. Damage is applied **once**, at `ImpactAtMS`; the rest is visual (`types.go:589-600`) |
| bomb flight (Panda throw) | 400 ms |
| actor collision half-size | 19 px (box of 38 px); corner-slide tolerance 6 px; max 40 px per update |
| speed table, rate 0..10 (px/s) | 0, 40, 72, 140, 160, 180, 204, 232, 268, 320, 520 (`types.go:79`) |
| trap -> death | not proven native. Live: humans wait for the client's Die callback; virtual players 6000 ms (`competitive_ai_runtime.go:490`). Oxygen comment: about 6 s (`types.go:660-665`) |
| rescue / kill | a non-trapped actor whose centre comes within 41 px on both axes of a trapped actor, after the mover changes cell: teammate -> rescue, enemy -> elimination (`actor_state.go:10-60`) |
| post-transformation protection | 3000 ms; movement status 10000 ms; detector item 10000 ms; scene-4 effect 30000 ms |
| wall push | 2 points per 50 ms pulse; moves after the count exceeds 12 (about 350 ms) |

**Explosions** (`bomb.go:66-272`, `native_scene_lifecycle.go:186`):
* Reach per arm is `Power` cells.
* An arm stops before an out-of-map cell and before a non-flame-passable, non-destructible cell.
* An arm **includes and stops at** a destructible cell (Durability > 0), which then loses 1 durability or starts retiring.
* An arm stops **at** another registered bomb, which chain-detonates in the same batch. Chains are tracked through `nativeBombLinks` and `TriggeredByBombID`.
* Visible items in open cells hit by flame are destroyed.
* An actor that flame touches becomes **trapped** (in a bubble), unless the actor is harm-protected. A transformed actor loses the transformation instead (`actor_state.go:76-105`).

**Roles** (`internal/game/rolecatalog/combat.go:23-44`). Each entry is (bubbles, maxBubbles, power, maxPower, speedRate, maxSpeedRate), decoded from Client.exe `DAT_007f2680`:
```
1:{2,6,2,8,4,7}  2:{2,6,3,9,4,6}  3:{1,5,2,9,5,7}  4:{1,7,3,9,4,7}  5:{2,7,3,8,3,7}
6:{1,6,2,7,5,8}  7:{1,8,3,9,3,8}  8:{1,7,2,6,5,8}  9:{2,6,3,7,5,8} 10:{3,8,4,9,5,8}
11:{3,8,4,9,5,8} 13:{2,8,3,7,4,6} 14:{1,7,2,6,5,8} 15:{2,7,3,9,4,8} 16:{1,8,3,9,4,8}
18:{1,6,1,6,5,8} 19:{2,8,1,6,4,7} 20:{2,6,1,5,4,6} 21:{2,8,1,5,3,6} 22:{2,6,3,7,5,8}
```
* Speed rate 4 = 160 px/s, i.e. 4 cells/s.
* Roles 12, 17 and 23 (random) are not playable.
* `internal/game/roledata/rules.go` covers room role-selection rules, not combat.

**Items (scene IDs)** (`battleengine/pickup.go:9-52`, `internal/clientdata/sceneelement/catalog.go:140-190`):
* Basic attributes: 1 bubble +1, 2 power +1, 3 speed +1, 6 bubble +8 (to max), 7 power +8, 8 speed +8, 5 random attribute.
* Effects: 4 scene-4 bubble visual (30 s), 47 fast movement, 61 detector (reveals hidden items in a 3x3 area), 62 oxygen +1000, 26 oxygen bottle.
* Held battle-action pickups (scene -> action x count): 21 -> 41x1, 23 -> 42x1, **24 -> 63x1 (needle: self-rescue from a bubble, `active_item.go:56-80`)**, 25 -> 43x3, 27 -> 44x2, 28 -> 64x1 (detonate all own bombs now). Actions 41/42/43 place field objects. Actions 44/46 are 1-second projectiles at the first bomb in the facing direction.
* Transformations (scene -> avatar role): 101 -> 43, 104 -> 41, 107 -> 45, 108 -> 46, 109 -> 44 (Panda), 110 -> 42, 114 -> 54, 115 -> 55.
* Match sugar: 81-95.
* Hidden-item amounts per map come from the `.map` wall-item rules plus `PlanCompetitiveSupply` (`mapdata/competitive_supply.go:44`). Positions come from the ItemSeed shuffle over `HiddenItemCells`.
* Repo docs: `docs/item-id-catalog.md` (account item IDs, a different namespace), `docs/competitive-map-rules.md`, `docs/ai-bubble-costumes.md` (bubble skins).

---

## 4. Rendering a match

### 4.1 Sprites (all DIMG, from `data/object.pkg` unless noted) [verified paths and frame counts]
* **Players**: `object/player/Player<role>.ini` is loose on disk. Its sections are `[walk] [stand] [trigger] [die] [win] [cry] [lose] [wait]`, each listing parts (hair, face, cloth, leg, npack, head, body, foot -> part ID). Each part image is `object/<part>/<part><id>_<action>.img` on a 100x100 canvas.
  * `_stand`: 4 directions x 1 frame. `_walk`: **4 directions x 6 frames** (24 frames; e.g. `cloth10101_walk.img`, `body1_walk.img`). Direction order is right, up, left, down.
  * Team colour comes from the `_m.img` mask plus `colorlist.dat`.
  * This is already implemented for stand frames in `client/native/qqtclient/avatar.py:94-130` (`PART_ORDER` npack, foot, leg, body, cloth, head, face, hair). Extend it to choose `frame = dir*6 + (t // frame_ms) % 6`.
  * Some action sections reference cloth IDs without a matching file (e.g. `cloth10102_trigger.img`). In that case fall back to the stand image.
* **Bubbles**: `object/bomb/bomb<N>_stand.img`. 303 are loose on disk plus 8 in the pkg. `bomb1_stand` = 4 frames (pulse loop), 44x42 canvas, frames overhang downward (y+h up to 55). The default bubble is probably `bomb1`. Costumes map account item -> bomb style (`docs/ai-bubble-costumes.md`). The wire `PLAYER_USE_BOMB.Property` byte is at `internal/protocol/game/bomb_event.go:29`.
* **Flames**: `object/flame/flame<k>_stand.img`, all 40x40 per frame. Contact sheet: `/tmp/qqt-tools/flames.png`. **[inferred from images]**
  * `flame1`: centre loop, 4 frames.
  * `flame2..5`: arm middle segments in 4 directions. 7 frames: 4 burning, then 3 fading.
  * `flame6..9`: arm end caps. From the bulge side they look like right, up, left, down, matching the sprite direction order.
  * `flame10`: 9-frame centre burst.
  * `flame255`: red-dot danger overlay, 3 frames.
  * Over the 500 ms lifetime, play the frames at about 70 ms each.
* **Items**: `object/item/item<SceneID>_stand.img`, a 7-8 frame bob animation. Present for 1-8, 21-28, 47, 61, 62, 101-115. The repo itself relies on this convention (`mapdata/competitive_objective_items.go:19` mentions `Item16_stand.img`).
* **Map elements**: section 1.4. Use the `_die` animation on `EventCellDestroyed`.
* **Trapped bubble, death, other effects**: `effect/*.eff`/`.img` (e.g. `effect/baozha.eff`, `effect/chuanqiang.eff`) and `object/misc/misc<N>_{stand,die,trigger}.img`. Which one is the "trapped in bubble" sprite has **not been identified**; the player `[trigger]`/`[die]` ini sections cover part of it. This is an RE task.

### 4.2 Screen layout (800x600)
* The `UI.game` screen is `rect=(0,0,800,600)` (`generated/scripts/res/uiRes/uiGame.py:291-294`). Overlays:
  * player list `dlg_playerList.img` at x=609 (191x600, frame x-offset 7): `uiGame.py:381`
  * status/item bar `dlg_statusBar.img` at (0,540,623,60): `uiGame.py:855-858`
  * save-replay and leave buttons at (27,533) and (125,534)
  * replay ProcessBar at (631,580)
  * timer `number3.img` inside the player list
* The playfield frame images drawn by the host are `object/ui/game/gametop.img` (623x27) and `gameleft.img` (10x544).
* **[inferred]** The map (600x520) is drawn at about **(10, 27)**: left strip 10 px, top bar 27 px, right edge at 610 where the player list begins, bottom at 547 under the status bar's transparent top. Confirm against a real screenshot or RE before relying on it.
* The repo has no tooling, tests or docs on battle rendering. `client/native/README.md` says the battle screen is "未开始" (not started).

---

## 5. Replays (QBV)

* Format (`internal/game/qbv/format.go:1-60`, `decode.go:11-24`): a zlib stream. The decompressed body starts with `"vaqq"`, version 1, then 4 header DWORDs (opaque, LocalUIN, opaque, PlayerCount).
* After the header come bootstrap records (GAME_BEGIN, GAME_OVER, one per player), then timed events `{TimeMS, Schema u16, Data, 3 opaque DWORDs}`. Event bodies are the **native network packets**, unchanged.
* `qbv.DecodeNativeEvents` (`native.go:52`) types them: Movement 0x0FA2, BombPlaced 0x0FA3, BombExplode 0x0FA4, Trapped 0x0FA5/6, Died, Killed, Item 0x0FAC/D, Dispatch 0x0FAE, UseItem 0x0FAF/B0, MoveBomb, MapElementMoved, ...
* `qbv.ParseBootstrapGameBegin` (`bootstrap.go:13`) returns map, seeds and players.
* `battlereplay.ConfigFromRecording(catalog, rec, opts)` (`config.go:38`) plus `Run`/`RunEventAligned` (`run.go:95/104`) re-simulate a rule-1 QBV and diff the result against the recording (`Report`, `run.go:44`). `BuildTimeline` (`timeline.go:59`) turns QBV packets into engine inputs. It is a good reference for wire -> engine conversion, including the power-1 conversion and the right/up/left/down bit order.
* Sample files: `$R/Tutorial1.qbv` (map 311, 2 players, 2169 events, 93 s), `Tutorial2.qbv` (map 806, bun), `Tutorial3.qbv` (map 704, kick-bomb). All decode. None is rule 1, so `ConfigFromRecording` rejects them (Tutorial1 because of unsupported scene ID 24040). The repo contains no `.qbv` files and no testdata. The probe used: `/tmp/qqt-tools/qbvprobe/main.go` (same overlay trick).
* **Using QBV to test rendering without a server**: decode it (port the ~100-line `decode.go` + `format.go` to Python, or have a Go helper dump JSON). Feed GAME_BEGIN into map/actor setup, then replay the events on their `TimeMS` through the same packet handlers the battle screen will use for live 0x0FA2..0x0FBF traffic. For a rule-1 visual test, record your own `.qbv` (the original client's 保存录像 button, `btn_saveReplay`), or generate a synthetic timeline with the sidecar (`out.jsonl` already serves as a state-per-tick fixture).
* `battleoracle` (`suite.go`, `static_trace.go`, `dynamic_trace.go`, `pass_activation_trace.go`) compares engine movement and pass state against captured Client.exe function traces. It is a movement-correctness oracle and not useful as a rendering fixture.
* `battleenv` (`encoding.go`, `inference.go`) encodes engine observations into ONNX tensors for AI. It has no stdio or RPC interface.

---

## Artifacts in /tmp/qqt-tools
* `battle-engine.md`: this report.
* `sidecar/main.go`, `sidecar/overlay.json`, `sidecar/qqt-battle-sidecar`, `sidecar/in.jsonl`, `sidecar/out.jsonl`: JSON-lines sidecar prototype and test run.
* `qbvprobe/main.go`, `qbvprobe/qbvprobe`: QBV decode and replay probe.
* `flames.png`: contact sheet of the flame sprites.

## Native inventory update, 2026-11-04

The native client now enables the default-off `Rules.NativeActionInventory` extension (and the matching competitive-runtime option). It retains the ordered action vector and 32-bit stack counts beyond the seven visible slots. `Actor.HeldActionInventory()` returns a detached full list; consumption, death drops, clearing, respawn and cloning preserve its semantics. The default policy inventory and fixed seven-slot `Observation.HeldActions` stay bounded, including their byte-count projection.

Local prepared props follow the room bar via `Client.exe 005B974A`, then share chronological append/stack/erase order with world actions. The sidecar exports the full mixed `slots` list; Tab pages it seven entries at a time. The original Tab getters omit the offset, so the host corrects this indexing bug. Native probes, compatibility scope and live verification: [item-slots-re.md](item-slots-re.md).
