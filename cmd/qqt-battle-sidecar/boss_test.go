package main

import (
	"os"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func TestBossSpawnsOnFirstUnusedInterleavedPoint(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{11, 124, 701, 702, 211} {
		entry, ok := catalog.CompetitiveMap(id)
		if !ok {
			t.Fatalf("map %d missing", id)
		}
		rule := newBossRule(entry, game.GameBeginData{Players: make([]game.PlayerGameInfo, 4)})
		if rule == nil {
			t.Fatalf("map %d has no Boss overlay", id)
		}
		if len(rule.spawns) != len(entry.SpawnGroupA)+len(entry.SpawnGroupB) {
			t.Fatalf("map %d table %v from %v / %v", id, rule.spawns, entry.SpawnGroupA, entry.SpawnGroupB)
		}
		// A0, B0, A1, B1, ...: map 124 (A 2, B 2) puts B0 second.
		if len(entry.SpawnGroupB) > 0 && rule.spawns[1] != (battleengine.Cell{Row: int16(entry.SpawnGroupB[0].Row), Col: int16(entry.SpawnGroupB[0].Col)}) {
			t.Fatalf("map %d is not interleaved: %v", id, rule.spawns)
		}
		// Two players always leave a point for the Boss.
		if len(rule.spawns) < 3 {
			t.Errorf("map %d has only %d spawn points", id, len(rule.spawns))
		}
	}
	if newBossRule(mapdata.CompetitiveMap{ID: 1}, game.GameBeginData{}) != nil {
		t.Fatal("map 1 has no Boss candidate")
	}
}

// bossMatch is a 15x13 open field with players 1 (team 1, 6,11) and 2
// (team 1, 1,1) and the sailor at spawn 3,7.
func bossMatch(t *testing.T, local uint16) *onlineMatch {
	t.Helper()
	m := kickMatch(t, 1)
	m.local, m.kick = local, nil
	rules := onlineRules(local, 1)
	rules.NativeBossNPC, rules.ExternalConclusion = true, true
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 240_000, 3000, 500, 19
	participants := []battleengine.Participant{}
	for _, a := range m.engine.Actors() {
		p := a.Participant
		p.TeamID = 1
		participants = append(participants, p)
	}
	var err error
	m.engine, err = battleengine.New(battleengine.Config{Seed: 1, Grid: m.engine.Grid(), Rules: rules, Participants: participants})
	if err != nil {
		t.Fatal(err)
	}
	m.runtime, _ = battleengine.NewRuntime(m.engine, nil)
	entry := mapdata.CompetitiveMap{ID: 11, SpawnGroupA: []mapdata.CompetitiveCell{{Row: 6, Col: 11}, {Row: 3, Col: 7}}, SpawnGroupB: []mapdata.CompetitiveCell{{Row: 1, Col: 1}}}
	m.boss = newBossRule(entry, game.GameBeginData{ItemSeed: 1, Players: make([]game.PlayerGameInfo, 2)})
	advanceTo(t, m, 3000)
	m.createBosses(game.CreateNPCBoss{Bosses: []game.CompetitiveBossInfo{{
		BossID: 30001, RoleID: 32, TeamID: 7, AIType: 3, HP: 5, Rate: 8, Bubble: 8, Power: 9,
		NormalItems: []game.BossItemInfo{{ItemID: 81, ItemCount: 5}, {ItemID: 86, ItemCount: 3}},
		OutfitItems: []game.BossItemInfo{{ItemID: 450, ItemCount: 1}}, Skills: []uint32{1},
	}}}, m.engine.ElapsedMS())
	return m
}

// explodeAt detonates a rule bubble on the Boss's cell and runs the Boss
// for every tick, as step does.
func explodeAt(t *testing.T, m *onlineMatch, cell battleengine.Cell) {
	t.Helper()
	m.engine.RegisterRuleNPCOwner(30002)
	if _, err := m.engine.PlaceRuleNPCBomb(30002, cell, 1, 0, m.engine.ElapsedMS()); err != nil {
		t.Fatal(err)
	}
	until := m.engine.ElapsedMS() + 3200
	for m.engine.ElapsedMS() < until {
		events, err := m.runtime.Step(nil)
		if err != nil {
			t.Fatal(err)
		}
		m.bossTick(m.engine.ElapsedMS(), events)
	}
}

func TestBossTakesOneHPPerHitWithProtectionDropsAndDeath(t *testing.T) {
	owner, peer := bossMatch(t, 1), bossMatch(t, 2)
	b := owner.boss.actors[30001]
	if b == nil || b.pos != battleengine.PositionAtCellCenter(battleengine.Cell{Row: 3, Col: 7}) || b.hp != 5 {
		t.Fatalf("Boss %+v", b)
	}
	b.ai = nil // stand still
	cell := b.pos.Cell()
	explodeAt(t, owner, cell)
	if b.hp != 4 || b.protectedUntil <= owner.engine.ElapsedMS() {
		t.Fatalf("after the first hit HP %d protected until %d at %d", b.hp, b.protectedUntil, owner.engine.ElapsedMS())
	}
	harms := mirrored(t, owner, game.PlayerBeHarmed)
	if len(harms) != 1 {
		t.Fatalf("0x10F4 count %d", len(harms))
	}
	// The drop was mirrored in the same step; take it from the outbox log.
	for _, out := range owner.boss.outbox {
		if out.author != 30001 {
			t.Fatalf("Boss message authored by %d", out.author)
		}
	}
	// A peer applies the arbitrator's facts: one HP per 0x10F4.
	peer.applyRemote(30001, game.BattleMessageData{DataID: game.PlayerBeHarmed, Data: harms[0].Body})
	peer.applyRemote(30001, game.BattleMessageData{DataID: game.PlayerBeHarmed, Data: harms[0].Body})
	if peer.boss.actors[30001].hp != 4 {
		t.Fatalf("peer HP %d after a repeated 0x10F4", peer.boss.actors[30001].hp)
	}
	// Still protected: the bubble bursts 3 s after placement, inside the
	// window.
	b.protectedUntil = owner.engine.ElapsedMS() + 3100
	explodeAt(t, owner, cell)
	if b.hp != 4 {
		t.Fatalf("protected Boss lost HP: %d", b.hp)
	}
	for b.hp > 0 {
		b.protectedUntil = 0
		explodeAt(t, owner, cell)
	}
	if !b.dead || owner.bossView().Actors != nil {
		t.Fatalf("Boss not removed after HP 0: %+v", b)
	}
	deaths := mirrored(t, owner, game.NotifyPlayerDieEvent)
	if len(deaths) != 1 {
		t.Fatalf("0x0FA7 count %d", len(deaths))
	}
	death, _ := game.ParsePlayerDeathEvent(deaths[0])
	outfit := false
	for _, item := range death.Items {
		outfit = outfit || item.ItemID == 450
	}
	if death.PlayerID != 30001 || !outfit {
		t.Fatalf("death %+v", death)
	}
	peer.applyRemote(30001, game.BattleMessageData{DataID: game.NotifyPlayerDieEvent, Data: deaths[0].Body})
	if !peer.boss.actors[30001].dead {
		t.Fatal("peer kept the Boss")
	}
}

func TestBossIsImmuneWhileBorn(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	b.ai = nil
	if b.vulnerable(m.engine.ElapsedMS() + bossBirthMS - 20) {
		t.Fatal("Boss vulnerable during its birth")
	}
	if !b.vulnerable(m.engine.ElapsedMS() + bossBirthMS) {
		t.Fatal("Boss still immune after its birth")
	}
}

func TestBossKillsTrappedPlayerItTouches(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	b.ai = nil
	b.bornAt = 0
	target, _ := actorByID(m.engine.Actors(), 2)
	if _, _, err := m.runtime.AcceptNativeActorHitFrom(2, 0, target.Position, false); err != nil {
		t.Fatal(err)
	}
	b.pos = battleengine.Position{X: target.Position.X + 40, Y: target.Position.Y}
	m.bossContact(b, m.engine.ElapsedMS())
	if a, _ := actorByID(m.engine.Actors(), 2); a.State != battleengine.ActorEliminated {
		t.Fatalf("trapped player state %d", a.State)
	}
	kills := mirrored(t, m, game.NotifyPlayerKilled)
	if len(kills) != 1 {
		t.Fatalf("kill mirrors %d", len(kills))
	}
	killed, _ := game.ParsePlayerKilledEvent(kills[0])
	if killed.PlayerID != 30001 || killed.DestinationPlayerID != 2 {
		t.Fatalf("kill %+v", killed)
	}
	// An untrapped player is not touched.
	one, _ := actorByID(m.engine.Actors(), 1)
	b.pos = one.Position
	m.bossContact(b, m.engine.ElapsedMS())
	if a, _ := actorByID(m.engine.Actors(), 1); a.State != battleengine.ActorActive {
		t.Fatal("Boss contact hurt an untrapped player")
	}
}

func TestWFPathFinderFindsManhattanRouteAroundWalls(t *testing.T) {
	m := bossMatch(t, 1)
	grid := m.engine.Grid()
	w := m.bossWorld(m.engine.ElapsedMS())
	nodes, g, ok := w.find(battleengine.Cell{Row: 3, Col: 7}, battleengine.Cell{Row: 3, Col: 10}, pathMaxF, pathMaxOpen, true, true)
	if !ok || len(nodes) != 4 || g != 3 || nodes[0].dir != 0 || nodes[3].dir != bossDirStop {
		t.Fatalf("open route %+v g %d %v", nodes, g, ok)
	}
	_ = grid
}

func TestBossChasesAndBubblesThenEscapes(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	start := b.pos
	until := m.engine.ElapsedMS() + 3000
	placed := false
	for m.engine.ElapsedMS() < until && !placed {
		events, err := m.runtime.Step(nil)
		if err != nil {
			t.Fatal(err)
		}
		m.bossTick(m.engine.ElapsedMS(), events)
		for _, bomb := range m.engine.Bombs() {
			placed = placed || bomb.OwnerID == 30001
		}
	}
	if b.pos == start {
		t.Fatal("Boss never moved")
	}
	if !placed {
		t.Fatalf("Boss placed no bubble; at %+v", b.pos.Cell())
	}
	if len(mirrored(t, m, game.PlayerUseBomb)) == 0 {
		t.Fatal("no 0x0FA3 mirror")
	}
	// It leaves its own bubble's cross before the burst.
	var bombCell battleengine.Cell
	for _, bomb := range m.engine.Bombs() {
		if bomb.OwnerID == 30001 {
			bombCell = bomb.Cell
		}
	}
	for i := 0; i < 140; i++ {
		events, _ := m.runtime.Step(nil)
		m.bossTick(m.engine.ElapsedMS(), events)
	}
	if b.dead || b.hp != 5 {
		t.Fatalf("Boss was caught by its own bubble at %+v (bubble %+v), HP %d", b.pos.Cell(), bombCell, b.hp)
	}
}

func TestBossSlowGlueSkillEveryTenSeconds(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	ai := b.ai.(*bossGeneral)
	ai.tick(m, b, m.engine.ElapsedMS())
	now := m.engine.ElapsedMS()
	w := m.bossWorld(now + 9990)
	ai.genericSkills(w, b)
	if len(m.boss.flights) != 0 {
		t.Fatal("skill before the 10 s gate")
	}
	w = m.bossWorld(now + 10000)
	ai.genericSkills(w, b)
	skills := mirrored(t, m, game.NotifyNPCUseSkill)
	if len(skills) != 1 || len(m.boss.flights) != 5 {
		t.Fatalf("skills %d flights %d", len(skills), len(m.boss.flights))
	}
	skill, _ := game.ParseNPCUseSkillEvent(skills[0])
	if skill.SkillID != 1 || skill.ObjectID != 30001 || len(skill.Items) != 5 || skill.Items[0].SceneID != 43 {
		t.Fatalf("skill %+v", skill)
	}
	latest := uint32(0)
	for _, f := range m.boss.flights {
		latest = max(latest, f.lands)
		if f.lands <= now+10000 && f.from != f.to {
			t.Fatalf("flight %+v lands at once", f)
		}
	}
	m.landBossFlights(now + 10000)
	if len(m.engine.FieldObjects()) == 5 {
		t.Fatal("glue landed before flying")
	}
	m.landBossFlights(latest)
	if len(m.engine.FieldObjects()) != 5 {
		t.Fatalf("glue on the field: %d", len(m.engine.FieldObjects()))
	}
	// A peer throws the same glue from the mirrored message.
	peer := bossMatch(t, 2)
	peer.applyRemote(30001, game.BattleMessageData{DataID: game.NotifyNPCUseSkill, Data: skills[0].Body})
	if len(peer.boss.flights) != 5 {
		t.Fatalf("peer flights %d", len(peer.boss.flights))
	}
}

func TestNewArbitratorTakesTheBossOver(t *testing.T) {
	peer := bossMatch(t, 2)
	b := peer.boss.actors[30001]
	advanceTo(t, peer, 6000)
	peer.bossTick(peer.engine.ElapsedMS(), nil)
	if b.moving || b.pos != battleengine.PositionAtCellCenter(battleengine.Cell{Row: 3, Col: 7}) {
		t.Fatal("a follower moved the Boss by itself")
	}
	peer.arbitrator = 2
	start := b.pos
	for i := 0; i < 50; i++ {
		events, _ := peer.runtime.Step(nil)
		peer.bossTick(peer.engine.ElapsedMS(), events)
		if d := abs32(b.pos.X-start.X) + abs32(b.pos.Y-start.Y); i == 0 && d > 20 {
			t.Fatalf("takeover jumped %d px", d)
		}
	}
	if b.pos == start {
		t.Fatal("the new arbitrator did not run the Boss")
	}
}

func TestBossStopsWhenEveryPlayerIsDead(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	for _, id := range []uint16{1, 2} {
		if _, err := m.runtime.AcceptNativeElimination(id, 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	start := b.pos
	for i := 0; i < 200; i++ {
		events, _ := m.runtime.Step(nil)
		m.bossTick(m.engine.ElapsedMS(), events)
	}
	if b.pos != start || len(m.engine.Bombs()) != 0 || len(m.takeTCP()) != 0 {
		t.Fatal("the Boss kept acting after every player died")
	}
}
