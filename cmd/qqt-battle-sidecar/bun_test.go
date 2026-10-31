package main

import (
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

// Map-806-like houses: team 1 at 1,4 and team 2 at 1,8.
func bunTestRule() *bunRule {
	entry := mapdata.CompetitiveMap{ObjectiveCells: []mapdata.CompetitiveCell{{Row: 1, Col: 4}, {Row: 1, Col: 8}}}
	begin := game.GameBeginData{Players: []game.PlayerGameInfo{{PlayerID: 6, TeamID: 1}, {PlayerID: 7, TeamID: 1}, {PlayerID: 8, TeamID: 2}, {PlayerID: 9, TeamID: 2}}}
	return newBunRule(entry, begin)
}

func bunActor(id uint16, team byte, cell battleengine.Cell) battleengine.Actor {
	return battleengine.Actor{Participant: battleengine.Participant{PlayerID: id, TeamID: team}, Position: battleengine.PositionAtCellCenter(cell), State: battleengine.ActorActive}
}

func TestBunHousesFollowGameBeginOrder(t *testing.T) {
	rule := bunTestRule()
	if rule.houses[0] != (bunHouse{team: 1, anchor: battleengine.Cell{Row: 1, Col: 4}, own: 2}) ||
		rule.houses[1] != (bunHouse{team: 2, anchor: battleengine.Cell{Row: 1, Col: 8}, own: 2}) {
		t.Fatalf("houses = %+v", rule.houses)
	}
	if rule.houseAt(battleengine.Cell{Row: 3, Col: 5}) != &rule.houses[0] || rule.houseAt(battleengine.Cell{Row: 4, Col: 5}) != nil {
		t.Fatal("house footprint is not 3x3 from the anchor")
	}
}

func TestBunRequestsFollowPosition(t *testing.T) {
	rule := bunTestRule()
	// In the opposing house with nothing carried: take its bun.
	schema, request, wanted := rule.wanted(bunActor(8, 2, battleengine.Cell{Row: 3, Col: 5}))
	if !wanted || schema != game.RequestGetBun || request.BunID != bunHomeBun || request.BunTeamID != 1 {
		t.Fatalf("take = %v %+v %v", schema, request, wanted)
	}
	// In its own house with nothing carried: nothing to do.
	if _, _, wanted := rule.wanted(bunActor(6, 1, battleengine.Cell{Row: 3, Col: 5})); wanted {
		t.Fatal("asked for something in its own house")
	}
	// Carrying, in its own house: put the bun down.
	rule.carried[8] = 1
	schema, request, wanted = rule.wanted(bunActor(8, 2, battleengine.Cell{Row: 2, Col: 9}))
	if !wanted || schema != game.RequestPutBun || request.BunTeamID != 1 {
		t.Fatalf("put = %v %+v %v", schema, request, wanted)
	}
	// An empty opposing house offers nothing.
	rule.carried[8] = bunNone
	rule.houses[0].own = 0
	if _, _, wanted := rule.wanted(bunActor(8, 2, battleengine.Cell{Row: 3, Col: 5})); wanted {
		t.Fatal("asked an empty house for a bun")
	}
}

func TestBunTakeAndPutKeepTheCount(t *testing.T) {
	rule := bunTestRule()
	taker := bunActor(8, 2, battleengine.Cell{Row: 3, Col: 5})
	_, take, _ := rule.wanted(taker)
	if !rule.allowTake(take, taker) {
		t.Fatal("valid take refused")
	}
	rule.applyTake(take)
	if rule.houses[0].own != 1 || rule.carrying(8) != 1 {
		t.Fatalf("after take: house %+v carried %d", rule.houses[0], rule.carrying(8))
	}
	if rule.allowTake(take, taker) {
		t.Fatal("a carrier may take a second bun")
	}
	home := bunActor(8, 2, battleengine.Cell{Row: 2, Col: 9})
	_, put, _ := rule.wanted(home)
	wrong := put
	wrong.PosX, wrong.PosY = take.PosX, take.PosY // in the opposing house
	if rule.allowPut(wrong, home) {
		t.Fatal("put accepted in the opposing house")
	}
	if !rule.allowPut(put, home) {
		t.Fatal("valid put refused")
	}
	rule.applyPut(put)
	if rule.houses[1].own != 2 || rule.houses[1].captured != 1 || rule.carrying(8) != bunNone {
		t.Fatalf("after put: house %+v carried %d", rule.houses[1], rule.carrying(8))
	}
}

func TestBunDeaths(t *testing.T) {
	rule := bunTestRule()
	where := battleengine.PositionAtCellCenter(battleengine.Cell{Row: 6, Col: 6})
	// Killed by a player who carries nothing: the killer takes the bun.
	rule.carried[8] = 1
	rule.onDeath(8, 6, true, where)
	if rule.carrying(6) != 1 || rule.carrying(8) != bunNone || len(rule.loose) != 0 {
		t.Fatalf("kill handover: carried %v loose %v", rule.carried, rule.loose)
	}
	// Killed by a carrier: the bun drops where the message says.
	rule.carried[9] = 1
	rule.onDeath(9, 6, true, where)
	if len(rule.loose) != 1 || rule.loose[0] != (looseBun{cell: battleengine.Cell{Row: 6, Col: 6}, team: 1}) || rule.carrying(9) != bunNone {
		t.Fatalf("kill by a carrier: carried %v loose %v", rule.carried, rule.loose)
	}
	// A carrier's own bubble: the bun drops.
	rule.onDeath(6, 0, false, battleengine.PositionAtCellCenter(battleengine.Cell{Row: 7, Col: 2}))
	if len(rule.loose) != 2 || rule.carrying(6) != bunNone {
		t.Fatalf("trap death: carried %v loose %v", rule.carried, rule.loose)
	}
	// Anyone carrying nothing may pick a loose bun up.
	picker := bunActor(7, 1, battleengine.Cell{Row: 7, Col: 2})
	schema, request, wanted := rule.wanted(picker)
	if !wanted || schema != game.RequestGetBun || request.BunID != bunLooseBun || request.BunTeamID != 1 || !rule.allowTake(request, picker) {
		t.Fatalf("loose pickup = %v %+v %v", schema, request, wanted)
	}
	rule.applyTake(request)
	if len(rule.loose) != 1 || rule.carrying(7) != 1 {
		t.Fatalf("after loose pickup: carried %v loose %v", rule.carried, rule.loose)
	}
}

func TestArbitratorAnnouncesOnlyValidBunActions(t *testing.T) {
	match := treasureMatch(t, 1)
	match.bun = &bunRule{carried: map[uint16]int16{1: bunNone, 2: bunNone}}
	match.bun.houses = [2]bunHouse{{team: 1, anchor: battleengine.Cell{Row: 0, Col: 0}, own: 1}, {team: 2, anchor: battleengine.Cell{Row: 0, Col: 2}, own: 1}}
	match.carry = match.bun
	// Player 2 asks for a bun from its own house (column 4, outside house
	// 1's columns 0..2): refused, no answer.
	match.arbitrateBun(game.RequestGetBun, game.BunActionEvent{PlayerID: 2, PosX: 180, PosY: 60, BunID: bunHomeBun, BunTeamID: 2})
	if len(match.outbound) != 0 || len(match.takeTCP()) != 0 {
		t.Fatal("an invalid take was announced")
	}
	// From player 1's house: granted to everyone and mirrored to the server.
	match.arbitrateBun(game.RequestGetBun, game.BunActionEvent{PlayerID: 2, PosX: 20, PosY: 60, BunID: bunHomeBun, BunTeamID: 1})
	if len(match.outbound) != 1 || match.outbound[0].data.DataID != game.NotifyPlayerGetBun || match.outbound[0].arbitratorOnly {
		t.Fatalf("outbound = %+v", match.outbound)
	}
	if copies := mirrored(t, match, game.NotifyPlayerGetBun); len(copies) != 1 {
		t.Fatalf("mirrored %d copies", len(copies))
	}
	if match.bun.houses[0].own != 0 || match.bun.carrying(2) != 1 {
		t.Fatalf("not applied: %+v %v", match.bun.houses, match.bun.carried)
	}
}

func TestBunLeaverReturnsTheBunHome(t *testing.T) {
	rule := bunTestRule()
	rule.houses[0].own = 1
	rule.carried[8] = 1 // took team 1's bun
	rule.onDeparture(8)
	if rule.houses[0].own != 2 || rule.houses[1].own != 2 || rule.carrying(8) != bunNone {
		t.Fatalf("after leaving: houses %+v carried %v", rule.houses, rule.carried)
	}
	rule.onDeparture(9) // carries nothing
	if rule.houses[0].own != 2 || rule.houses[1].own != 2 {
		t.Fatalf("an empty-handed leaver changed the houses: %+v", rule.houses)
	}
}
