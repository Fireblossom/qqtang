# QQTang 5.2 match protocol — client-perspective spec (StartGame → back to room)

Source: Go server at `/Users/cduan/qqtang` (read-only analysis). All paths below are relative to that repo.
Integers are **big-endian** unless marked **LE**. "Client" = the new Python client. "Native" = original Client.exe/QQTPPP.dll behaviour that the server was built around.

> Update: implemented for the arbitrator case in `cmd/qqt-battle-sidecar/online.go` (UDP side) and `qqtclient/battle/online.py` (TCP side). Findings from testing against the server: a solo owner needs the AI card (item 30099) active in the inventory or StartGame fails with 0xF004; server AIs publish their item uses (0x0FB0) on Type-2, not TCP; the 0x0FA4 map-element list must include walls destroyed by the blast (engine `EventCellDestroyed`), not only damaged ones; AI contact with a banana/glue field object arrives as 0x0FAC with ItemID 42/43 and is answered with 0x0FAD like a pickup.

> Legend: **[code]** = directly enforced/produced by server code; **[native]** = server comment describing what Client.exe does (not enforced, but peers depend on it); **[inferred]** = my reading, not proven by code.

---

## 0. Architecture in one paragraph (read this first)

The server is **not** a game simulator for human players. A match is peer-simulated by the clients themselves:

* Every client runs the full scene simulation from `GAME_BEGIN` (map id + seeds + rosters).
* Realtime scene facts (movement, bombs, explosions, pickups, traps, deaths…) travel **only** over the QQTPPP UDP channel, **Type-2 multicast**, which the server relays byte-for-byte to the listed room peers (`internal/server/probe/room_peer_udp.go:432-450`).
* The same facts are **also** uploaded over TCP as a *reliable audit mirror* (`REQUEST_PLAY 0x0083` and/or the room-fast upload `0x0065`). The server validates them, updates settlement state, sends a correlated ACK to the sender, and — for almost all schemas — **does not** fan them out again (`internal/server/probe/game_event_dispatch.go:39-105, 624-630`; `room_fast_dispatch.go:143-160`).
* One human is the elected **arbitrator** (the native "rule object" owner). In competitive rooms this is the player who sent StartGame, i.e. the room owner, if human (`internal/game/match/competitive.go:232-252`; `match_start_dispatch.go:358`). The arbitrator's client resolves requests (item pickup, kill/save, bomb kick), computes bubble explosions, dispatches the item bird, and produces the corresponding NOTIFY_* records. **If your Python client is room owner, it is the arbitrator** and must implement that role, including for server-side AI players.
* The server owns only: roster/participants, trap/kill/death accounting, elimination-based conclusion (ordinary maps), the round timeout, GAME_OVER normalization, and settlement.

---

## 1. TCP envelope recap (needed for every message below)

`internal/protocol/game/local_packet.go:13-17, 75-105, 154-197`; `internal/protocol/directory/packet.go:12-14`

| Off | Size | Field |
|---|---|---|
| 0 | 4 | total length (incl. this field) |
| 4 | 4 | outer sequence (0 in all captures) |
| 8 | 2 | route sequence (request correlation; server notifications set 0) |
| 10 | 2 | 0xFFFF |
| 12 | 4 | UIN |
| 16 | 1 | 0x01 (key marker) |
| 17 | 1 | 0x20 (ST length 32) |
| 18 | 32 | ST = bytes 0x20..0x3F (`directory.LocalST`) |
| 50 | n | QQ-TEA(key = bytes 0x10..0x1F) of plaintext |

Plaintext = 14-byte inner header + payload:

| Off | Size | Field |
|---|---|---|
| 0 | 2 | command |
| 2 | 4 | dispatch flags (0; server notifications force 0) |
| 6 | 2 | inner sequence (= route seq for requests; 0 for server pushes) |
| 8 | 2 | route (3 = room/match subsystem; 2 = player profile; 4 = transfer-UDP-OK) |
| 10 | 2 | 0xFFFF |
| 12 | 2 | section id (room id for route 3, e.g. 1; 0xFFFF for route 4) |

Correlated responses copy the request's header (route seq, inner seq, route, section) and replace the command/payload (`local_packet.go:120-122`). Unsolicited pushes zero bytes 2..8 and outer bytes 8..10 (`local_packet.go:160-171, 193-195`). In-match pushes for a recipient use route 3 / section = roomID (`game_event.go:528-544`).

Captured StartGame plaintext (header+payload): `00820000000000dd0003ffff0001 000f4241 0f45940d` (`start_game_test.go:10`): route 3, section 1 (room 1), payload = UIN 1000001 + 4-byte client time.

---

## 2. Timeline overview

```
owner  --TCP 0x0082 StartGame------------------------------------> server
owner  <--TCP 0x0082 ResultID (2 bytes)                              (correlated)
all    <--TCP 0x008D EnterRoomNotify  (one per virtual AI, only if AI fill)
owner  <--TCP 0x0089 GameBegin       (correlated to the StartGame request)
peers  <--TCP 0x0089 GameBegin       (rebound as notification, seq 0)
all    <--TCP 0x0084 PLAYER_THROW_BOMB pushes (passive-function projections, if any)
all    <--TCP lobby room push (room now in-match)
owner  <--TCP 0x0084 CREATE_NPC_BOSS (only Boss maps; +50 ms)
        [server starts round timer: RoundDurationMS (180 000 or 240 000 ms) of wall time]
all    --UDP Type-1 presence -> server UDP port; <- Type-1 reply with observed endpoint
all    --TCP 0x0086 RequestUDPOK(mode 0) per peer -> peer gets 0x0087
all    --UDP Type-3 rendezvous per peer (every ~3 s while negotiating)
all    --UDP 15-byte 0x20/0x21 peer handshake
all    --TCP 0x0086 RequestUDPOK(mode 1, UIN+route) per peer -> peer gets 0x0087
        scene clock 0..3000 ms = ReadyGo countdown (no control)
all    <==UDP Type-2==> realtime scene stream (relayed by server)
all    --TCP 0x0083 REQUEST_PLAY audit copies  <- correlated ACK (echo)
all    --TCP 0x0065 room-fast audit copies (no reply)
        ... death / kill / timeout / client GAME_OVER ...
all    <--TCP 0x0084 NOTIFY_GAME_OVER (0x0FBB)  (+750 ms grace after the deciding event)
        server settles; then: 0x008E LeaveRoomNotify per virtual AI, lobby push,
        0x0094 GameMoneyNotify (if money reward), 0x00D9 PlayerItemAddNotify (inventory)
        room returns to PhasePreparing; clients stay in the room (no re-enter)
```

---

## 3. StartGame 0x0082 (C→S) and its response

`internal/protocol/game/start_game.go:9-71`; `internal/server/probe/match_start_dispatch.go:124-232`

Request payload (8 bytes, enforced length `start_game.go:64`):

| Off | Size | Field |
|---|---|---|
| 0 | 4 | UIN (must equal envelope UIN) |
| 4 | 4 | client time (opaque) |

Response 0x0082 (correlated), payload 2 bytes `ResultID`:
`0x0000` success, `0xF001` players required, `0xF002` not ready, `0xF003` team invalid, `0xF004` item required, `0xFFFF` rejected. On rejection the server also sends a `RoomChatNotify 0x0093` system message with Chinese text (`match_start_dispatch.go:98-119`). Only the owner may start; all must be ready; ≥2 teams; standard rooms need equal teams (`match_start_dispatch.go:52-96`).

Delivery order on success (`tcp_sequence.go:47-95`, `match_start_dispatch.go:166-231`): response → `postResponse` (AI `EnterRoomNotify 0x008D` broadcasts, if AI fill) → `GameBegin 0x0089` to owner → `afterFollowUp` (GameBegin rebroadcast to peers, AI runtime install, passive-function projections, lobby push, **round-timeout scheduled**) → optional `startFollowUp` (Boss create) after its delay.

---

## 4. GameBeginNotify 0x0089 (S→C)

`internal/protocol/game/game_begin.go:8-61`, `game_begin_data.go:204-259, 261-398`

Payload = `u16 innerLength` + `GAME_BEGIN_DATA` (no 0x0FA1 id on the wire; the comment at `game_begin.go:9-18` explains the client selects schema 0x041C → NOTIFY_GAME_BEGIN 0x0FA1 implicitly).

| Off | Size | Field | Notes |
|---|---|---|---|
| 0 | 2 | InnerLength | = remaining bytes |
| 2 | 4 | GameID | non-zero; **must** be used in every Type-2 batch / 0x0FBD package |
| 6 | 4 | MapID | competitive map id (resource map) |
| 10 | 4 | SpawnSeed | random, non-zero; native spawn placement RNG |
| 14 | 4 | ItemSeed | random, non-zero; native wall-item placement RNG |
| 18 | 2 | ArbitratorPlayerID | must be one of Players; **the rule-object owner** |
| 20 | 1 | PlayerNum (1..8) | |
| 21 | var | PLAYER_GAME_INFO × PlayerNum | see below |
| … | 1 | ItemCount (≤32 types, ≤64 expanded) | |
| … | 6×n | GAME_ITEM_TYPE Items | competitive: **delayed supply pool** the arbitrator dispatches via the bird (`competitive_start.go:81-83`) |
| … | 1 | ReportFlag | always 0 from this server |
| … | 32 | MapHash | map file hash |
| … | 1 | ItemNewCount (≤100) | |
| … | 6×n | GAME_ITEM_TYPE NewItems | competitive: **items hidden in destructible walls** (map pool + objective pool); positions derived natively from ItemSeed + map (`competitive_start.go:64-80`) |
| … | 4 | ContinueID (int32) | competitive: -1 (0xFFFFFFFF) = use local Continue.ini |
| … | 16 | FileHash | zero for competitive |
| … | 4 | GameTimeMS | competitive: 240 000 constant; **not** the authoritative deadline (`game_begin.go:21-25`) |

PLAYER_GAME_INFO (`game_begin_data.go:55-98`), 12 + 6×k bytes:

| Off | Size | Field |
|---|---|---|
| 0 | 2 | PlayerID (room player id, not UIN) |
| 2 | 1 | RoleID (random role already resolved) |
| 3 | 1 | TeamID (non-zero) |
| 4 | 4 | DelayTime (0 for humans in competitive) |
| 8 | 4 | ExtPoint (competitive points of the player) |
| 12 | 1 | NewItemCount (≤10) |
| 13 | 6×k | GAME_ITEM_TYPE = u32 ItemID + i16 Quantity (prepared props; item-field rooms only) |

There is **no seat index** in GAME_BEGIN; seats come from the room state you already hold. Virtual AI players appear here with PlayerIDs that were announced just before via `EnterRoomNotify 0x008D` (UIN = 1 900 000 000 + PlayerID, nickname `AI<n>`, `competitive_ai_room_projection.go:11-63`).

Fields needed to start simulation: GameID, MapID, SpawnSeed, ItemSeed, ArbitratorPlayerID, the roster (PlayerID/RoleID/TeamID), Items, NewItems. **[uncertain]** the exact native RNG used with SpawnSeed/ItemSeed is not in this repo's protocol package; check `internal/game/mapdata` (`PlanCompetitiveSupply`, `CompetitiveObjectiveSceneItems`) and `internal/game/battleengine` for the Go re-implementation.

Clock: the scene clock starts at 0 on GAME_BEGIN; 0–3000 ms is the ReadyGo countdown; the first controllable time is 3000 (`battleengine/types.go:27-30`, `competitive_ai_runtime.go:15-21`). All `Time`/`ClientTime` fields inside battle messages are this scene clock in ms.

The round timeout is server wall-clock: `RoundDurationMS` (240 000 for most rules, 180 000 for two rules; `internal/game/mapdata/catalog.go:127-177`) measured from when GameBegin was sent (`match_start_dispatch.go:586-594`, `competitive_battles.go:62-68`).

---

## 5. After GAME_BEGIN: UDP registration and peer negotiation

### 5.1 Server UDP endpoint
Same host as the game TCP server; local config uses UDP `127.0.0.1:18000` (listener `p2p-candidate`, `configs/server.json`), same port number as the game TCP listener. The advertised value is `ServerUDPPort` in network settings (`internal/server/probe/config.go:81`). The server identifies UDP senders only by **(UIN, PlayerID) in the header + source IP equal to the live TCP session's IP** (`room_peer_udp.go:895-915`). There is no token; port may differ from TCP.

### 5.2 QQTPPP control header (Types 1/2/3), 18 bytes
`internal/protocol/game/room_peer_udp.go:174-312`

| Off | Size | Field |
|---|---|---|
| 0 | 2 | total datagram length (≤1024) |
| 2 | 1 | Flags (0 in all captures; server echoes request flags on Type-3) |
| 3 | 4 | PacketNumber (per-sender sequence; receivers drop non-increasing numbers, signed 32-bit compare `room_peer_udp.go:428-430`) |
| 7 | 2 | sender PlayerID (non-zero) |
| 9 | 4 | sender UIN (non-zero) |
| 13 | 2 | Type (1 presence, 2 multicast, 3 room-peer) |
| 15 | 2 | Checksum |
| 17 | 1 | RouteBytes (Type 1/3: payload length; Type 2: 6 × target count) |

Checksum (`room_peer_udp.go:318-329`): set field to 0, sum the whole datagram as **little-endian** 16-bit words (odd last byte added as-is), fold carries twice, one's-complement, store result **big-endian** at [15:17]. A valid datagram re-sums to 0.

Endpoint = 4-byte IPv4 + u16 port.

### 5.3 Type 1 — presence (mode-1 registration) **[code]**
C→S: header + 6-byte endpoint (client's local IP/port). S→C reply: identical header fields, payload replaced by the **observed** public endpoint, checksum recomputed (`server room_peer_udp.go:286-364`). The client stops sending Type-1 once answered **[native]**; the server keeps the registration for the TCP session lifetime (never pruned, `room_peer_udp.go:20-22, 974-985`). A new Type-1 replaces the endpoint and its PacketNumber high-water mark.

Requirements:
* You **must** send Type-1 from the **same UDP socket** you use for Type-2; the server delivers Type-2 to that address and rejects Type-2 whose source ≠ presence address (`room_peer_udp.go:935-940`).
* Must be in a room (`liveRoomID != 0`), else it only touches the auth lease.
* Capture (`room_peer_udp_test.go:11`): `001800000000000001000f42410001afe006c0a83801d445` (player 1, UIN 1000001, 192.168.56.1:54341).

### 5.4 Type 3 — room-peer rendezvous (mode 0)
Payload 12 bytes: `u16 TargetPlayerID, u32 TargetUIN, endpoint(6)` (`room_peer_udp.go:232-253`). Target ≠ sender. Server registers the sender's mode-0 endpoint (15 s lease, `room_peer_udp.go:20-22`), and if the target has a mode-0 registration, forwards the packet to the target with the endpoint replaced by the sender's observed address (`server room_peer_udp.go:585-673`). Native clients repeat every ~3 s while negotiating.
For a **virtual AI** target the server answers immediately with a Type-3 from the AI identity (same Flags/PacketNumber, endpoint = server UDP addr) and marks that human↔AI pair "native peer ready" (`room_peer_udp.go:681-783`).
Captures: `001e00000000000001000f424100030f7a0c0002000f4242c0a83801f686` (`room_peer_udp_test.go:32`), `001e0000000cf90001000f4241000336960c0002000f4242c0a83801e152` (`:81`).

### 5.5 15-byte direct peer handshake
`room_peer_udp.go:37-92`: `u16 len=15, u8 flags (0x20 request / 0x21 response), u16 checksum (same algorithm, at [3:5]), u32 UIN, endpoint(6)`. Normally exchanged directly peer↔peer; if sent to the server it is relayed to all other room peers with a current mode-0 registration, or, for AI peers, answered with one 0x21 per AI (`server room_peer_udp.go:113-213`). Capture: `000f21e93c000f42417f0000014650` (`room_peer_handshake_test.go:9`).

### 5.6 RequestUDPOK 0x0086 (C→S) / NotifyUDPOK 0x0087 (S→C)
`internal/protocol/game/udp_ok.go:8-131`; `internal/server/probe/peer_transport_dispatch.go:14-151`

Request payload:

| Off | Size | Field |
|---|---|---|
| 0 | 4 | SourceUIN (= envelope UIN) |
| 4 | 4 | ClientTime |
| 8 | 2 | DestinationPlayerID |
| 10 | 4 | DestinationUIN |
| 14 | 2 | InfoLength (≤32) |
| 16 | n | Info: `[0x00]` = start negotiation (mode 0); or `[mode≠0, u32 UIN, u16 route]` (≥7 bytes) = transport descriptor |

Server: validates sender in a room, destination is a live session in the same room with matching PlayerID; **no response to the sender**; forwards a NotifyUDPOK to the destination (`route 4, section 0xFFFF`, notification sequences 0).
Notify payload: `u32 RecipientUIN, u32 ClientTime, u16 SourcePlayerID, u32 SourceUIN, u16 InfoLength, Info` — e.g. `000f42411af6d4150001000f4242000100` (`udp_ok_test.go:69`).
Captured requests: mode 0 `udp_ok_test.go:13` (1000002 → player 1, Info `00`); mode 1 `udp_ok_test.go:27` (1000001 → player 2, Info `01 000f4242 0003`).
For AI destinations the server answers on the AI's behalf (Info `[0]`, or `[1, requesterUIN, 0x0003]` when you sent a non-zero descriptor) and marks the pair ready (`peer_transport_dispatch.go:63-113`).

### 5.7 Hello 0x0088
`hello.go:9-61`, `room_dispatch.go:307-323`. Request: `u32 UIN, u32 Time, u16 InfoLen(≤32), Info`. Response 0x0088 payload `00 00`, followed by a `RoomPush 0x008C` lobby room list. It is a keep-alive only; nothing match-specific.

### 5.8 "Loading finished/ready" and timeouts
* There is **no** loading-complete / ready handshake for humans. The server never waits for clients after GAME_BEGIN.
* **AI matches:** the AI runtime does not move until **every human has completed the native peer handshake with every AI** (Type-3 to the AI identity, or 0x0086 with non-zero Info to the AI) — it loops in 10 s waits forever (`competitive_ai_runtime.go:972-1005`, `:709-728`). A client that skips this sees frozen AIs until the round timeout draw.
* Round timeout: §9.3.
* TCP idle timeout: 300 s without any TCP frame closes the connection (`configs/server.json idle_timeout_ms`, `server.go:808, 849-853`) → departure → loss (§9.4). Send Hello periodically.
* Mode-0 (Type-3) registrations expire after 15 s without refresh; mode-1 presence does not expire.

---

## 6. Realtime scene data: QQTPPP Type 2

### 6.1 Datagram
`room_peer_udp.go:212-231, 268-283`:

| Off | Size | Field |
|---|---|---|
| 0 | 18 | header, Type = 2, RouteBytes = 6 × N |
| 18 | 6×N | targets: `u16 PlayerID, u32 UIN` (1..8, never yourself; include AIs) |
| 18+6N | rest | **BatchData** ciphertext: QQ-TEA with key ASCII `"qqt is the best!"` (`room_peer_batch.go:12`) |

Send to the server UDP endpoint. Server checks: sender identity/IP, room membership, Type-1 presence address == source, each target is a current room member with presence; unknown/stale targets are silently skipped; then relays the **original bytes unchanged** to each target's presence address (`server room_peer_udp.go:432-450, 786-893`). Received Type-2 from the server keep the original sender's header (PlayerID/UIN/PacketNumber). AI packets are server-built with the AI's UIN and an independent per-AI PacketNumber (`room_fast_dispatch.go:705-759`).
Capture (`room_peer_udp_test.go:56`): `0050 00 00000100 0002 000f4242 0002 21bc 06 0001000f4241 <56-byte ciphertext>`.

### 6.2 BatchData plaintext (`room_peer_batch.go:46-118`)

| Off | Size | Field |
|---|---|---|
| 0 | 1 | entry count (1..64) |
| 1 | 4 | GameID **LE** |
| 5 | 2 | FirstIndex (0 in all captures) |
| 7 | var | entries: `u16 Index, u16 Length, <Length bytes 0x0FBD package>` |

### 6.3 QQT_DATA_PACKAGE 0x0FBD (`movement_transport.go:95-153, 303-346`)

| Off | Size | Field |
|---|---|---|
| 0 | 2 | PlayerID (author; must be the sender for humans) |
| 2 | 4 | Time (scene ms) |
| 6 | 4 | GameID (non-zero, = GAME_BEGIN.GameID) |
| 10 | 1 | message count n (1..64) |
| 11 | 4n | MessageIndexes (u32 each) |
| … | | n × QQT_MSG_DATA |

QQT_MSG_DATA (22 + len bytes):

| Off | Size | Field |
|---|---|---|
| 0 | 4 | Time (scene ms) |
| 4 | 4 | DataID (schema, u32 **BE** here) |
| 8 | 2 | Data length (≤800) |
| 10 | 4 | Sequence (per-player message seq) |
| 14 | len | Data (schema body, BE fields) |
| … | 4 | GameTime |
| … | 4 | Flag |

What the server AI writes (good template, `competitive_ai_runtime.go:1381-1400`): per-player `seq++`; `MessageIndexes=[seq]`, batch entry `Index = uint16(seq)`, `Time = GameTime = scene ms`, `Flag = 0`. For PLAYER_USE_BOMB it uses `GameTime = 0xFFFF0065` copied from a native capture (`:1844`, `movement_transport_test.go:23`).

Captured 0x0FBD (`movement_transport_test.go:23`): `0002 00006bc9 00000001 01 000000b0 | 00006bc9 00000fa3 000f 4b0004cd 0002 00006bc9 f20c7fff 04 12 0004 00 ffff0065 00000000` = player 2, game 1, PLAYER_USE_BOMB, BombID 0xF20C7FFF, row 4, col 18, power 4.

### 6.4 Coordinate system and units
* Pixel coordinates, **40 px per cell**; competitive board 15 cols × 13 rows (600 × 520 px) (`battleengine/types.go:18`, `competitive_rule_event.go:13-14`). Cell centre = `col*40+20, row*40+20` (`battleengine/types.go:202-207`). Server clamps to `[0, 15*40-1] × [0, 13*40-1]`.
* Grid fields (`Row`, `Col`) are bytes (row 0..12, col 0..14 in competitive; adventure maps may be larger).
* Time: scene ms since GAME_BEGIN, ReadyGo ends at 3000.

### 6.5 Scene event schemas (DataID)
`internal/protocol/game/game_event.go:15-92`. "Producer" = who emits it on Type-2 **[native / code]**.

| ID | Name | Producer | Body |
|---|---|---|---|
| 0x0FA2 | PLAYER_MOVE | each player for itself | §6.6 |
| 0x0FA3 | PLAYER_USE_BOMB | placing player | 15 B: `u16 PlayerID, u32 ClientTime, u32 BombID(bubble appearance, default 0xF20C7FFF), u8 Row, u8 Col, u16 Power (= reach+1 per AI code), u8 Property` (`bomb_event.go:18-72`, `competitive_ai_runtime.go:1825-1848`) |
| 0x138B | NOTIFY_PLAYER_USE_BOMB | byte-identical peer form; server converts only for Boss/NPC actors on TCP | same |
| 0x0FA4 | NOTIFY_BOMB_EXPLODE | **arbitrator** | `u16 PlayerID, u32 Time, u8 nB, nB×{u16 Owner, u32 OwnerPlaceTime, u8 BombID/style, u8 Row, u8 Col, u8 RowMin, u8 RowMax, u8 ColMin, u8 ColMax}, u8 nM, nM×{u16 MapElemID, u8 Row, u8 Col}, u8 nI, nI×{u32 ItemID, u8 Row, u8 Col}` (bombs ≤64, elems ≤32, items ≤32; items = already-visible scene items destroyed) (`bomb_event.go:74-233`) |
| 0x0FA5 | PLAYER_BE_EXPLODED (trapped/hit) | the hit player (AI: server) ; arbitrator mirrors over TCP | 11 B: `u16 PlayerID, u32 Time, u16 PosX, u16 PosY, u8 IsAvatar` (`combat_event.go:15-127`) |
| 0x0FA6 | NOTIFY_PLAYER_EXPLODED | unused in this build (table entry only) | same 11 B |
| 0x0FA7 | NOTIFY_PLAYER_DIE | dying player (syrup timeout) or arbitrator | `u16 PlayerID, u32 Time, u16 PosX, u16 PosY, u8 n, n×GAME_ITEM{u32 ItemID, u8 Row, u8 Col}` (drops) (`game_event.go:127-181`) |
| 0x0FA8 | REQUEST_KILL_PLAYER | player touching a trapped enemy → to arbitrator | 12 B `u16 Src, u32 Time, u16 Dst, u16 PosX, u16 PosY` (`combat_event.go:27-37`) |
| 0x0FA9 | NOTIFY_PLAYER_KILLED | arbitrator | 12 B interaction + `u8 n, n×GAME_ITEM` |
| 0x0FAA | REQUEST_SAVE_PLAYER | teammate touching trapped mate → arbitrator | 12 B |
| 0x0FAB | NOTIFY_PLAYER_SAVED | arbitrator | 12 B |
| 0x0FAC | REQUEST_GET_ITEM | player touching an item → arbitrator | 14 B `u16 PlayerID, u32 Time, u32 ItemID(scene id), u16 PosX, u16 PosY` (`combat_event.go:46-68`) |
| 0x0FAD | NOTIFY_PLAYER_GET_ITEM | arbitrator (only this removes the item / applies effect) | 14 B |
| 0x0FAE | NOTIFY_DISPATCH_ITEM | arbitrator (bird supply) | `u32 Time, u8 n, n×GAME_ITEM, u8 m, m×{u32 ItemID, u32 DispatchTime}, m×u8 Row, m×u8 Col` (`dispatch_item.go:20-101`) |
| 0x0FAF / 0x0FB0 | REQUEST_USE_ITEM / NOTIFY_PLAYER_USE_ITEM | world item use; server converts request→notify on TCP | 30 B `u16 PlayerID, u32 Time, u32 ItemID, u16 PosX, u16 PosY, 4×u32 Flags` (`use_item.go:8-74`) |
| 0x0FB1 | NOTIFY_CHANGE_ARBITRATOR | server (TCP) when arbitrator leaves | `u16 NewArbitrator, u32 0` (`game_event.go:546-560`) |
| 0x0FB4 / 0x139C | REQUEST_MOVE_BOMB / NOTIFY_PLAYER_MOVE_BOMB (kick) | player / arbitrator | 20 B `u16 PlayerID, u16 Time, u16 FromRow, u16 FromCol, u16 ToRow, u16 ToCol, u8 Dir(0..3), u16 BombOwner, u32 BombPlaceTime, u8 Power` (`move_bomb.go:8-66`) |
| 0x0FBA | NOTIFY_PLAYER_RELIVE | arbitrator (respawn rules) | 8 B `u16 PlayerID, u32 Time, u8 Row, u8 Col` (`player_relive.go`) |
| 0x0FBB | NOTIFY_GAME_OVER | arbitrator (client-rule maps) / server | §9 |
| 0x0FBF | NOTIFY_ITEM_BEEXPLODED | arbitrator | `u16 PlayerID, u32 Time, u8 n, n×GAME_ITEM` (`item_exploded.go`) |
| 0x10E1 | NOTIFY_DISPATCH_BOMB | arbitrator (airborne bubbles, special maps) | `u16 PlayerID, u32 Time, u8 nB, nB×{u32 SceneID, u8 Prop, u8 Row, u8 Col}, u8 nI, nI×{u32 ItemID, u8 Row, u8 Col}` (`dispatch_bomb.go`) |
| 0x10E2/0x10E3 | REQUEST_EAT_BOMB / NOTIFY_PLAYER_EAT_BOMB | TCP request; server generates notify | see `eat_bomb` codec |
| 0x1166 | NOTIFY_PLAYER_TRAPPED | box rule only | — |

Rule-specific families (machines 0x10EF–0x10F3/0x115D, boxes 0x115E–0x1168, buns 0x0FB6–0x0FB9, tanks 0x15B3, Boss/NPC 0x1169–0x116B, 0x10F4 harm) are out of scope for ordinary elimination maps.

Note the TCP REQUEST_PLAY wrapper carries the schema as **LE u32**, while QQT_MSG_DATA.DataID is BE u32 — same numeric id, different byte order.

### 6.6 PLAYER_MOVE 0x0FA2 (`movement_event.go:8-118`)
Body = 7 + 22×n bytes; **parallel arrays**, not interleaved:

| Off | Size | Field |
|---|---|---|
| 0 | 2 | PlayerID (author) |
| 2 | 1 | n (≤16) |
| 3 | 4 | SeqReply (server AI writes 0) |
| 7 | 2n | PlayerIDs[n] |
| 7+2n | 20n | MoveSeq[n] |

PLAYER_MOVESEQ (20 B): `u16 Sequence, u32 TimeStamp(scene ms), u16 CurX, u16 CurY, u16 CornerX, u16 CornerY, u16 EndX, u16 EndY, u8 WalkAndDirection, u8 Speed`.

* `WalkAndDirection`: bits 0–1 direction (0 right, 1 up, 2 left, 3 down); bit 4 (0x10) = moving/held; bit 5 (0x20) = forced flush (stop/hit/action checkpoint) (`competitive_ai_runtime.go:1468-1475, 1554-1580, 2551-2552`).
* `Sequence` increments at each "segment boundary" (start/stop, direction change, speed change); unchanged while the same key is held.
* `Cur*` = current pixel position; `End*` = collision-limited end of the held segment (= Cur when stopped); `Corner*` = turning corner if the path has one, else 0.
* Emission cadence (native, mirrored by AI): on every boundary/path change plus a heartbeat every 150 ms while holding (`competitive_ai_runtime.go:39-42, 1449-1452`). Native clients don't move before scene time 3000.
* `Speed`: native "speed rate" byte (role speed + shoes); **[uncertain]** exact pixel/ms mapping — see `battleengine.NativeEffectiveSpeedRate`.

Absolute/delta compact movement (`0x10E7`, `0x10E9`, `0x10EA`) and upstream `0x10EB` exist as codecs (`movement_transport.go:57-93`) but nothing in the live path uses them.

### 6.7 Who sends what (competitive, ordinary elimination map)

You **send** on Type-2 (targets = all other participants incl. AIs):
* 0x0FA2 your movement; 0x0FA3 your bubble placement; 0x0FA5 when you are hit (`IsAvatar=0` → syrup/trapped); 0x0FA7 when your syrup times out; 0x0FA8/0x0FAA when you touch a trapped enemy/teammate; 0x0FAC when you touch an item; 0x0FB4 kick.
* If **arbitrator**: additionally 0x0FA4 (all explosions, including AI bubbles), 0x0FA9, 0x0FAB, 0x0FAD, 0x0FAE (bird supply from GAME_BEGIN.Items), 0x0FBF, 0x139C, 0x0FBA, rule-specific notifies, and 0x0FBB GAME_OVER on client-rule maps. Requests from non-arbitrator players should be addressed only to the arbitrator (server filters request-only fast uploads to the arbitrator, `room_fast_dispatch.go:278-326`).

You **receive** on Type-2: all of the above from peers, and from server-owned AIs: 0x0FA2 movement, 0x0FA3 bubbles, 0x0FAC pickup requests (retry ≤3×, ≥1 s apart), 0x0FA5 hits, 0x0FA8/0x0FAA requests, 0x0FA7 syrup-timeout death (`competitive_ai_runtime.go:1666-1699, 1825-1890, 2063-2075, 2130-2160`). AIs **depend on the arbitrator** to emit 0x0FA4/0x0FAD/0x0FA9/0x0FAB for them; the server validates a virtual target's 0x0FA5 only if the arbitrator mirrors it on TCP and it matches an exact pending AI request (`game_event_dispatch.go` 0x0FA5 case, lines 735-790 of the file).

---

## 7. TCP room-fast upload (command 0x0065 variant)

`internal/protocol/game/room_fast_packet.go:12-231`; `internal/server/probe/room_fast_dispatch.go:60-170, 769-809, 1132-1191`

Recognised by `packet[16]==1` and an ST of length `3+2N` whose first u16 is 0x0065 (`room_fast_packet.go:87-100`).

| Off | Size | Field |
|---|---|---|
| 0 | 4 | total length |
| 4 | 4 | outer seq (0) |
| 8 | 2 | route seq |
| 10 | 2 | 0xFFFF (enforced) |
| 12 | 4 | UIN (must equal session UIN) |
| 16 | 1 | 0x01 |
| 17 | 1 | STlen = 3 + 2N (N 1..8) |
| 18 | 2 | 0x0065 |
| 20 | 1 | N |
| 21 | 2N | recipient PlayerIDs (unique, non-zero, not you) |
| 18+STlen | rest | QQ-TEA(LocalKey) plaintext |

Plaintext: 14-byte inner header with **command=0, flags=0, innerSeq = route seq, route=1, marker 0xFFFF, section=1** (all enforced, `room_fast_packet.go:165-173`), then:

| Off | Size | Field |
|---|---|---|
| 0 | 1 | count (1..64) |
| 1 | var | entries: `u32 GameTime, u32 Flag, u16 Length, <Length bytes>` |

In a match each entry is a 0x0FBD package (§6.3); in the waiting room it is ROOM_MSG_DATA (0x0BB8 move / 0x0BB9 bomb, little-endian bodies; not used in matches). Mixing kinds is rejected.

Server behaviour during a match: requires room phase InMatch and `GameID == active game`; each package's PlayerID must be you (or an authorized Boss/NPC proxy); then dedupes/accounts (0x0FAD pickups, 0x0FA7/0x0FA9 relay dedupe, 0x0FA5 trap accounting, durability deaths) and **returns without relaying anything and without a TCP reply** (`room_fast_dispatch.go:143-160`). It can end the match on rule 7/8 durability deaths or Boss kills (`competitiveFastSettlementAfterUnlock`, `:586-618`).
It is therefore a reliable *mirror* of the Type-2 batch; the native client sends it in parallel with Type-2 **[native]**. For ordinary maps the authoritative trap/kill/death accounting is driven by the 0x0083 path (§8) — the fast path only duplicates trap accounting. Sending it is optional for correctness on elimination maps **[inferred]**, but recommended for fidelity.

Captures: waiting-room move `room_fast_packet_test.go:10`, bomb `:11`, in-match 0x0FBD `:12` (UIN 1000002, recipients [1], package = the 0x0FA3 vector in §6.3).

---

## 8. In-match TCP commands

### 8.1 REQUEST_PLAY / GameEventRequest 0x0083 (C→S)
`game_event.go:202-227, 278-320`; dispatcher `game_event_dispatch.go:107-696`

Payload:

| Off | Size | Field |
|---|---|---|
| 0 | 4 | UIN (= envelope UIN) |
| 4 | 4 | ClientTag (opaque; captures look like a tick count, e.g. 0x00790b6d) |
| 8 | 2 | GameData length L (4..4096) |
| 10 | 4 | schema id, **LE u32** (must be in allow-list `game_event.go:299-320`) |
| 14 | L-4 | body (same as Type-2 body) |

Server reply: on success a **correlated echo**: command 0x0083, identical payload (`game_event.go:337-358`). On any validation error: **no reply at all** (only a log line, `game_event_dispatch.go:634-648`). Do not block on the ACK.

For mirror schemas (`game_event_dispatch.go:50-105`: 0x0FA4, 0x0FA5, 0x0FA7, 0x0FA8–0x0FAE, 0x0FBA, 0x0FBF, 0x10E1, 0x139C, …) the server never re-broadcasts; Type-2 is the only scene delivery. Important per-schema effects:

| Schema | Who may send | Server effect |
|---|---|---|
| 0x0FA3 | the placing player | ACK only (Boss/NPC actor: also 0x138B broadcast) |
| 0x0FA4 | anyone (arbitrator in practice) | ACK; sculpture accounting |
| 0x0FA5 | the hit player or an authorized observer; AI targets: **arbitrator only** with matching pending request | `IsAvatar=0` → `RecordTrapped`; AI → commit hit |
| 0x0FA7 | the dying player itself, or arbitrator for others | `RecordDeath`; if one team remains (elimination maps) → **GAME_OVER** after 750 ms (`competitive_death_dispatch.go:28-125`) |
| 0x0FA8 | source must be you | `RecordKill` (kill credit + elimination) → may conclude → GAME_OVER (`game_event_dispatch.go:793-858`) |
| 0x0FAA/0x0FAB | source you | `RecordRescue` |
| 0x0FA9 | **arbitrator only** | dedupe, kill accounting, may conclude |
| 0x0FAC | you (or proxy) | ACK only |
| 0x0FAD | **arbitrator only** | pickup accounting (rewards/treasure) |
| 0x0FAE | arbitrator only | ACK |
| 0x0FBA | arbitrator only | `RecordRespawn` |
| 0x0FAF | you | server consumes and pushes 0x0FB0 |
| 0x10E2 | you | server pushes 0x10E3 to room |
| 0x1174 | you | prepared prop; push 0x1175 + inventory 0x00D9 |
| 0x0FBB | arbitrator (if rule requires) | §9.2 |

### 8.2 GameEventNotify 0x0084 (S→C)
`game_event.go:110-118, 229-273`. Payload (NOTIFY_GAME_EVENT 0x041B):

| Off | Size | Field |
|---|---|---|
| 0 | 2 | RoomID |
| 2 | 4 | GameDataSequence (server-global counter, non-zero) |
| 6 | 4 | GameData length L (u32!) |
| 10 | 4 | schema **LE u32** |
| 14 | L-4 | body |

Vector (`game_event_test.go:120-134`): request payload `000f4241 00790b6d 000f a70f0000 0001 0000f85b 012c 00bf 00` → notify `0001 00000001 0000000f a70f0000 0001 0000f85b 012c 00bf 00` (player 1 dies at (300,191) px).

In an ordinary competitive match you receive 0x0084 for: 0x0FBB GAME_OVER, 0x0FB1 arbitrator change, 0x10EC player leave, 0x0FB0 use-item, 0x10E3 eat-bomb, 0x1175 prepared prop, 0x1159 passive-function projections at start, Boss/NPC creation on Boss maps.

### 8.3 ItemStatusChange 0x0085
`item_status.go:9-90`. Room-UI (carried items), not in-match. Request: `u32 UIN, u32 ClientTime, u16 count, count×{u32 ItemID, u8 NewStatus, u8 NewRoleID}`. Response payload `00 00`.

### 8.4 Others seen during/after a match
`0x00D9 PlayerItemAddNotify` (route 2/section 1; `u32 UIN, u32 Time, u32 SrcUIN, u32 CommodityID, u16 n, n×ITEM_INFO(18 B)`, `item_update.go`), `0x0094 GameMoneyNotify`, `0x008D/0x008E` AI enter/leave, `0x008C` lobby push, `0x0093` room chat (rejections).

---

## 9. Match end

### 9.1 NOTIFY_GAME_OVER body 0x0FBB (`game_result.go:96-205`, `enums.go:149-172`)

| Off | Size | Field |
|---|---|---|
| 0 | 4 | Time (scene ms / result time) |
| 4 | 1 | result count (1..8) — must equal participant count |
| 5 | var | results |
| last | 1 | GameMode (0 competitive, 2 adventure) |

Each result (12 + 8k bytes): `u16 PlayerID, u8 Result (0 loss, 1 draw, 2 win), u32 Remark, u32 Point, u8 k(≤4), k×u32 FieldValue1 (counts), k×u32 FieldValue2 (scores)`. Field 0 = kills, 1 = rescues, 2 = reward, 3 = mode-specific (`game_result.go:14-24`). The server fills statistics and base points (`competitive_death_dispatch.go:127-146`).

Delivered as 0x0084 with schema 0x0FBB to every room member still in the match. When it results from a 0x0083 you sent, you get the ACK first, then (after 750 ms; 10 s for Boss victory) the GAME_OVER (`competitive_death_dispatch.go:11-14`, `tcp_sequence.go:82-93`).

### 9.2 Who decides
* **Ordinary elimination maps** (`UsesServerElimination`): the server concludes when only one team has living players, from 0x0FA7 (death), 0x0FA8 (kill request) or 0x0FA9 (arbitrator kill notify) on TCP 0x0083, or from a departure. So every client **must** mirror its own 0x0FA7 (and its 0x0FA8) on TCP 0x0083; the arbitrator must mirror 0x0FA9. (`match_start_dispatch.go:395-398`, `competitive_death_dispatch.go`, `game_event_dispatch.go:793-930`.)
* **Client-rule maps** (buns, treasure, sculpture, machine, box, tank, Boss overlays): the arbitrator's client sends 0x0FBB on TCP 0x0083. The server validates (result count = participants, arbitrator only when required), normalizes team outcome/draw, applies its own statistics, sends you a correlated 0x0083 response containing the **rewritten** body, and broadcasts it as 0x0084 (`match_game_over_dispatch.go:24-116`). A late client 0x0FBB after the server already concluded is ignored without reply.
* **Timeout**: after `RoundDurationMS` wall time the server pushes 0x0FBB (Time = RoundDurationMS) to all members; ordinary result is draw unless a winner is determinable (`competitive_battles.go:80-128`).
* **Departure** (TCP close, LeaveRoom 0x0069): leaver gets a loss; if it leaves one team standing the others get GAME_OVER; if the arbitrator left, peers get 0x0FB1 with the new arbitrator (`competitive_death_dispatch.go:163-260`, `game_event.go:546-560`).

### 9.3 Return to the room
After sending GAME_OVER the server (`match_settlement.go:269-363`): persists settlement, broadcasts `LeaveRoomNotify 0x008E` for each virtual AI (with NewRoomOwnerID, arbitrator, MapID), clears CurrentGameID, room phase → Preparing, lobby push, then per player `GameMoneyNotify 0x0094` (if money reward) and `PlayerItemAddNotify 0x00D9` inventory refresh. There is **no** explicit "back to room" message and **no re-enter**: the client shows the result screen on 0x0FBB and returns to its existing room UI by itself **[native]**. Ready flags: **[uncertain]** whether the server resets per-member ready state; check `internal/game/room` FinishSettlement/CompleteMatch if your UI relies on it. After completion, further 0x0083/0x0065 for that game are rejected (no active game).

---

## 10. Test vectors (verbatim from tests)

| What | Hex | Location |
|---|---|---|
| StartGame plaintext (inner hdr + payload) | `00820000000000dd0003ffff0001000f42410f45940d` | `internal/protocol/game/start_game_test.go:10` |
| RequestUDPOK mode 0 (full TCP packet) | `00000062000000000e36ffff000f42420120202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3fa73522bc33062bb414ebd178f92369691d98fd1ad80bca49c341fb7a40cf14d46c34c00eac75de80a4ae614ae75be7d4` → src 1000002, time 0x1AF6D415, dst player 1/UIN 1000001, Info `00` | `udp_ok_test.go:13` |
| RequestUDPOK mode 1 | `00000062000000000c84ffff000f42410120202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f1d4dfc265749da8ff9a766b09764b156ad2610aaaab30b06f7dc9d095d4b02eb3ed725eba1e713e7318df8887252eda1` → Info `01000f42420003` | `udp_ok_test.go:27` |
| NotifyUDPOK payload | `000f42411af6d4150001000f4242000100` | `udp_ok_test.go:69` |
| UDP Type-1 presence | `001800000000000001000f42410001afe006c0a83801d445` | `room_peer_udp_test.go:11` |
| UDP Type-3 room-peer | `001e00000000000001000f424100030f7a0c0002000f4242c0a83801f686` | `room_peer_udp_test.go:32` |
| Type-3 before/after endpoint rewrite (checksum test) | `001e0000000cf90001000f4241000336960c0002000f4242c0a83801e152` → `001e0000000cf90001000f42410003df0f0c0002000f42427f000001e152` | `room_peer_udp_test.go:81,91` |
| Type-3 (player 2 → 1) | `001e00000000000002000f4242000312780c0001000f4241c0a83801f685` | `room_peer_udp_test.go:121` |
| UDP Type-2 multicast | `005000000000010002000f4242000221bc060001000f4241ab4714790feafddafe3d76c2ba849fd15d02307b0843ac887c61580ae66348f32fe2b36b0e302dc1babb1e2e7c6cb3e7dc7601262f5c9295` (56-byte BatchData) | `room_peer_udp_test.go:56` |
| 15-byte peer handshake (0x21) | `000f21e93c000f42417f0000014650` | `room_peer_handshake_test.go:9` |
| Type-2 BatchData ciphertext, 1 PLAYER_MOVE (game 16, player 2, index 1) | `55a8ae52…cf2702` (full string in file) | `room_peer_batch_test.go:9` |
| Type-2 BatchData, 2 PLAYER_MOVE entries (game 1, player 1) | `983db1d7…eaa419` | `room_peer_batch_test.go:82` |
| 0x0FBD with PLAYER_USE_BOMB | `000200006bc90000000101000000b000006bc900000fa3000f4b0004cd000200006bc9f20c7fff0412000400ffff006500000000` | `movement_transport_test.go:23` |
| PLAYER_MOVE synthetic (2 entries, parallel arrays) | `0001020102030400020003110111111102110311041105110611071108a1a2220122222202220322042205220622072208b1b2` | `movement_event_test.go:9` |
| TCP room-fast in-match upload | `0000006f00000000002fffff000f424201050065010001a344…5e61f4c2` | `room_fast_packet_test.go:12` |
| TCP room-fast waiting-room move / bomb | `capturedRoomMovePacket`, `capturedRoomBombPacket` | `room_fast_packet_test.go:10-11` |
| REQUEST_PLAY payloads (death, NPC death, next-map) | `000f424111d8b347000fA70f000000010000beeb062b00b400` etc. | `game_event_test.go:46-60` |
| REQUEST_PLAY death → NOTIFY_GAME_EVENT | `000f424100790b6d000fa70f000000010000f85b012c00bf00` → `0001000000010000000fa70f000000010000f85b012c00bf00` | `game_event_test.go:120,134` |
| QQ-TEA / checksum self-tests | `internal/protocol/qqtea/qqtea_test.go` | |

Keys: TCP QQ-TEA key `101112131415161718191a1b1c1d1e1f`; Type-2 BatchData key ASCII `qqt is the best!`. (I did not decrypt the ciphertext vectors; the decrypted expectations are the assertions in the cited tests.)

---

## 11. Minimum viable client checklist (competitive, ordinary map)

1. Owner sends 0x0082; everyone handles 0x0089 (parse §4), plus any 0x008D for AIs first.
2. Open one UDP socket; send Type-1 to server UDP port, wait for the reply (observed endpoint).
3. For each other participant (humans and AIs): 0x0086 Info `[0]`; Type-3 to that peer every 3 s until answered; then 0x0086 Info `[1, peerUIN, 0x0003]` (mirrors the captured descriptor; **[uncertain]** route semantics). Answer incoming 0x0087/Type-3/0x20 from native peers likewise.
4. Run the simulation from scene time 0; accept input from 3000 ms.
5. Send your facts on Type-2 to all other participants; mirror trap/death/kill-request (0x0FA5/0x0FA7/0x0FA8) and, if arbitrator, every notify you produce, on TCP 0x0083.
6. If arbitrator, implement explosion resolution, item pickup arbitration, kill/save arbitration, bird dispatch, and answer AI requests.
7. Send Hello 0x0088 at least every few minutes.
8. On 0x0084/0x0FBB show results, drop UDP match state, stay in room; expect 0x008E (AI leave), 0x0094, 0x00D9.

## 12. Biggest uncertainties

* Native SpawnSeed/ItemSeed algorithms (spawn cells, wall-item positions, bird schedule) — required for a peer-consistent simulation with native clients; not in the protocol package.
* Exact semantics of the 0x0086 transport descriptor route value (captures show `0x0003`) and whether native peers refuse to send Type-2 to a client that skips Type-3/0x20 negotiation (server relay itself does not require it).
* `Speed` byte units, `PLAYER_USE_BOMB.Power` vs reach (AI uses reach+1), `BombID` meaning (appearance id; explosion record uses a 1-byte "BombID/style").
* Meaning of QQT_MSG_DATA.Sequence/GameTime in native captures (e.g. 0x4B0004CD, 0xFFFF0065) — the server AI copies them but nothing validates them.
* Whether the native client requires the 0x0065 fast mirror; server treats it as accounting only.
* Room ready-state reset after settlement.
