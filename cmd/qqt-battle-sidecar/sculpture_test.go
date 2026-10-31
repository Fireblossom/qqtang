package main

import (
	"encoding/json"
	"strings"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

// sculptureTestRule is players 1 (team 2) and 2 (team 1) on an open 15x13
// field with statues at 11,3 (first: team 2, the first player's) and 11,11,
// a fragment 162 at 5,5 and a fragment 161 under a wall at 6,6.
func sculptureTestRule(t *testing.T) *sculptureRule {
	t.Helper()
	grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 15*13)}
	for index := range grid.Cells {
		grid.Cells[index].FlamePassable = true
	}
	grid.Cells[6*15+6] = battleengine.Tile{Kind: battleengine.CellBreakable, Durability: 1}
	participant := func(id uint16, team byte, col int16) battleengine.Participant {
		return battleengine.Participant{PlayerID: id, TeamID: team, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: col},
			SpeedPixelsPerSecond: 80, BombCapacity: 2, BombPower: 2}
	}
	engine, err := battleengine.New(battleengine.Config{
		Seed: 1, Grid: grid,
		Rules:        battleengine.Rules{TickMS: 20, RoundDurationMS: 240_000, BombFuseMS: 3000, FlameDurationMS: 500, ActorHalfSizePixels: 19, ExternalConclusion: true},
		Participants: []battleengine.Participant{participant(1, 2, 1), participant(2, 1, 13)},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := mapdata.CompetitiveMap{ObjectiveCells: []mapdata.CompetitiveCell{{Row: 11, Col: 3}, {Row: 11, Col: 11}}}
	begin := game.GameBeginData{Players: []game.PlayerGameInfo{{PlayerID: 1, TeamID: 2}, {PlayerID: 2, TeamID: 1}}}
	fragments := []battleengine.Pickup{{SceneID: 162, Cell: battleengine.Cell{Row: 5, Col: 5}}, {SceneID: 161, Cell: battleengine.Cell{Row: 6, Col: 6}}}
	return newSculptureRule(engine, entry, begin, fragments)
}

func sculptureActor(id uint16, team byte, cell battleengine.Cell) battleengine.Actor {
	return battleengine.Actor{Participant: battleengine.Participant{PlayerID: id, TeamID: team}, Position: battleengine.PositionAtCellCenter(cell), State: battleengine.ActorActive}
}

func held(rule *sculptureRule, playerID uint16, action uint8) uint8 {
	actor, _ := actorByID(rule.engine.Actors(), playerID)
	for _, slot := range actor.HeldActions {
		if slot.ActionID == action {
			return slot.Count
		}
	}
	return 0
}

func TestSculptureSetup(t *testing.T) {
	rule := sculptureTestRule(t)
	if rule.statues[0].team != 2 || rule.statues[0].cell != (battleengine.Cell{Row: 11, Col: 3}) || rule.statues[1].team != 1 {
		t.Fatalf("statues = %+v", rule.statues)
	}
	if len(rule.loose) != 2 || rule.loose[0].material != 13 || rule.loose[1].material != 12 {
		t.Fatalf("fragments = %+v", rule.loose)
	}
	// The walled fragment can be neither seen nor taken.
	if rule.looseAt(battleengine.Cell{Row: 6, Col: 6}, 0, true) >= 0 || !rule.view().Loose[1].Hidden {
		t.Fatal("a fragment under a wall is reachable")
	}
}

func TestSculptureTakeAndDeposit(t *testing.T) {
	rule := sculptureTestRule(t)
	taker := sculptureActor(1, 2, battleengine.Cell{Row: 5, Col: 5})
	schema, take, wanted := rule.wanted(taker)
	if !wanted || schema != game.RequestGetBun || take.BunID != sculptureFragmentBun || take.BunTeamID != 13 || !rule.allowTake(take, taker) {
		t.Fatalf("take = %v %+v %v", schema, take, wanted)
	}
	rule.applyTake(take)
	if rule.carrying(1) != 13 || len(rule.loose) != 1 || held(rule, 1, sculpturePutDownItem) != 1 {
		t.Fatalf("after take: carried %d loose %+v item66 %d", rule.carrying(1), rule.loose, held(rule, 1, sculpturePutDownItem))
	}
	// The opposing statue does not take it; the own one does.
	if _, _, wanted := rule.wanted(sculptureActor(1, 2, battleengine.Cell{Row: 11, Col: 11})); wanted {
		t.Fatal("asked to deposit on the opposing statue")
	}
	home := sculptureActor(1, 2, battleengine.Cell{Row: 11, Col: 3})
	schema, put, wanted := rule.wanted(home)
	if !wanted || schema != game.RequestPutBun || put.BunID != sculptureDepositBun || put.BunTeamID != 13 || !rule.allowPut(put, home) {
		t.Fatalf("put = %v %+v %v", schema, put, wanted)
	}
	rule.applyPut(put)
	if rule.statues[0].progress != 1 || rule.statues[0].slots[0] != 13 || rule.carrying(1) != bunNone || held(rule, 1, sculpturePutDownItem) != 0 {
		t.Fatalf("after put: statue %+v carried %d", rule.statues[0], rule.carrying(1))
	}
}

func TestSculpturePutDownAndDeaths(t *testing.T) {
	rule := sculptureTestRule(t)
	rule.carried[1] = 12
	rule.engine.GrantHeldAction(1, sculpturePutDownItem, 1)
	rule.putDown(1, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 2, Col: 2}))
	if rule.carrying(1) != bunNone || rule.looseAt(battleengine.Cell{Row: 2, Col: 2}, 12, false) < 0 || held(rule, 1, sculpturePutDownItem) != 0 {
		t.Fatalf("put down: carried %d loose %+v", rule.carrying(1), rule.loose)
	}
	// Killed by an empty-handed player: the killer takes it and item 66.
	rule.carried[1] = 14
	rule.bombLeft[1] = true
	rule.onDeath(1, 2, true, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 3, Col: 3}))
	if rule.carrying(2) != 14 || rule.carrying(1) != bunNone || held(rule, 2, sculpturePutDownItem) != 1 || held(rule, 1, sculptureBombItem) != 1 {
		t.Fatalf("kill: carried %v, killer 66 %d, victim 48 %d", rule.carried, held(rule, 2, sculpturePutDownItem), held(rule, 1, sculptureBombItem))
	}
	// A trapped-to-death carrier drops it on the message's cell.
	rule.onDeath(2, 0, false, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 4, Col: 4}))
	if rule.carrying(2) != bunNone || rule.looseAt(battleengine.Cell{Row: 4, Col: 4}, 14, false) < 0 {
		t.Fatalf("drop: carried %v loose %+v", rule.carried, rule.loose)
	}
}

func TestSculptureDamage(t *testing.T) {
	rule := sculptureTestRule(t)
	statue := &rule.statues[0] // 11,3
	statue.progress, statue.slots = 2, [sculpturePieces]byte{12, 15}
	// A bubble below the statue (12,3): the statue is "up" from it. Placed
	// at time 9: start at entry (9&7)+1 = 2, up (10,3).
	rule.damage(battleengine.Cell{Row: 12, Col: 3}, 9)
	if statue.progress != 1 || rule.looseAt(battleengine.Cell{Row: 10, Col: 3}, 15, false) < 0 {
		t.Fatalf("damage: statue %+v loose %+v", *statue, rule.loose)
	}
	// Time 0 starts at the right-hand cell.
	rule.damage(battleengine.Cell{Row: 11, Col: 3}, 0)
	if statue.progress != 0 || rule.looseAt(battleengine.Cell{Row: 11, Col: 4}, 12, false) < 0 {
		t.Fatalf("second damage: statue %+v loose %+v", *statue, rule.loose)
	}
	// Empty and complete statues are left alone.
	rule.damage(battleengine.Cell{Row: 11, Col: 3}, 0)
	statue.progress = sculpturePieces
	rule.damage(battleengine.Cell{Row: 11, Col: 3}, 0)
	if statue.progress != sculpturePieces || len(rule.loose) != 4 {
		t.Fatalf("immune statues changed: %+v loose %+v", *statue, rule.loose)
	}
}

func TestSculptureRulesReliveAfterFiveSeconds(t *testing.T) {
	rules, respawn, err := ruleOptions(mapdata.CompetitiveMap{Rule: mapdata.CompetitiveRuleSculpture}, 1, 1)
	if err != nil || respawn == nil || respawn.delayMS != 5000 || respawn.waitFree || !rules.ExternalConclusion || rules.LooseObjectSceneIDs != [battleengine.MaxLooseObjectKinds]uint32{161, 162, 163, 164, 165} {
		t.Fatalf("rules %+v respawn %+v err %v", rules, respawn, err)
	}
}

func TestSculptureViewSlotsAreNumbers(t *testing.T) {
	rule := sculptureTestRule(t)
	rule.statues[0].progress, rule.statues[0].slots = 2, [sculpturePieces]byte{12, 15}
	encoded, err := json.Marshal(rule.view())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"slots":[12,15]`) {
		t.Fatalf("view = %s", encoded)
	}
}
