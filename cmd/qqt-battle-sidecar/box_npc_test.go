package main

import (
	"io"
	"math/rand"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func npcTestMatch(t *testing.T, local, arb uint16) *onlineMatch {
	t.Helper()
	grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 195)}
	for i := range grid.Cells {
		grid.Cells[i].FlamePassable = true
	}
	rules := onlineRules(local, arb)
	rules.NativeBoxPhysics = true
	rules.ExternalConclusion = true
	rules.RoundDurationMS = 240000
	rules.BombFuseMS = 3000
	rules.FlameDurationMS = 500
	rules.ActorHalfSizePixels = 19
	ps := []battleengine.Participant{{PlayerID: 1, TeamID: 1, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: 1}, SpeedPixelsPerSecond: 80, BombCapacity: 2, BombPower: 2}, {PlayerID: 2, TeamID: 2, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 11, Col: 13}, SpeedPixelsPerSecond: 80, BombCapacity: 2, BombPower: 2}}
	engine, err := battleengine.New(battleengine.Config{Seed: 1, Grid: grid, Rules: rules, Participants: ps})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := battleengine.NewRuntime(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := mapdata.CompetitiveMap{ID: 1401, Rule: mapdata.CompetitiveRuleBox, NativeRule: 8, SpawnGroupA: []mapdata.CompetitiveCell{{Row: 1, Col: 1}, {Row: 3, Col: 3}}, SpawnGroupB: []mapdata.CompetitiveCell{{Row: 11, Col: 13}, {Row: 3, Col: 3}, {Row: 9, Col: 11}}}
	return &onlineMatch{engine: engine, runtime: runtime, local: local, arbitrator: arb, localUIN: 1000003, log: io.Discard, begin: game.GameBeginData{GameID: 1, MapID: 1401, Players: []game.PlayerGameInfo{{PlayerID: 1, TeamID: 1}, {PlayerID: 2, TeamID: 2}}}, uins: map[uint16]uint32{1: 1000003, 2: 1000004}, bombs: map[uint32]wireBomb{}, collected: map[pickupKey]uint16{}, departed: map[uint16]bool{}, pickupRequests: map[pickupKey]pickupRequest{}, box: &boxRule{entry: entry, rng: rand.New(rand.NewSource(1)), objects: map[uint32]*boxObject{}, elements: map[uint32]mapdata.NativeBoxElement{}, seen: map[[3]uint32]bool{}, npc: &boxNPCState{}}}
}
func npcInfo(id uint16, role byte, row, col uint16) game.CreateNPCBoss {
	return game.CreateNPCBoss{Bosses: []game.CompetitiveBossInfo{{BossID: id, RoleID: role, TeamID: 7, AIType: 2, Row: row, Col: col, OutfitItems: []game.BossItemInfo{{ItemID: 500, ItemCount: 3}, {ItemID: 501, ItemCount: 2}}}}}
}
func addTestNPC(t *testing.T, m *onlineMatch, id uint16, role byte) *boxNPCActor {
	t.Helper()
	if err := m.createBoxNPC(npcInfo(id, role, 5, 5), m.engine.ElapsedMS()); err != nil {
		t.Fatal(err)
	}
	return m.box.npc.actors[id]
}

func TestBoxNPCSpawnWaitsForClockAndRegistrationACK(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	m.tryCreateBoxNPC(60000)
	if m.box.npc.pending != nil {
		t.Fatal("spawned at exactly60s")
	}
	m.tryCreateBoxNPC(60020)
	s := m.box.npc
	if s.pending == nil || s.living() != 0 || len(s.outbox) != 0 || m.engine.HasRuleNPCOwner(20001) {
		t.Fatal("NPC became live before reliable registration")
	}
	if len(s.candidates) != 4 {
		t.Fatalf("spawn pool did not deduplicate A/B: %+v", s.candidates)
	}
	packets := mirrored(t, m, game.CreateNPCBossEvent)
	if len(packets) != 1 {
		t.Fatal("missing registration request")
	}
	created, err := game.ParseCreateNPCBossNetwork(packets[0].Body)
	if err != nil || created.Time != 0 || len(created.Bosses) != 1 || created.Bosses[0].BossID != 20001 {
		t.Fatalf("native creation %+v %v", created, err)
	}
	m.boxNPCAcknowledged(packets[0])
	if s.living() != 1 || len(m.engine.Actors()) != 2 || !m.engine.HasRuleNPCOwner(20001) {
		t.Fatal("registered NPC not installed independently")
	}
	if len(s.outbox) < 2 || s.outbox[0].author != 1 || s.outbox[0].message.DataID != game.CreateNPCBossEvent || s.outbox[1].author != 20001 || s.outbox[1].message.DataID != uint32(game.PlayerMoveSchema) {
		t.Fatal("creation is not ordered before first NPC proxy movement")
	}
	count := len(s.outbox)
	m.boxNPCAcknowledged(packets[0])
	if len(s.outbox) != count || s.living() != 1 {
		t.Fatal("duplicate ACK recreated NPC")
	}
}
func TestBoxNPCReceiverCreatesBeforeProxyAndUsesCellCoordinates(t *testing.T) {
	m := npcTestMatch(t, 2, 1)
	data := npcInfo(20001, 26, 4, 7)
	body, _ := data.MarshalNetworkBinary()
	if m.boxNPCProxy(1, 20001) {
		t.Fatal("unknown NPC proxy accepted")
	}
	m.boxNPCRemote(1, game.GameEvent{Schema: game.CreateNPCBossEvent, Body: body})
	n := m.box.npc.actors[20001]
	if n == nil || n.pos != (battleengine.Position{X: 300, Y: 180}) || n.capacity != 2 || n.power != 2 || n.speed != 2 {
		t.Fatalf("NPC attributes/position %+v", n)
	}
	if !m.boxNPCProxy(1, 20001) || m.boxNPCProxy(2, 20001) {
		t.Fatal("NPC proxy does not follow current arbiter")
	}
	m.boxNPCRemote(1, game.GameEvent{Schema: game.CreateNPCBossEvent, Body: body})
	if m.box.npc.living() != 1 {
		t.Fatal("creation retry doubled NPC")
	}
}
func TestBoxNPCBombRequiresEscapeAndDeduplicatesTCPUDP(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 27)
	if !m.npcPlace(n, 100) {
		t.Fatal("open-field NPC did not find safe bubble escape")
	}
	if len(m.engine.Bombs()) != 1 || len(n.navigator.path.nodes) == 0 {
		t.Fatal("bubble placement did not retain escape plan")
	}
	peer := npcTestMatch(t, 2, 1)
	addTestNPC(t, peer, 20001, 27)
	body, _ := (game.PlayerUseBombEvent{PlayerID: 20001, ClientTime: 100, BombID: 1, Row: 5, Column: 5, Power: 4}).MarshalNetworkBinary()
	peer.boxNPCRemote(20001, game.GameEvent{Schema: game.PlayerUseBomb, Body: body})
	peer.boxNPCRemote(1, game.GameEvent{Schema: game.NotifyPlayerUseBomb, Body: body})
	if len(peer.engine.Bombs()) != 1 {
		t.Fatal("reliable138B duplicated proxy0FA3 bubble")
	}
	blocked := npcTestMatch(t, 1, 1)
	trapped := addTestNPC(t, blocked, 20001, 25)
	for _, c := range []battleengine.Cell{{Row: 4, Col: 5}, {Row: 5, Col: 6}, {Row: 6, Col: 5}, {Row: 5, Col: 4}} {
		blocked.engine.SetNativeBoxCell(c, battleengine.Tile{Kind: battleengine.CellSolid, MapElementOccupied: true})
	}
	if blocked.npcPlace(trapped, 100) {
		t.Fatal("boxed-in NPC placed a bubble without escape")
	}

	// Capacity is separately enforced even when a second path is clear.
	n.capacity = 1
	if m.npcPlace(n, 120) {
		t.Fatal("NPC exceeded own bubble capacity")
	}
}

func TestBoxNPCInitialNavigatorAndBlockedSubpixelEndpoint(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	if !n.navigator.wander.valid || n.navigator.wander.cell != n.pos.Cell() || n.navigator.wander.direction != 3 || n.navigator.lastThink != n.lastTick {
		t.Fatalf("native NPC birth cache not initialized: %+v", n.navigator)
	}
	position := npcFollowPosition{float32(n.pos.X), float32(n.pos.Y)}
	direction := n.navigator.choose(npcNavigationFrame{position: &position, facing: 3, now: n.lastTick + 500, mask: 15,
		random: func() uint32 { t.Fatal("birth cache consumed random value"); return 0 }})
	if direction != 3 {
		t.Fatalf("birth cache direction%d", direction)
	}
	n.pos = battleengine.Position{X: 20, Y: 60}
	n.navigator.walking, n.navigator.direction = true, 2
	m.moveBoxNPC(n, 1)
	if !n.moving || !n.moveBlocked || n.moveEnd != n.pos {
		t.Fatalf("blocked remainder advertised invalid endpoint: pos%v end%v moving%v", n.pos, n.moveEnd, n.moving)
	}
	n.netMoveSet = true
	n.netMove = game.PlayerMoveSequence{CurrentPosX: 20, CurrentPosY: 60, EndPosX: 20, EndPosY: 60, WalkAndDirection: 0x12, Speed: 1}
	m.interpolateBoxNPC(n, 1000)
	if !n.moving {
		t.Fatal("blocked wire walk flag was overwritten by equal endpoint")
	}
}

func TestBoxNPCEnteringExistingBoxFlameIsTrapped(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	if _, err := m.engine.PlaceRuleNPCBomb(n.id, n.pos.Cell(), 3, 0, 0); err != nil {
		t.Fatal(err)
	}
	for len(m.engine.Flames()) == 0 {
		if m.engine.ElapsedMS() > 4000 {
			t.Fatal("NPC bubble never exploded")
		}
		if _, err := m.engine.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.engine.Step(nil); err != nil {
		t.Fatal(err)
	}
	n.lastTick = m.engine.ElapsedMS()
	n.pos = battleengine.PositionAtCellCenter(m.engine.Flames()[0].Cell)
	if m.engine.Flames()[0].ImpactAtMS >= n.lastTick {
		t.Fatal("fixture did not enter an existing flame")
	}
	m.advanceBoxNPC(n, n.lastTick)
	if !n.trapped {
		t.Fatal("NPC ignored an already-burning rule-8 cell")
	}
}

func TestBoxNPCSearchAppendsBeforeCapacityFailureAndPreservesRouteOnSearchFailure(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	old := []pathNode{{cell: n.pos.Cell(), dir: 2}, {cell: boxStep(n.pos.Cell(), 2), dir: 9, kind: 3}}
	n.navigator.path.nodes = append([]pathNode(nil), old...)
	if _, err := m.engine.PlaceRuleNPCBomb(n.id, battleengine.Cell{Row: 1, Col: 1}, 3, 0, 0); err != nil {
		t.Fatal(err)
	}
	if m.npcPlace(n, 100) {
		t.Fatal("full capacity placed a second bubble")
	}
	if len(m.engine.Bombs()) != 1 || len(n.navigator.path.nodes) <= len(old) || n.navigator.path.nodes[len(n.navigator.path.nodes)-1] != old[len(old)-1] {
		t.Fatal("capacity failure discarded the native escape append")
	}
	blocked := npcTestMatch(t, 1, 1)
	b := addTestNPC(t, blocked, 20001, 25)
	b.navigator.path.nodes = append([]pathNode(nil), old...)
	for direction := byte(0); direction < 4; direction++ {
		if err := blocked.engine.SetNativeBoxCell(boxStep(b.pos.Cell(), direction), battleengine.Tile{Kind: battleengine.CellSolid, MapElementOccupied: true}); err != nil {
			t.Fatal(err)
		}
	}
	if blocked.npcPlace(b, 100) || len(blocked.engine.Bombs()) != 0 || len(b.navigator.path.nodes) != len(old) {
		t.Fatal("failed search placed or discarded the old route")
	}
}
func TestBoxNPCSyrupTimeoutAndRespawnAreSceneState(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	m.trapBoxNPC(n, 60020)
	m.advanceBoxNPC(n, 66019)
	if n.dead {
		t.Fatal("NPC died before6s syrup timeout")
	}
	m.advanceBoxNPC(n, 66020)
	if !n.dead {
		t.Fatal("NPC syrup did not expire")
	}
	if len(mirrored(t, m, game.NotifyPlayerDieEvent)) != 1 {
		t.Fatal("NPC death was not reported once")
	}
	m.tryCreateBoxNPC(69020)
	if m.box.npc.pending != nil {
		t.Fatal("respawned at exactly3s")
	}
	m.tryCreateBoxNPC(69021)
	if m.box.npc.pending == nil || m.box.npc.pending.data.Bosses[0].BossID != 20002 {
		t.Fatal("missing next NPC identity after empty3s")
	}
	if m.engine.Terminal().Ended || len(m.engine.Actors()) != 2 {
		t.Fatal("NPC death affected participant conclusion")
	}
}
func TestBoxNPCDeathDropsSurviveKnockBeforeKill(t *testing.T) {
	m := npcTestMatch(t, 2, 1)
	n := addTestNPC(t, m, 20001, 25)
	knock, _ := (game.PlayerKnockedEvent{PlayerID: n.id, Time: 60020, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y)}).MarshalNetworkBinary()
	m.boxNPCRemote(1, game.GameEvent{Schema: game.NotifyPlayerKnocked, Body: knock})
	kill, _ := (game.PlayerKilledEvent{PlayerInteractionEvent: game.PlayerInteractionEvent{PlayerID: 1, DestinationPlayerID: n.id, ClientTime: 60020}, Items: []game.GameItem{{ItemID: 2, Row: 4, Col: 4}}}).MarshalNetworkBinary()
	for i := 0; i < 2; i++ {
		m.boxNPCRemote(1, game.GameEvent{Schema: game.NotifyPlayerKilled, Body: kill})
	}
	count := 0
	for _, p := range m.engine.Pickups() {
		if p.State == battleengine.PickupAvailable && p.SceneID == 2 {
			count++
		}
	}
	if !n.dead || count != 1 {
		t.Fatalf("late drop table lost/duplicated: dead=%t count=%d", n.dead, count)
	}
}
func TestBoxNPCPickupsKeepRule8BubbleMeaningAndTransferState(t *testing.T) {
	m := npcTestMatch(t, 2, 1)
	n := addTestNPC(t, m, 20001, 25)
	n.pickup(1, 100)
	if n.capacity != 1 || n.basic[0] != 0 {
		t.Fatal("rule8 NPC scene1 increased ordinary capacity")
	}
	n.pickup(2, 100)
	n.pickup(3, 100)
	if n.power != 3 || n.speed != 2 || n.basic[1] != 1 || n.basic[2] != 1 {
		t.Fatal("native basic attributes missing")
	}
	n.trapped = true
	n.trappedAt = 100
	n.lastTick = 100
	m.changeArbitrator(2)
	if m.box.npc.actors[20001] != n || n.trappedAt != 100 || n.power != 3 || m.box.npc.nextID != 20002 {
		t.Fatal("arbitrator promotion reset NPC state")
	}
	m.advanceBoxNPC(n, 120)
	if n.dead {
		t.Fatal("promoted NPC immediately died")
	}
}
func TestBoxNPCFutureHitClockCannotWrapTimeout(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	n.trapped = true
	n.trappedAt = 120
	n.lastTick = 0
	m.advanceBoxNPC(n, 100)
	if n.dead {
		t.Fatal("future hit clock wrapped syrup timeout")
	}
}
