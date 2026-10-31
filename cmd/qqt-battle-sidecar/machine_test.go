package main

import (
	"os"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
	"testing"
	"time"
)

func machineMatch(t *testing.T, local uint16) *onlineMatch {
	m := kickMatch(t, 1)
	m.local = local
	m.kick = nil
	entry := mapdata.CompetitiveMap{ID: 1304, Rule: mapdata.CompetitiveRuleMachine, ObjectiveCells: []mapdata.CompetitiveCell{{Row: 3, Col: 3}, {Row: 9, Col: 3}, {Row: 3, Col: 10}, {Row: 9, Col: 10}}}
	rules, _, _ := ruleOptions(entry, local, 1)
	rules.RoundDurationMS = 240000
	rules.BombFuseMS = 3000
	rules.FlameDurationMS = 500
	rules.ActorHalfSizePixels = 19
	participants := []battleengine.Participant{}
	for i, a := range m.engine.Actors() {
		p := a.Participant
		p.RoleID = uint16(i + 1)
		participants = append(participants, p)
	}
	var err error
	m.engine, err = battleengine.New(battleengine.Config{Seed: 1, Grid: m.engine.Grid(), Rules: rules, Participants: participants})
	if err != nil {
		t.Fatal(err)
	}
	m.runtime, _ = battleengine.NewRuntime(m.engine, nil)
	m.machine = newMachineRule(entry, game.GameBeginData{ItemSeed: 1, Players: []game.PlayerGameInfo{{PlayerID: 1, RoleID: 1, TeamID: 1}, {PlayerID: 2, RoleID: 2, TeamID: 2}}})
	return m
}
func TestMachineLaunchSchedulesSameBatchOnBothPeers(t *testing.T) {
	owner, peer := machineMatch(t, 1), machineMatch(t, 2)
	advanceTo(t, owner, 48000)
	advanceTo(t, peer, 48000)
	owner.arbitrateMachine(game.MachineLaunchEvent{MachineIndex: 1, FirstTeam: 1, PlayerID: 1, RoleID: 1, Time: 48000})
	states := mirrored(t, owner, game.NotifyMachineFree)
	if len(states) != 1 {
		t.Fatalf("free notifications: %d", len(states))
	}
	decoded, err := game.ParseMachineBombState(states[0])
	if err != nil || decoded.MachineIndex != 1 || len(decoded.Bombs) != 10 {
		t.Fatalf("state %+v %v", decoded, err)
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyMachineFree, Data: states[0].Body})
	if len(owner.machine.pending) != 1 || len(peer.machine.pending) != 1 || owner.machine.last != peer.machine.last {
		t.Fatal("follower did not preserve shared cooldown/pending salvo")
	}
	advanceTo(t, owner, 51000)
	advanceTo(t, peer, 51000)
	if events := owner.machineTick(); len(events) != 10 {
		t.Fatalf("created %d bubbles", len(events))
	}
	moves := mirrored(t, owner, game.NotifyMachineMove)
	if len(moves) != 1 {
		t.Fatal("no delayed move notification")
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyMachineMove, Data: moves[0].Body})
	if len(peer.engine.Bombs()) != 10 || len(peer.machine.pending) != 0 {
		t.Fatal("peer did not consume delayed batch")
	}
	for _, m := range []*onlineMatch{owner, peer} {
		for _, b := range m.engine.Bombs() {
			if b.ExplodeAtMS != 54001 || m.bombs[b.ID].placedAt != 48000 {
				t.Fatalf("launch identity confused with creation fuse: %+v %+v", b, m.bombs[b.ID])
			}
		}
	}
}
func TestMachineFollowerCanDispatchPendingSalvoAfterTakeover(t *testing.T) {
	peer := machineMatch(t, 2)
	advanceTo(t, peer, 48000)
	state := game.MachineBombState{PlayerID: 1, Time: 48000, Bombs: []game.DispatchBomb{{SceneID: 1, Prop: 8, Row: 6, Col: 12}}}
	peer.applyMachineState(game.NotifyMachineFree, state)
	advanceTo(t, peer, 51000)
	if len(peer.machineTick()) != 0 || len(peer.machine.pending) != 1 {
		t.Fatal("follower discarded or authored pending batch")
	}
	peer.arbitrator = 2
	if events := peer.machineTick(); len(events) != 1 || len(peer.machine.pending) != 0 {
		t.Fatalf("takeover failed: %+v", events)
	}
}
func TestMachinePatternsAndSharedCooldown(t *testing.T) {
	m := machineMatch(t, 1)
	rule := m.machine
	for _, test := range []struct {
		role uint16
		want uint16
	}{{1, game.NotifyMachineFree}, {2, game.NotifyMachineAngry}, {3, game.NotifyMachineGrace}, {8, game.NotifyMachineFree}} {
		if got := rule.pattern(test.role, 48000); got != test.want {
			t.Fatalf("role %d pattern %x", test.role, got)
		}
	}
	for _, schema := range []uint16{game.NotifyMachineFree, game.NotifyMachineAngry, game.NotifyMachineGrace, game.NotifyMachineCool} {
		bombs := rule.bombs(m.engine, 0, schema)
		if len(bombs) == 0 {
			t.Fatalf("empty pattern %x", schema)
		}
		if schema == game.NotifyMachineAngry && (len(bombs) != 1 || bombs[0].SceneID != 13) {
			t.Fatal("angry is not type13")
		}
		if schema == game.NotifyMachineCool && (len(bombs) != 1 || bombs[0].SceneID != 14) {
			t.Fatal("cool is not type14")
		}
		for _, b := range bombs {
			if b.Col < 8 {
				t.Fatalf("shot on friendly half: %+v", b)
			}
		}
	}
	if rule.ready(0, 47999) || !rule.ready(0, 48000) {
		t.Fatal("initial cooldown differs from48s")
	}
	rule.hits[0] = 1
	if !rule.ready(0, 47000) || rule.ready(1, 47000) {
		t.Fatal("damage did not shorten only its team cooldown")
	}
}
func TestAllMachineMapsBuild(t *testing.T) {
	root := "../../runtime/client-patched"
	if _, err := os.Stat(root); err != nil {
		t.Skip("native resources unavailable")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for id := uint32(1301); id <= 1311; id++ {
		entry, ok := catalog.CompetitiveMap(id)
		if !ok {
			t.Fatalf("missing map %d", id)
		}
		rules, _, err := ruleOptions(entry, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := battleengine.ParticipantFromNativeRole(1, 1, 1, battleengine.ParticipantHuman)
		b, _ := battleengine.ParticipantFromNativeRole(2, 2, 2, battleengine.ParticipantHuman)
		supply := entry.PlanCompetitiveSupply(123, []uint16{1, 2}, false)
		config, err := battleengine.ConfigFromCompetitiveMap(entry, battleengine.CompetitiveMapConfigOptions{SpawnSeed: 7, ItemSeed: 123, SpawnMode: battleengine.NativeSpawnModeForMap(entry, false), Rules: rules, Participants: []battleengine.Participant{a, b}, RecordedWallItems: supply.WallItems, RecordedDelayedItems: supply.DelayedItems, UseRecordedWallItems: true, AllowSpecialRules: true})
		if err != nil {
			t.Fatalf("map %d: %v", id, err)
		}
		engine, err := battleengine.New(config)
		if err != nil {
			t.Fatal(err)
		}
		rule := newMachineRule(entry, game.GameBeginData{Players: []game.PlayerGameInfo{{PlayerID: 1, TeamID: 1}, {PlayerID: 2, TeamID: 2}}})
		for _, g := range rule.gates {
			engine.SetMachineGate(g.anchor, g.side == 0, true)
			tile, _ := engine.Grid().Cell(g.trigger())
			if tile.NativeGridAttr != 0x1a1a {
				t.Fatal("gate not ready")
			}
		}
		t.Logf("map %d: %d machines, spawns %+v", id, len(rule.gates), []battleengine.Cell{engine.Actors()[0].Spawn, engine.Actors()[1].Spawn})
	}
}

func TestMachineFinalHarmAndDeathCanArriveEitherOrder(t *testing.T) {
	for _, deathFirst := range []bool{false, true} {
		m := machineMatch(t, 2)
		m.local = 3 // observe two independent sender streams
		pos := m.engine.Actors()[1].Position
		for n := uint32(0); n < 3; n++ {
			m.receiveMachineHarm(game.EntityHarmedEvent{ObjectID: 2, Time: 1000 + n*3000, PosX: uint16(pos.X), PosY: uint16(pos.Y)})
		}
		harm := game.EntityHarmedEvent{ObjectID: 2, Time: 10000, PosX: uint16(pos.X), PosY: uint16(pos.Y)}
		body, _ := (game.PlayerDeathEvent{PlayerID: 2, ClientTime: 10000, PosX: uint16(pos.X), PosY: uint16(pos.Y), Items: []game.GameItem{}}).MarshalNetworkBinary()
		die := func() { m.applyRemote(1, game.BattleMessageData{DataID: game.NotifyPlayerDieEvent, Data: body}) }
		if deathFirst {
			die()
			m.receiveMachineHarm(harm)
		} else {
			m.receiveMachineHarm(harm)
			die()
		}
		if a := m.engine.Actors()[1]; a.State != battleengine.ActorEliminated || a.DurabilityLeft != 0 || m.machine.hits[1] != 4 {
			t.Fatalf("deathFirst=%t: state %v layers %d hits %d", deathFirst, a.State, a.DurabilityLeft, m.machine.hits[1])
		}
	}
}
func TestMachineRejectedHarmDoesNotConsumeCooldownOrReportDeath(t *testing.T) {
	m := machineMatch(t, 1)
	pos := m.engine.Actors()[1].Position
	first := game.EntityHarmedEvent{ObjectID: 2, Time: 1000, PosX: uint16(pos.X), PosY: uint16(pos.Y)}
	m.receiveMachineHarm(first)
	m.takeTCP()
	first.Time = 1001
	m.receiveMachineHarm(first)
	first.Time = 4000
	first.PosX = 65000
	m.receiveMachineHarm(first)
	if m.machine.hits[1] != 1 || len(mirrored(t, m, game.NotifyPlayerDieEvent)) != 0 {
		t.Fatal("rejected harm changed cooldown/death accounting")
	}
}

func TestMachineBotRoutesAroundHorizontalGate(t *testing.T) {
	m := machineMatch(t, 1)
	gate := battleengine.Cell{Row: 6, Col: 6}
	m.engine.SetMachineGate(gate, true, true)
	world := newAutoplayWorld(m.engine)
	start, goal := battleengine.Cell{Row: 7, Col: 6}, battleengine.Cell{Row: 6, Col: 5}
	path := world.nearest(start, func(battleengine.Cell) bool { return true }, func(c battleengine.Cell) bool { return c == goal })
	if len(path) != 3 || path[1] != (battleengine.Cell{Row: 7, Col: 5}) {
		t.Fatalf("bot tried entering a horizontal door from below: %+v", path)
	}
}

func TestMachinePlayerThrowUsesNativeTargetAnd1159(t *testing.T) {
	owner, peer := machineMatch(t, 1), machineMatch(t, 2)
	from := battleengine.Cell{Row: 6, Col: 2}
	target, distance, ok := machineThrowTarget(owner.engine, from, battleengine.DirectionRight)
	if !ok || target != (battleengine.Cell{Row: 6, Col: 9}) || distance != 7 {
		t.Fatalf("target %v distance %d valid %t", target, distance, ok)
	}
	target, distance, ok = machineThrowTarget(owner.engine, from, battleengine.DirectionDown)
	if !ok || target != (battleengine.Cell{Row: 0, Col: 2}) || distance != 7 {
		t.Fatalf("vertical target %v distance %d valid %t", target, distance, ok)
	}
	action := game.KickBombAction{BombRowAndCol: 0x62, BombDestRowAndCol: 0x69, Direction: 0, Power: 3, BombPower: 3, Time: 5000, PlayerID: 1}
	body, _ := action.MarshalNetworkBinary()
	a := owner.applyMachineThrow(action)
	b := peer.applyRemote(1, game.BattleMessageData{DataID: game.PlayerThrowBomb, Data: body})
	if len(a) != 1 || len(b) != 1 || owner.engine.Bombs()[0] != peer.engine.Bombs()[0] {
		t.Fatal("throw did not mirror to peer")
	}
	if len(peer.applyRemote(1, game.BattleMessageData{DataID: game.PlayerThrowBomb, Data: body})) != 0 {
		t.Fatal("throw duplicated")
	}
}

func TestMachineCoolPermanentlyRemovesReceivingSideEquipment(t *testing.T) {
	owner, peer := machineMatch(t, 1), machineMatch(t, 2)
	explosion := game.BombExplodeEvent{PlayerID: 1, ClientTime: 130000, Bombs: []game.ExplodedBomb{{PlayerID: 1, BombID: 14, Row: 6, Column: 12}}}
	for _, m := range []*onlineMatch{owner, peer} {
		for _, g := range m.machine.gates {
			m.engine.SetMachineGate(g.anchor, g.side == 0, true)
		}
		m.machineExplosion(explosion)
		m.machineTick()
		if !m.machine.destroyed[1] || m.machine.destroyed[0] || m.machine.ready(1, 200000) {
			t.Fatal("wrong side destroyed or ready again")
		}
		for _, g := range m.machine.gates {
			if g.side == 1 {
				tile, _ := m.engine.Grid().Cell(g.anchor)
				if tile.MapElementOccupied || tile.Kind != battleengine.CellOpen {
					t.Fatal("destroyed gate was reconstructed")
				}
			}
		}
	}
	peer.arbitrator = 2
	peer.machineTick()
	if !peer.machine.destroyed[1] {
		t.Fatal("takeover restored destroyed equipment")
	}
}

func TestMachineChargeReadyPeriodAndConsumption(t *testing.T) {
	m := machineMatch(t, 1)
	rule := m.machine
	rule.updateCharges(m.engine, 3000, 1)
	charge := rule.charges[1]
	if charge.count != 1 || charge.last != 2000 {
		t.Fatalf("Ready did not charge once: %+v", charge)
	}
	charge.count--
	rule.updateCharges(m.engine, 3999, 1)
	if charge.count != 0 || charge.last != 2000 {
		t.Fatal("spending reset refill phase")
	}
	rule.updateCharges(m.engine, 4000, 1)
	if charge.count != 1 || charge.last != 4000 {
		t.Fatal("refill did not hit exact period")
	}
	rule.updateCharges(m.engine, 20000, 1)
	if charge.count != 2 {
		t.Fatal("a long frame must refill only one shot")
	}
	for clock := uint32(22000); clock <= 40000; clock += 2000 {
		rule.updateCharges(m.engine, clock, 1)
	}
	if charge.count != 5 || charge.last != 40000 {
		t.Fatal("full magazine did not retain its rolling phase")
	}
	for n := 0; n < 6; n++ {
		if n > 0 {
			if _, err := m.engine.ApplyVerifiedPickup(1, battleengine.SceneBombCapacitySmall); err != nil {
				t.Fatal(err)
			}
		}
		a, _ := actorByID(m.engine.Actors(), 1)
		if got, want := machineChargePeriod(a), uint32(2000-min(5, n)*200); got != want {
			t.Fatalf("pickup count %d period %d want%d", n, got, want)
		}
	}
}
func TestMachineThrowOriginsAndDiagonalSpeed(t *testing.T) {
	from := machineThrowOrigin(battleengine.Cell{Row: 6, Col: 2}, battleengine.DirectionRight)
	if from != (battleengine.Cell{Row: 5, Col: 3}) {
		t.Fatalf("origin %+v", from)
	}
	if got := machineFlightTime(from, battleengine.Cell{Row: 6, Col: 9}, 0); got != 487 {
		t.Fatalf("diagonal flight %dms want487", got)
	}
	if from := machineThrowOrigin(battleengine.Cell{Row: 0, Col: 14}, battleengine.DirectionRight); from != (battleengine.Cell{Row: 0, Col: 14}) {
		t.Fatalf("boundary origin %+v", from)
	}
}

func TestMachineBotSeeksAFiringLineInsteadOfThrowingPastOpponent(t *testing.T) {
	m := machineMatch(t, 1)
	m.engine.ApplyVerifiedMovementCheckpoint(1, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 6, Col: 2}))
	m.engine.ApplyVerifiedMovementCheckpoint(2, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 4, Col: 12}))
	m.machine.charges[1] = &machineCharge{count: 1}
	bot := newAutoplayBot(1)
	bot.machine = m.machine
	action := bot.act(m.engine)
	if action.PlaceBomb || action.Move != battleengine.DirectionUp {
		t.Fatalf("bot failed to find an attacking firing line: %+v", action)
	}
}

func TestMachineFinalHitStopsProducersWhileKeepingDeathReport(t *testing.T) {
	m := machineMatch(t, 1)
	pos := m.engine.Actors()[1].Position
	for hit := uint32(0); hit < 4; hit++ {
		m.receiveMachineHarm(game.EntityHarmedEvent{ObjectID: 2, Time: 1000 + hit*3000, PosX: uint16(pos.X), PosY: uint16(pos.Y)})
	}
	if !m.machineFinishing() {
		t.Fatal("final durability loss did not stop producers")
	}
	if len(mirrored(t, m, game.NotifyPlayerDieEvent)) != 4 {
		t.Fatal("final reliable death report was discarded")
	}
	// The final scene death stays queued for flush; test the following step
	// after that batch has already flushed, without a real UDP transport.
	m.outbound = nil
	m.presence = true
	m.lastRendezvous = time.Now()
	m.started = time.Now().Add(-time.Minute)
	m.bot = newAutoplayBot(1)
	m.bot.machine = m.machine
	before := m.engine.ElapsedMS()
	if _, err := m.step(command{}); err != nil {
		t.Fatal(err)
	}
	if m.engine.ElapsedMS() != before || len(m.outbound) != 0 || len(m.takeTCP()) != 0 {
		t.Fatal("gameplay continued while waiting for server result")
	}
}
