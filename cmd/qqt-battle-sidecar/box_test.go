package main

import (
	"os"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
	"testing"
)

func boxTestMatch(t *testing.T, local uint16) *onlineMatch {
	m := machineMatch(t, local)
	m.machine = nil
	rules, _, _ := ruleOptions(mapdata.CompetitiveMap{Rule: mapdata.CompetitiveRuleBox}, local, 1)
	rules.RoundDurationMS = 240000
	rules.BombFuseMS = 3000
	rules.FlameDurationMS = 500
	rules.ActorHalfSizePixels = 19
	grid := m.engine.Grid()
	parts := []battleengine.Participant{}
	for _, a := range m.engine.Actors() {
		parts = append(parts, a.Participant)
	}
	defs := map[uint32]mapdata.NativeBoxElement{}
	for _, id := range []uint32{14112, 14100, 14105, 14115, 14110, 14111, 14113, 14114, 14120, 14121, 14122, 14126, 8005, 14116, 14118, 14119, 14117} {
		flags := uint32(2)
		life := 1
		attrs := []uint32{0}
		if id == 14100 {
			flags = 10
		}
		if id == 14105 {
			flags = 6
		}
		if id == 14115 {
			flags = 18
		}
		if id == 8005 {
			flags, life = 0, -1
		}
		if id >= 14116 && id <= 14119 {
			flags, life, attrs = 0, -1, []uint32{3855}
		}
		defs[id] = mapdata.NativeBoxElement{ID: id, LifeTime: life, Flags: flags, Width: 1, Height: 1, GridAttrs: attrs}
	}
	grid.Cells[6*15+3] = battleengine.Tile{Kind: battleengine.CellBreakable, MapElementOccupied: true, MapElementID: 14112, Durability: 1, ElementWidth: 1, ElementHeight: 1, ElementAnchor: battleengine.Cell{Row: 6, Col: 3}}
	config := battleengine.Config{Grid: grid, Rules: rules, Participants: parts, Pickups: []battleengine.Pickup{{SceneID: 1, Cell: battleengine.Cell{Row: 6, Col: 3}, State: battleengine.PickupHidden}}}
	m.box = newBoxRule(mapdata.CompetitiveMap{Rule: mapdata.CompetitiveRuleBox}, game.GameBeginData{ItemSeed: 1}, &config, defs)
	var err error
	m.engine, err = battleengine.New(config)
	if err != nil {
		t.Fatal(err)
	}
	m.runtime, _ = battleengine.NewRuntime(m.engine, nil)
	m.installBoxLaunchers()
	return m
}
func TestBoxNativePushTargetsAndPeerMovement(t *testing.T) {
	if got := boxDestination(battleengine.Cell{Row: 6, Col: 10}, 2, 3); got.Col != 1 {
		t.Fatalf("native left branch changed: %+v", got)
	}
	owner, peer := boxTestMatch(t, 1), boxTestMatch(t, 2)
	id := uint32(6*16 + 3)
	if !owner.pushBox(1, id, 0, 5) {
		t.Fatal("push refused")
	}
	notices := mirrored(t, owner, game.NotifyPushMapElement)
	if len(notices) != 1 {
		t.Fatal("push notification absent")
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyPushMapElement, Data: notices[0].Body})
	for _, m := range []*onlineMatch{owner, peer} {
		b := m.box.objects[id]
		m.advanceBox(b, 200)
		if b.Position.X != 220 || b.Cell.Col != 3 {
			t.Fatalf("400px/s or source registration changed: %+v", b)
		}
	}
	owner.advanceBox(owner.box.objects[id], 500)
	owner.stopBox(owner.box.objects[id], 500)
	stops := mirrored(t, owner, game.NotifyMapElementStop)
	if len(stops) != 1 {
		t.Fatal("stop absent")
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyMapElementStop, Data: stops[0].Body})
	for _, m := range []*onlineMatch{owner, peer} {
		b := m.box.objects[id]
		if b.Cell.Col != 8 || b.Moving || b.Position.X != 340 {
			t.Fatal("peer stop differs")
		}
	}
	owner.destroyBoxes(1, []uint32{id}, 500)
	destroy := mirrored(t, owner, game.NotifyMapElementExploded)
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyMapElementExploded, Data: destroy[0].Body})
	for _, m := range []*onlineMatch{owner, peer} {
		advanceTo(t, m, 1000)
		m.boxTick()
		p := m.engine.Pickups()
		if len(p) != 1 || p[0].Cell != (battleengine.Cell{Row: 6, Col: 8}) || p[0].State != battleengine.PickupAvailable {
			t.Fatalf("hidden item did not travel with box: %+v", p)
		}
	}
}
func TestBoxCollisionWithStationaryAndMovingBoxes(t *testing.T) {
	for _, moving := range []bool{false, true} {
		m := boxTestMatch(t, 1)
		id := uint32(6*16 + 3)
		other := uint32(6*16 + 8)
		m.addBox(game.NewMapElementWire(14112, other, 6, 8), 0, false)
		m.box.objects[other].FrozenUntil = 0
		m.engine.SetNativeBoxCell(battleengine.Cell{Row: 6, Col: 8}, m.engine.Grid().Cells[6*15+3])
		m.pushBox(1, id, 0, 8)
		if moving {
			m.pushBox(2, other, 2, 8)
		}
		for clock := uint32(20); clock <= 500; clock += 20 {
			for _, b := range m.box.ordered() {
				m.advanceBox(b, clock)
			}
			m.boxCollisions(clock)
			if !m.box.objects[id].Moving {
				break
			}
		}
		a, b := m.box.objects[id], m.box.objects[other]
		if moving {
			if a.DestroyAt == 0 || b.DestroyAt == 0 {
				t.Fatalf("moving collision did not destroy both: %+v %+v", a, b)
			}
		} else if a.DestroyAt != 0 || a.Moving || a.Cell.Col != 7 || b.DestroyAt != 0 {
			t.Fatalf("stationary collision should stop before it: %+v %+v", a, b)
		}
	}
}
func TestBoxLaunchersEvenOddBlockDifferentSide(t *testing.T) {
	for _, clock := range []uint32{4000, 4001} {
		m := boxTestMatch(t, 1)
		events := m.applyBoxFire(game.PlayerTimedStateEvent{PlayerID: 1, Time: clock, Value: 15})
		if len(events) != 4 {
			t.Fatalf("launcher batch %d", len(events))
		}
		tl := m.box.objects[m.box.blockers[0]]
		want := battleengine.Cell{Row: 0, Col: 1}
		if clock&1 != 0 {
			want = battleengine.Cell{Row: 1, Col: 0}
		}
		if tl.Cell != want || !tl.Hidden {
			t.Fatalf("side blocker %+v", tl)
		}
		for _, b := range m.engine.Bombs() {
			if !b.Invisible || b.ExplodeAtMS != clock+1051 {
				t.Fatal("launcher created flying/visible or wrong fuse bubble")
			}
		}
	}
}
func TestAllBoxMapsBuildWithRetainedHiddenItems(t *testing.T) {
	root := "../../runtime/client-patched"
	if _, err := os.Stat(root); err != nil {
		t.Skip("native resources unavailable")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	elements, err := mapdata.LoadNativeBoxElements(root)
	if err != nil {
		t.Fatal(err)
	}
	if elements[14105].Flags&4 == 0 || elements[14100].Flags&8 == 0 || elements[14115].Flags&16 == 0 {
		t.Fatal("native special box flags missing")
	}
	for id := uint32(1401); id <= 1409; id++ {
		entry, ok := catalog.CompetitiveMap(id)
		if !ok {
			t.Fatalf("missing %d", id)
		}
		rules, _, err := ruleOptions(entry, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := battleengine.ParticipantFromNativeRole(1, 1, 1, battleengine.ParticipantHuman)
		b, _ := battleengine.ParticipantFromNativeRole(2, 2, 2, battleengine.ParticipantHuman)
		supply := entry.PlanCompetitiveSupply(123, []uint16{1, 2}, false)
		cfg, err := battleengine.ConfigFromCompetitiveMap(entry, battleengine.CompetitiveMapConfigOptions{SpawnSeed: 7, ItemSeed: 123, SpawnMode: battleengine.NativeSpawnModeForMap(entry, false), Rules: rules, Participants: []battleengine.Participant{a, b}, RecordedWallItems: supply.WallItems, RecordedDelayedItems: supply.DelayedItems, UseRecordedWallItems: true, AllowSpecialRules: true})
		if err != nil {
			t.Fatalf("map %d: %v", id, err)
		}
		before := len(cfg.Pickups)
		rule := newBoxRule(entry, game.GameBeginData{ItemSeed: 123}, &cfg, elements)
		retained := len(cfg.Pickups)
		for _, box := range rule.objects {
			retained += len(box.items)
		}
		if retained != before {
			t.Fatalf("map %d hidden items lost", id)
		}
		if _, err = battleengine.New(cfg); err != nil {
			t.Fatal(err)
		}
		t.Logf("map %d: %d box objects, %d hidden pickups", id, len(rule.objects), before)
	}
}

func TestBoxForwardCollisionStripMatchesNativeBounds(t *testing.T) {
	box := &boxObject{Position: battleengine.Position{X: 100, Y: 100}, Direction: 0}
	if !boxContact(box, battleengine.Position{X: 148, Y: 100}) {
		t.Fatal("forward contact at48px missed")
	}
	if boxContact(box, battleengine.Position{X: 150, Y: 100}) {
		t.Fatal("50px boundary should be excluded")
	}
	if boxContact(box, battleengine.Position{X: 148, Y: 106}) || boxContact(box, battleengine.Position{X: 148, Y: 90}) {
		t.Fatal("native perpendicular strict bounds lost")
	}
	box.Direction = 2
	if boxContact(box, battleengine.Position{X: 148, Y: 100}) {
		t.Fatal("forward strip became symmetric")
	}
}
func TestBoxLocalContactDeathKeepsAnimationEvent(t *testing.T) {
	m := boxTestMatch(t, 1)
	pos := m.engine.Actors()[0].Position
	for n := uint32(0); n < 3; n++ {
		m.engine.ApplyNativeDurabilityHit(1, 2, pos, 1000+n*3000)
	}
	advanceTo(t, m, 10000)
	events := m.boxHitLocal(game.NotifyPlayerKnocked, 0)
	queued := m.takeBoxEvents()
	if len(events) != 2 || len(queued) != 2 || queued[1].Kind != battleengine.EventActorEliminated || !m.boxFinishing() {
		t.Fatalf("final local box hit lost UI event: %+v", queued)
	}
	if len(m.takeBoxEvents()) != 0 {
		t.Fatal("box UI event repeated")
	}
}
func TestBoxLauncherCannotUnderflowAnUnquantizedPreviousClock(t *testing.T) {
	m := boxTestMatch(t, 1)
	m.box.lastFire = 3029
	if mask := m.box.launcherMask(3020); mask != 0 {
		t.Fatalf("past tick fired mask%x", mask)
	}
}

func TestBoxRegenerationMirrorsIdentityAndInitialProtection(t *testing.T) {
	owner, peer := boxTestMatch(t, 1), boxTestMatch(t, 2)
	for _, m := range []*onlineMatch{owner, peer} {
		for id, b := range m.box.objects {
			m.engine.ClearNativeBox(b.Cell, b.Element)
			delete(m.box.objects, id)
		}
		advanceTo(t, m, 30020)
	}
	owner.boxGenerate(30020)
	notices := mirrored(t, owner, game.NotifyGenerateBox)
	if len(notices) != 1 {
		t.Fatal("depleted map did not regenerate")
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyGenerateBox, Data: notices[0].Body})
	if len(owner.box.objects) < 2 || len(owner.box.objects) > 4 || len(peer.box.objects) != len(owner.box.objects) {
		t.Fatal("wrong regenerated count")
	}
	for id, b := range owner.box.objects {
		other := peer.box.objects[id]
		if id>>8 != 1 || other == nil || other.Cell != b.Cell || b.breakable(31519) || !b.breakable(31520) {
			t.Fatalf("new object identity/protection differs: %+v %+v", b, other)
		}
	}
	for _, m := range []*onlineMatch{owner, peer} {
		advanceTo(t, m, 31520)
		m.box.lastFire = 31520
		m.boxTick()
		for _, b := range m.box.objects {
			tile, _ := m.engine.Grid().Cell(b.Cell)
			if tile.Durability != 1 {
				t.Fatal("generation protection did not expire")
			}
		}
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyGenerateBox, Data: notices[0].Body})
	for _, b := range peer.box.objects {
		if b.BornAt != 30020 {
			t.Fatal("repeated UID reset an existing box")
		}
	}
}
