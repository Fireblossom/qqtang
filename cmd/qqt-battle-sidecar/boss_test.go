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

func TestGriffinGreetsWhenBornButSailorDoesNot(t *testing.T) {
	m := bossMatch(t, 1)
	m.boss.actors[30001].ai = nil
	advanceTo(t, m, m.engine.ElapsedMS()+bossBirthMS)
	m.bossTick(m.engine.ElapsedMS(), nil)
	if len(mirrored(t, m, game.NotifyNPCTalk)) != 0 {
		t.Fatal("the sailor spoke")
	}
	b := m.boss.actors[30001]
	b.role, b.greeted = 24, false
	m.bossTick(m.engine.ElapsedMS(), nil)
	m.bossTick(m.engine.ElapsedMS(), nil)
	talks := mirrored(t, m, game.NotifyNPCTalk)
	if len(talks) != 1 {
		t.Fatalf("griffin talks %d", len(talks))
	}
	talk, err := game.ParseNPCTalkEvent(talks[0])
	if err != nil || talk.ObjectID != 30001 || len(talk.Words) != 40 {
		t.Fatalf("talk %+v %v", talk, err)
	}
}

func pirateMatch(t *testing.T, local uint16, role uint16, hp int) (*onlineMatch, *bossActor, *bossGeneral) {
	m := bossMatch(t, local)
	b := m.boss.actors[30001]
	b.role, b.hp = role, hp
	return m, b, b.ai.(*bossGeneral)
}

func skillIDs(t *testing.T, m *onlineMatch) []uint16 {
	var ids []uint16
	for _, e := range mirrored(t, m, game.NotifyNPCUseSkill) {
		s, _ := game.ParseNPCUseSkillEvent(e)
		ids = append(ids, s.SkillID)
	}
	return ids
}

func TestCaptainHookRunsHisScriptsInOrder(t *testing.T) {
	m, b, ai := pirateMatch(t, 1, pirateCaptainHook, 10)
	w := m.bossWorld(4999)
	ai.pirateSkills(w, b)
	if len(skillIDs(t, m)) != 0 {
		t.Fatal("skill before 5 s")
	}
	// Script 0: 9 (silent), 4 traps, 5 lock, 6 two waves, in one frame.
	ai.pirateSkills(m.bossWorld(8000), b)
	if got := skillIDs(t, m); len(got) != 4 || got[0] != 4 || got[1] != 5 || got[2] != 6 || got[3] != 6 {
		t.Fatalf("script 0 sent %v", got)
	}
	if m.boss.bubbleLock != 1 || ai.pirate.script != 1 {
		t.Fatalf("lock %d script %d", m.boss.bubbleLock, ai.pirate.script)
	}
	action := battleengine.Action{PlaceBomb: true}
	m.bossInput(&action)
	if action.PlaceBomb {
		t.Fatal("locked player placed a bubble")
	}
	ai.pirateSkills(m.bossWorld(12999), b)
	if len(skillIDs(t, m)) != 0 {
		t.Fatal("no 5 s spacing")
	}
	for _, at := range []uint32{13000, 18000, 23000} { // scripts 1, 2 (waves), 3 (unlock)
		ai.pirateSkills(m.bossWorld(at), b)
	}
	if m.boss.bubbleLock != 0 || ai.pirate.script != 4 {
		t.Fatalf("after scripts 1..3 lock %d script %d", m.boss.bubbleLock, ai.pirate.script)
	}
	mirrored(t, m, game.NotifyNPCUseSkill)
	ai.pirateSkills(m.bossWorld(28000), b) // script 4: skill 7, then a 15 s rest
	ai.pirateSkills(m.bossWorld(38000), b)
	if got := skillIDs(t, m); len(got) != 1 || got[0] != 7 {
		t.Fatalf("skill 7 rest: %v", got)
	}
	ai.pirateSkills(m.bossWorld(43000), b)
	if ai.pirate.script != 6 {
		t.Fatalf("script 0 did not run again after the rest: %d", ai.pirate.script)
	}
}

func TestFirstMateWaitsForHisHPGates(t *testing.T) {
	m, b, ai := pirateMatch(t, 1, pirateFirstMate, 10)
	ai.pirateSkills(m.bossWorld(8000), b)
	if len(skillIDs(t, m)) != 0 || ai.pirate.script != 0 {
		t.Fatal("first mate cast at HP 10")
	}
	b.hp = 6
	ai.pirateSkills(m.bossWorld(13000), b)
	if got := skillIDs(t, m); len(got) != 1 || got[0] != 4 || ai.pirate.script != 0 {
		t.Fatalf("HP 6 sent %v, script %d", got, ai.pirate.script)
	}
	b.hp = 4
	ai.pirateSkills(m.bossWorld(18000), b)
	if got := skillIDs(t, m); len(got) != 3 || ai.pirate.script != 1 {
		t.Fatalf("HP 4 sent %v, script %d", got, ai.pirate.script)
	}
}

func TestPirateBubbleWavesLandOnPeers(t *testing.T) {
	m, b, ai := pirateMatch(t, 1, pirateCaptainHook, 10)
	ai.pirateSkills(m.bossWorld(8000), b)
	peer := bossMatch(t, 2)
	waves := 0
	for _, e := range mirrored(t, m, game.NotifyNPCUseSkill) {
		peer.applyRemote(30001, game.BattleMessageData{DataID: game.NotifyNPCUseSkill, Data: e.Body})
		if s, _ := game.ParseNPCUseSkillEvent(e); s.SkillID == 6 {
			waves++
		}
	}
	k := min(8000/16000+5, 15)
	owned := 0
	for _, bomb := range peer.engine.Bombs() {
		if bomb.OwnerID == 30001 {
			owned++
		}
	}
	if waves != 2 || owned != 3*k || peer.boss.bubbleLock != 1 {
		t.Fatalf("waves %d, Boss bubbles %d (want %d), lock %d", waves, owned, 3*k, peer.boss.bubbleLock)
	}
}

func kickBossMatch(t *testing.T, local uint16, skills []uint32) (*onlineMatch, *bossActor, *bossKick) {
	m := bossMatch(t, local)
	m.kick = newKickRule(mapdata.CompetitiveMap{ID: 701, Rule: mapdata.CompetitiveRuleKickBomb}, 1)
	b := m.boss.actors[30001]
	b.info.AIType, b.info.Skills, b.role = 5, skills, 36
	ai := &bossKick{}
	b.ai = ai
	return m, b, ai
}

func TestFootballPathNeverStepsUpIntoTheTopRows(t *testing.T) {
	m, _, _ := kickBossMatch(t, 1, nil)
	w := m.bossWorld(m.engine.ElapsedMS())
	if _, ok := w.find5(battleengine.Cell{Row: 5, Col: 3}, battleengine.Cell{Row: 2, Col: 3}); ok {
		t.Fatal("reached row 2 from below")
	}
	nodes, ok := w.find5(battleengine.Cell{Row: 1, Col: 3}, battleengine.Cell{Row: 6, Col: 3})
	if !ok || len(nodes) != 6 || nodes[0].dir != 3 {
		t.Fatalf("down from the top rows: %+v %v", nodes, ok)
	}
}

func TestKickBossSkillCooldownCellsFollowTheListPosition(t *testing.T) {
	for _, tc := range []struct {
		skills []uint32
		want   []uint16 // skills sent at 30 s and 31 s
	}{
		{[]uint32{10, 11, 12}, []uint16{10, 11}},
		{[]uint32{0, 11, 12}, []uint16{11}},
		// The broken compaction: 11 tests cell 0 (never set) and fires again.
		{[]uint32{11, 12}, []uint16{11, 11}},
	} {
		m, b, ai := kickBossMatch(t, 1, tc.skills)
		ai.skills(m.bossWorld(30000), b)
		ai.skills(m.bossWorld(31000), b)
		if got := skillIDs(t, m); len(got) != len(tc.want) || got[0] != tc.want[0] {
			t.Fatalf("skills %v sent %v, want %v", tc.skills, got, tc.want)
		}
	}
}

func TestKickBossShieldAndKick(t *testing.T) {
	m, b, ai := kickBossMatch(t, 1, []uint32{0, 11, 12})
	ai.skills(m.bossWorld(30000), b)
	if b.protectedUntil != 40000 || b.vulnerable(39990) {
		t.Fatalf("skill 11 shield until %d", b.protectedUntil)
	}
	// A landed airdrop bubble right of the Boss is kicked as it walks in.
	b.bornAt = 0
	front := bossStep(b.pos.Cell(), 0)
	events, err := m.engine.DropRuleBatch(1, m.engine.ElapsedMS()-500, []battleengine.RuleBomb{{Cell: front, Power: 1, FromCol: front.Col}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.bombs[events[0].BombID] = wireBomb{owner: 1, placedAt: m.engine.ElapsedMS() - 500, cell: front}
	b.path = []battleengine.Cell{front}
	m.bossKickAhead(b, m.engine.ElapsedMS())
	kicks := mirrored(t, m, game.NotifyPlayerMoveBomb)
	if len(kicks) != 1 {
		t.Fatalf("kicks %d", len(kicks))
	}
	kick, _ := game.ParseMoveBombEvent(kicks[0])
	if kick.PlayerID != 30001 || kick.FromCol != uint16(front.Col) || m.engine.Bombs()[0].Cell == front {
		t.Fatalf("kick %+v, bubble at %+v", kick, m.engine.Bombs()[0].Cell)
	}
}

func TestKickBossStandsStillBeforeTheFirstAirdrop(t *testing.T) {
	m, b, ai := kickBossMatch(t, 1, nil)
	if d := ai.decide(m.bossWorld(4000), b); d != bossDirStop {
		t.Fatalf("moved %d before the first batch", d)
	}
}

func TestGreatNianTransformsAndAHitOnlyRemovesIt(t *testing.T) {
	owner, peer := bossMatch(t, 1), bossMatch(t, 2)
	b := owner.boss.actors[30001]
	b.role, b.info.Skills, b.bornAt = 31, []uint32{1, 3}, 0
	ai := b.ai.(*bossGeneral)
	ai.tick(owner, b, owner.engine.ElapsedMS())
	ai.last[3] = 0
	ai.genericSkills(owner.bossWorld(31000), b)
	grants := mirrored(t, owner, game.NotifyPlayerGetItem)
	if len(grants) != 1 || !b.transformed() || b.speed != 8 || b.capacity != 8 || b.avatarRole() != 42 {
		t.Fatalf("grants %d, transformed %v speed %d capacity %d", len(grants), b.transformed(), b.speed, b.capacity)
	}
	peer.applyRemote(30001, game.BattleMessageData{DataID: game.NotifyPlayerGetItem, Data: grants[0].Body})
	if !peer.boss.actors[30001].transformed() {
		t.Fatal("the peer did not transform the Boss")
	}
	// A hit on the demon: no HP lost, no drop, the transformation goes.
	b.ai, b.protectedUntil = nil, 0
	explodeAt(t, owner, b.pos.Cell())
	harms := mirrored(t, owner, game.PlayerBeHarmed)
	if b.hp != 5 || b.transformed() || len(harms) != 1 || len(owner.boss.actors[30001].normal) != 2 {
		t.Fatalf("hit on the demon: HP %d transformed %v harms %d", b.hp, b.transformed(), len(harms))
	}
	harm, _ := game.ParseEntityHarmedEvent(harms[0])
	peer.applyRemote(30001, game.BattleMessageData{DataID: game.PlayerBeHarmed, Data: harms[0].Body})
	if !harm.IsAvatar || peer.boss.actors[30001].transformed() || peer.boss.actors[30001].hp != 5 {
		t.Fatalf("peer after the avatar hit: %+v", peer.boss.actors[30001])
	}
}
