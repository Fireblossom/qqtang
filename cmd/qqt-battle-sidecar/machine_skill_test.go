package main

import (
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

// skillPlace puts a player at a cell centre facing a direction.
func skillPlace(t *testing.T, m *onlineMatch, player uint16, cell battleengine.Cell, facing battleengine.Direction) {
	t.Helper()
	if err := m.engine.ApplyVerifiedMovementCheckpoint(player, battleengine.PositionAtCellCenter(cell)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.runtime.Step([]battleengine.Action{{PlayerID: player, Move: facing}}); err != nil {
		t.Fatal(err)
	}
	if err := m.engine.ApplyVerifiedMovementCheckpoint(player, battleengine.PositionAtCellCenter(cell)); err != nil {
		t.Fatal(err)
	}
	if actor, _ := actorByID(m.engine.Actors(), player); actor.Facing != facing {
		t.Fatalf("player %d faces %d, want %d", player, actor.Facing, facing)
	}
}

// skillPair is the arbitrator (player 1, first team, left half) and a peer
// (player 2, right half), both at 5000 ms.
func skillPair(t *testing.T) (*onlineMatch, *onlineMatch) {
	owner, peer := machineMatch(t, 1), machineMatch(t, 2)
	for _, m := range []*onlineMatch{owner, peer} {
		advanceTo(t, m, 5000)
		skillPlace(t, m, 1, battleengine.Cell{Row: 6, Col: 3}, battleengine.DirectionRight)
		skillPlace(t, m, 2, battleengine.Cell{Row: 2, Col: 12}, battleengine.DirectionLeft)
	}
	return owner, peer
}

func destinations(t *testing.T, events []game.GameEvent) []battleengine.Cell {
	t.Helper()
	var cells []battleengine.Cell
	for _, event := range events {
		throw, err := game.ParseKickBombAction(event)
		if err != nil {
			t.Fatal(err)
		}
		cells = append(cells, battleengine.Cell{Row: int16(throw.BombDestRowAndCol >> 4), Col: int16(throw.BombDestRowAndCol & 15)})
	}
	return cells
}

func TestMachineSkillThrowPatterns(t *testing.T) {
	for _, test := range []struct {
		skill int8
		want  []battleengine.Cell
	}{
		{0, []battleengine.Cell{{Row: 6, Col: 10}, {Row: 7, Col: 9}, {Row: 5, Col: 11}, {Row: 4, Col: 12}}},  // 凤舞
		{3, []battleengine.Cell{{Row: 6, Col: 10}, {Row: 7, Col: 10}, {Row: 5, Col: 10}, {Row: 4, Col: 10}}}, // 长龙
		{4, []battleengine.Cell{{Row: 6, Col: 10}, {Row: 7, Col: 11}, {Row: 5, Col: 11}, {Row: 6, Col: 12}}}, // 天牢
	} {
		owner, peer := skillPair(t)
		owner.requestMachineSkill(test.skill)
		notices := mirrored(t, owner, game.NotifyWrestleSkill)
		if len(notices) != 1 {
			t.Fatalf("skill %d: %d notices", test.skill, len(notices))
		}
		// The TCP queue is drained by mirrored; rebuild the throws from the
		// engine and check the peer gets the same bubbles from 0x115B + 0x1159.
		var cells []battleengine.Cell
		for _, bomb := range owner.engine.Bombs() {
			cells = append(cells, bomb.Cell)
		}
		if len(cells) != len(test.want) {
			t.Fatalf("skill %d threw to %v, want %v", test.skill, cells, test.want)
		}
		for i, cell := range test.want {
			if cells[i] != cell {
				t.Fatalf("skill %d threw to %v, want %v", test.skill, cells, test.want)
			}
		}
		notice := owner.takeMachineSkillNotices()
		if len(notice) != 1 || notice[0].Pose != test.skill || notice[0].Player != 1 {
			t.Fatalf("notices %+v", notice)
		}
		peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyWrestleSkill, Data: notices[0].Body})
		if got := peer.takeMachineSkillNotices(); len(got) != 1 || got[0].Pose != test.skill {
			t.Fatalf("peer notices %+v", got)
		}
		if len(peer.engine.Bombs()) != 0 {
			t.Fatal("a follower threw skill bubbles itself")
		}
	}
}

func TestMachineSkillThrowsReachPeersAsArbitrator1159(t *testing.T) {
	owner, peer := skillPair(t)
	owner.requestMachineSkill(2)
	throws := mirrored(t, owner, game.PlayerThrowBomb)
	if cells := destinations(t, throws); len(cells) != 4 {
		t.Fatalf("throws %v", cells)
	}
	for _, throw := range throws {
		peer.applyRemote(1, game.BattleMessageData{DataID: game.PlayerThrowBomb, Data: throw.Body})
	}
	if len(peer.engine.Bombs()) != 4 {
		t.Fatalf("peer has %d bubbles", len(peer.engine.Bombs()))
	}
	// A non-arbitrator may not throw for someone else.
	other := machineMatch(t, 1)
	advanceTo(t, other, 5000)
	other.applyRemote(2, game.BattleMessageData{DataID: game.PlayerThrowBomb, Data: throws[0].Body})
	if len(other.engine.Bombs()) != 0 {
		t.Fatal("accepted a throw for player 1 from player 2")
	}
}

func TestMachineSkillExtraCellsStayOnTheOtherHalf(t *testing.T) {
	owner := machineMatch(t, 1)
	advanceTo(t, owner, 5000)
	// The first team's target band starts at column 7, its extras at 8.
	skillPlace(t, owner, 1, battleengine.Cell{Row: 0, Col: 0}, battleengine.DirectionRight)
	if !machineSkillExtraCell(owner.engine, 0x08, true) || machineSkillExtraCell(owner.engine, 0x07, true) || machineSkillExtraCell(owner.engine, 0x08, false) {
		t.Fatal("extra-cell half test")
	}
	if machineSkillExtraCell(owner.engine, 0xd8, true) {
		t.Fatal("row 13 accepted")
	}
	owner.requestMachineSkill(0)
	// Target 0,7. +0x0F gives 1,6 (own half), -0x0F wraps to row 15 and
	// -0x1E to row 14: only the target bubble is thrown.
	if cells := destinations(t, mirrored(t, owner, game.PlayerThrowBomb)); len(cells) != 1 || cells[0] != (battleengine.Cell{Row: 0, Col: 7}) {
		t.Fatalf("throws from row 0: %v", cells)
	}
}

func TestMachineSkillCooldownAndFacingRefusal(t *testing.T) {
	owner, _ := skillPair(t)
	owner.requestMachineSkill(2)
	advanceTo(t, owner, 6000)
	owner.requestMachineSkill(2) // 1000 ms later: refused
	advanceTo(t, owner, 8020)
	owner.requestMachineSkill(8) // charging notices bypass the cooldown
	owner.requestMachineSkill(2) // more than 2000 ms after the refused one
	var types []byte
	for _, notice := range mirrored(t, owner, game.NotifyWrestleSkill) {
		action, _ := game.ParseWrestleAction(notice)
		types = append(types, action.SkillType&15)
	}
	if len(types) != 4 || types[0] != 2 || types[1] != 9 || types[2] != 8 || types[3] != 2 {
		t.Fatalf("types %v", types)
	}
	// Facing the own half: refused even off cooldown.
	advanceTo(t, owner, 12000)
	skillPlace(t, owner, 1, battleengine.Cell{Row: 6, Col: 3}, battleengine.DirectionLeft)
	owner.requestMachineSkill(4)
	notices := mirrored(t, owner, game.NotifyWrestleSkill)
	action, _ := game.ParseWrestleAction(notices[0])
	if action.SkillType != 2<<4|9 {
		t.Fatalf("facing-left request became %#x", action.SkillType)
	}
	if poses := owner.takeMachineSkillNotices(); poses[len(poses)-1].Pose != -1 {
		t.Fatalf("refused notice pose %+v", poses)
	}
}

func TestMachineSkillRequestWireAndRouting(t *testing.T) {
	_, peer := skillPair(t)
	peer.requestMachineSkill(1)
	requests := mirrored(t, peer, game.RequestWrestle)
	if len(requests) != 1 {
		t.Fatal("no request")
	}
	action, _ := game.ParseWrestleAction(requests[0])
	// Player 2: second team, faces left (2), at 2,12.
	if action.SkillType != 2<<4|1 || action.FirstTeam != 0 || action.PlayerPos != 0x2c || action.PlayerID != 2 || action.Time != peer.engine.ElapsedMS() {
		t.Fatalf("request %+v", action)
	}
	if len(peer.engine.Bombs()) != 0 || len(mirrored(t, peer, game.NotifyWrestleSkill)) != 0 {
		t.Fatal("a follower answered its own request")
	}
	owner, _ := skillPair(t)
	owner.applyRemote(2, game.BattleMessageData{DataID: game.RequestWrestle, Data: requests[0].Body})
	// Target 2,5; the pattern is not mirrored for the second team, so the
	// third extra cell (0,7) is on the centre column and dropped.
	if len(mirrored(t, owner, game.NotifyWrestleSkill)) != 1 || len(owner.engine.Bombs()) != 3 {
		t.Fatalf("arbitrator did not answer: %d bubbles", len(owner.engine.Bombs()))
	}
	// A request naming someone else is ignored.
	owner.applyRemote(1, game.BattleMessageData{DataID: game.RequestWrestle, Data: requests[0].Body})
	if len(mirrored(t, owner, game.NotifyWrestleSkill)) != 0 {
		t.Fatal("answered a forged request")
	}
}

func TestMachineSkillKickSendsBubbleInFrontToTarget(t *testing.T) {
	owner, peer := skillPair(t)
	throw := game.KickBombAction{BombRowAndCol: 0x63, BombDestRowAndCol: 0x64, Power: 2, BombPower: 2, Time: 5000, PlayerID: 2}
	body, _ := throw.MarshalNetworkBinary()
	owner.applyMachineThrow(throw)
	peer.applyRemote(1, game.BattleMessageData{DataID: game.PlayerThrowBomb, Data: body})
	owner.takeTCP()
	owner.requestMachineSkill(6)
	moves := mirrored(t, owner, game.NotifyPlayerMoveBomb)
	if len(moves) != 1 {
		t.Fatal("no kick")
	}
	moved, _ := game.ParseMoveBombEvent(moves[0])
	if moved.FromRow != 6 || moved.FromCol != 4 || moved.ToRow != 6 || moved.ToCol != 10 || moved.PlayerID != 1 || moved.BombPlayerID != 2 || moved.Direction != 0 {
		t.Fatalf("kick %+v", moved)
	}
	if owner.engine.Bombs()[0].Cell != (battleengine.Cell{Row: 6, Col: 10}) {
		t.Fatal("arbitrator did not move the bubble")
	}
	if notices := owner.takeMachineSkillNotices(); len(notices) != 1 || notices[0].Pose != machineSkillKick {
		t.Fatalf("kick pose %+v", notices)
	}
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyPlayerMoveBomb, Data: moves[0].Body})
	if peer.engine.Bombs()[0].Cell != (battleengine.Cell{Row: 6, Col: 10}) {
		t.Fatal("peer did not move the bubble")
	}
}

func TestMachineSkillDodgeOnEveryClient(t *testing.T) {
	owner, peer := skillPair(t)
	owner.requestMachineSkill(7)
	notices := mirrored(t, owner, game.NotifyWrestleSkill)
	peer.applyRemote(1, game.BattleMessageData{DataID: game.NotifyWrestleSkill, Data: notices[0].Body})
	for _, m := range []*onlineMatch{owner, peer} {
		actor, _ := actorByID(m.engine.Actors(), 1)
		if actor.Position.Cell() != (battleengine.Cell{Row: 6, Col: 2}) || actor.HarmProtectionExpiresAt != m.engine.ElapsedMS()+300 {
			t.Fatalf("local %d: dodge to %+v, protected to %d", m.local, actor.Position, actor.HarmProtectionExpiresAt)
		}
		if poses := m.takeMachineSkillNotices(); len(poses) != 1 || poses[0].Pose != machineSkillDodge {
			t.Fatalf("dodge pose %+v", poses)
		}
	}
	if len(owner.engine.Bombs()) != 0 {
		t.Fatal("dodge threw bubbles")
	}
}

func TestMachineSkillTargetUsesTeamHalf(t *testing.T) {
	m := machineMatch(t, 1)
	from := battleengine.Cell{Row: 6, Col: 9}
	if cell, ok := machineSkillTarget(m.engine, from, battleengine.DirectionRight, true); ok && cell.Col < 7 {
		t.Fatalf("first team target %v", cell)
	}
	if cell, ok := machineSkillTarget(m.engine, battleengine.Cell{Row: 6, Col: 12}, battleengine.DirectionLeft, false); !ok || cell != (battleengine.Cell{Row: 6, Col: 5}) {
		t.Fatalf("second team target %v %t", cell, ok)
	}
	if _, ok := machineSkillTarget(m.engine, from, battleengine.DirectionNone, true); ok {
		t.Fatal("target without a facing")
	}
}
