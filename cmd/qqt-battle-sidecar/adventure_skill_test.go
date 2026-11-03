package main

import (
	"math/rand"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func TestAdventureAffectArea(t *testing.T) {
	center := battleengine.Cell{Row: 5, Col: 5}
	cases := []struct {
		shape, factor, dir int
		want               [4]int16
	}{
		{0, 2, 0, [4]int16{3, 7, 3, 7}},
		{1, 3, 0, [4]int16{5, 5, 6, 8}}, // right: columns 6..8
		{1, 2, 1, [4]int16{3, 4, 5, 5}}, // up: rows 3..4
		{2, 1, 3, [4]int16{6, 6, 4, 6}}, // the row below, 3 wide
		{3, 1, 0, [4]int16{4, 6, 5, 5}}, // facing right: the column through the centre
		{4, 5, 0, [4]int16{0, 0, 0, 0}},
	}
	for _, c := range cases {
		r0, r1, c0, c1 := affectArea(center, c.factor, c.dir, c.shape)
		if got := [4]int16{r0, r1, c0, c1}; got != c.want {
			t.Errorf("shape %d factor %d dir %d: %v, want %v", c.shape, c.factor, c.dir, got, c.want)
		}
	}
}

func TestAdventureSkillTriggers(t *testing.T) {
	hp := mapdata.AdventureNPCSkill{"HPTrigger": "3", "HPLower": "0"}
	if skillTriggerHolds(hp, "HPTrigger", "HPLower", "", 10) {
		t.Fatal("HP < 0 held")
	}
	if !skillTriggerHolds(mapdata.AdventureNPCSkill{"HPTrigger": ""}, "HPTrigger", "HPLower", "", 10) {
		t.Fatal("an empty trigger did not hold")
	}
	if !skillTriggerHolds(mapdata.AdventureNPCSkill{"HPTrigger": "1", "HPLower": "5"}, "HPTrigger", "HPLower", "", 10) {
		t.Fatal("HP > 5 did not hold at 10")
	}
}

func adventureSkillMatch(t *testing.T, prop mapdata.AdventureProp, trigger string) (*onlineMatch, *advNPC) {
	t.Helper()
	m := bossMatch(t, 1)
	m.boss = nil
	skill := mapdata.AdventureNPCSkill{"TimeTrigger": "2", "TimeLower": "0", "TimeUpper": "600000", "SkiLLID": "21001", "SkillTrigger": trigger}
	m.adventure = &adventureRule{players: map[uint16]*advPlayer{1: {hp: 100, maxHP: 100}, 2: {hp: 100, maxHP: 100}}, npcs: map[uint16]*advNPC{},
		bubbles: map[uint32]bool{}, seen: map[string]bool{}, rng: rand.New(rand.NewSource(1)),
		stage: mapdata.AdventureStage{NPCs: []mapdata.AdventureNPC{{Skills: []mapdata.AdventureNPCSkill{skill}}}},
		props: map[uint32]mapdata.AdventureProp{prop.ID: prop}}
	n := &advNPC{view: 3, enmity: map[uint16]int{1: 30}, strategy: 4, battle: true, hp: 50, maxHP: 100, lastFire: map[int]uint32{}, info: game.PVENPC{BattleStrategy: 1}}
	one, _ := actorByID(m.engine.Actors(), 1)
	n.id, n.pos, n.speed, n.bossSpeed = 30300, battleengine.PositionAtCellCenter(battleengine.Cell{Row: one.Position.Cell().Row, Col: one.Position.Cell().Col - 2}), 3, true
	m.adventure.npcs[n.id] = n
	return m, n
}

func TestAdventureNPCSkillHurtsTheLocalPlayerOnce(t *testing.T) {
	prop := mapdata.AdventureProp{ID: 21001, Tier: 1, PrepareTime: 500, AffectObject: 4, AffectShape: 0, AffectFactor: 3, AffectCount: 1,
		Effects: map[string]float64{"HP": -30}}
	m, n := adventureSkillMatch(t, prop, "1")
	now := m.engine.ElapsedMS()
	if d := m.adventureNPCDecide(n, now); d != bossDirStop || len(m.adventure.casts) != 1 {
		t.Fatalf("no cast: dir %d casts %d", d, len(m.adventure.casts))
	}
	c := m.adventure.casts[0]
	if !c.hasTarget || n.preparingUntil != now+500 {
		t.Fatalf("target %v preparing until %d", c.hasTarget, n.preparingUntil)
	}
	if m.adventureTrySkill(n, now+100) {
		t.Fatal("cast again while preparing")
	}
	m.adventureCastTick(now + 499)
	if m.adventure.players[1].hp != 100 {
		t.Fatal("hurt before the prepare time")
	}
	m.adventureCastTick(now + 501) // prepare -> trigger
	m.adventureCastTick(now + 502) // targets + first landing
	if m.adventure.players[1].hp != 70 || c.targets[0] != 1 {
		t.Fatalf("HP %d targets %v", m.adventure.players[1].hp, c.targets)
	}
	if m.adventure.players[2].hp != 100 {
		t.Fatal("the remote player's own client reports its HP")
	}
	m.adventureCastTick(now + 600)
	if len(m.adventure.casts) != 0 {
		t.Fatal("the prop outlived its affect time")
	}
	if m.adventureTrySkill(n, now+3000) {
		t.Fatal("Tier 1 cooldown ignored")
	}
	if !m.adventureTrySkill(n, now+501+5000) {
		t.Fatal("no cast after the Tier 1 cooldown")
	}
}

func TestAdventureNPCSkillHealsItselfAndRestoresDefence(t *testing.T) {
	prop := mapdata.AdventureProp{ID: 21001, Tier: 2, AffectObject: 0, AffectCount: 2, AffectTime: 1000, RestoreAttr: true,
		Effects: map[string]float64{"HP": 10, "Defence": 4}}
	m, n := adventureSkillMatch(t, prop, "0")
	now := m.engine.ElapsedMS()
	m.adventureNPCDecide(n, now)
	for _, at := range []uint32{1, 2, 3, 600, 1100} {
		m.adventureCastTick(now + at)
	}
	if n.hp != 70 || n.defense != 0 || len(m.adventure.casts) != 0 {
		t.Fatalf("HP %d defence %d casts %d", n.hp, n.defense, len(m.adventure.casts))
	}
}

func TestAdventureNPCProjectileStopsOnTheFirstHit(t *testing.T) {
	prop := mapdata.AdventureProp{ID: 21001, Tier: 1, HasMove: true, MoveDistance: 6, MoveSpeed: 400, HitPlayerPower: 1, AffectObject: 4, AffectShape: 4,
		AffectCount: 1, Effects: map[string]float64{"HP": -20}}
	m, n := adventureSkillMatch(t, prop, "0")
	one, _ := actorByID(m.engine.Actors(), 1)
	n.facing = 0 // the player stands two cells to the right
	now := m.engine.ElapsedMS()
	m.adventureNPCDecide(n, now)
	for at := uint32(1); at < 1000; at += 20 {
		m.adventureCastTick(now + at)
	}
	if m.adventure.players[1].hp != 80 {
		t.Fatalf("HP %d (player at %v)", m.adventure.players[1].hp, one.Position.Cell())
	}
}

func TestAdventureNPCSkillSlowsAndLocksThenRestores(t *testing.T) {
	prop := mapdata.AdventureProp{ID: 21001, Tier: 116, AffectObject: 4, AffectShape: 0, AffectFactor: 4, AffectCount: 1, AffectTime: 1000, RestoreAttr: true,
		Effects: map[string]float64{"CanUseMoveKey": 0, "CanUseBombKey": 0, "BombCountDel": -1}}
	m, n := adventureSkillMatch(t, prop, "0")
	now := m.engine.ElapsedMS()
	m.adventureNPCDecide(n, now)
	m.adventureCastTick(now + 1)
	m.adventureCastTick(now + 2)
	p := m.adventure.players[1]
	action := battleengine.Action{PlayerID: 1, Move: battleengine.DirectionUp, PlaceBomb: true}
	m.adventureInput(&action)
	if p.moveLocks != 1 || p.bombLocks != 1 || action.Move != battleengine.DirectionNone || action.PlaceBomb {
		t.Fatalf("locks %d/%d action %+v", p.moveLocks, p.bombLocks, action)
	}
	m.adventureCastTick(now + 1100)
	if p.moveLocks != 0 || p.bombLocks != 0 {
		t.Fatalf("locks after restore %d/%d", p.moveLocks, p.bombLocks)
	}
}

func TestAdventureNPCSkillDiesWithItsPreparingCaster(t *testing.T) {
	prop := mapdata.AdventureProp{ID: 21001, Tier: 1, PrepareTime: 1000, AffectObject: 4, AffectCount: 1, Effects: map[string]float64{"HP": -30}}
	m, n := adventureSkillMatch(t, prop, "1")
	now := m.engine.ElapsedMS()
	m.adventureNPCDecide(n, now)
	m.applyAdventureNPCDeath(n, nil)
	m.adventureCastTick(now + 1001)
	m.adventureCastTick(now + 1002)
	if len(m.adventure.casts) != 0 || m.adventure.players[1].hp != 100 {
		t.Fatalf("casts %d HP %d", len(m.adventure.casts), m.adventure.players[1].hp)
	}
}

func TestAdventureNPCSkillTurnsPlayersIntoPigsForTheAffectTime(t *testing.T) {
	prop := mapdata.AdventureProp{ID: 21001, Tier: 103, AffectObject: 4, AffectShape: 0, AffectFactor: 20, AffectCount: 1, AffectTime: 6000, RestoreAttr: true,
		Effects: map[string]float64{"SpecialAvatarType": 57}}
	m, n := adventureSkillMatch(t, prop, "0")
	now := m.engine.ElapsedMS()
	m.adventureNPCDecide(n, now)
	m.adventureCastTick(now + 1)
	m.adventureCastTick(now + 2)
	m.adventureCastTick(now + 6100) // the prop ends; the avatar stays to its own end
	p := m.adventure.players[1]
	if p.avatar != 57 || p.avatarUntil != now+2+6000 {
		t.Fatalf("avatar %d until %d", p.avatar, p.avatarUntil)
	}
}
