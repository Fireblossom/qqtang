package main

import (
	"encoding/hex"
	"io"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

// treasureMatch is a local player 1 against player 2 on an open 5x3 field
// under the rule-5 options, with a 3-point gem next to player 1.
func treasureMatch(t *testing.T, arbitrator uint16) *onlineMatch {
	t.Helper()
	return treasureMatchOn(t, arbitrator, nil)
}

// treasureMatchOn is treasureMatch with edit applied to the field first.
func treasureMatchOn(t *testing.T, arbitrator uint16, edit func(*battleengine.Grid)) *onlineMatch {
	t.Helper()
	grid := battleengine.Grid{Width: 5, Height: 3, Cells: make([]battleengine.Tile, 15)}
	for index := range grid.Cells {
		grid.Cells[index].FlamePassable = true
	}
	if edit != nil {
		edit(&grid)
	}
	participant := func(id uint16, team byte, col int16) battleengine.Participant {
		return battleengine.Participant{PlayerID: id, TeamID: team, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: col},
			SpeedPixelsPerSecond: 80, BombCapacity: 2, BombPower: 2}
	}
	rules, respawn, err := ruleOptions(mapdata.CompetitiveMap{ID: 1101, Rule: mapdata.CompetitiveRuleTreasure}, 1, arbitrator)
	if err != nil {
		t.Fatal(err)
	}
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 180_000, 3000, 500, 19
	engine, err := battleengine.New(battleengine.Config{
		Seed: 1, Grid: grid, Rules: rules,
		Participants: []battleengine.Participant{participant(1, 1, 1), participant(2, 2, 4)},
		Pickups:      []battleengine.Pickup{{SceneID: 152, Cell: battleengine.Cell{Row: 1, Col: 2}, State: battleengine.PickupAvailable}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := battleengine.NewRuntime(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &onlineMatch{
		runtime: runtime, engine: engine, local: 1, localUIN: 1000001, arbitrator: arbitrator,
		uins: map[uint16]uint32{1: 1000001, 2: 1000002}, trapMS: defaultTrapMS, log: io.Discard,
		peerReady: map[uint16]bool{}, peerAnswered: map[uint16]bool{}, lastNumber: map[uint16]uint32{}, bombs: map[uint32]wireBomb{},
		collected: map[pickupKey]uint16{}, fieldContacts: map[fieldContactKey]bool{}, itemUses: map[[3]uint32]bool{},
		useRequested: map[uint8]uint32{}, preparedRequested: map[uint32]uint32{},
		pickupRequests: map[pickupKey]pickupRequest{}, sceneRequests: map[sceneRequestKey]uint32{},
		respawn: respawn, eliminatedAt: map[uint16]uint32{},
	}
}

func TestOnlyTreasureJoinsRuleOne(t *testing.T) {
	if _, respawn, err := ruleOptions(mapdata.CompetitiveMap{Rule: mapdata.CompetitiveRuleOrdinary}, 1, 1); err != nil || respawn != nil {
		t.Fatalf("rule 1: respawn %v, %v", respawn, err)
	}
	if _, _, err := ruleOptions(mapdata.CompetitiveMap{Rule: mapdata.CompetitiveRuleBun}, 1, 1); err == nil {
		t.Fatal("bun maps were accepted")
	}
}

func mirrored(t *testing.T, match *onlineMatch, schema uint16) []game.GameEvent {
	t.Helper()
	var found []game.GameEvent
	for _, packet := range match.takeTCP() {
		payload, _ := hex.DecodeString(packet.Payload)
		if event, err := game.ParseGameEventPayload(payload); err == nil && event.Schema == schema {
			found = append(found, event)
		}
	}
	return found
}

func advanceTo(t *testing.T, match *onlineMatch, clock uint32) {
	t.Helper()
	for match.engine.ElapsedMS() < clock {
		if _, err := match.runtime.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArbitratorRelivesDeadPlayerAfterFiveSeconds(t *testing.T) {
	match := treasureMatch(t, 1)
	events, err := match.runtime.AcceptNativeElimination(2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	match.noteEliminations(events)
	if match.engine.Terminal().Ended {
		t.Fatal("the round ended when one team was wiped out")
	}
	diedAt := match.eliminatedAt[2]
	advanceTo(t, match, diedAt+4980)
	if relived := match.respawnDue(); len(relived) != 0 {
		t.Fatalf("relived early: %+v", relived)
	}
	advanceTo(t, match, diedAt+5000)
	relived := match.respawnDue()
	if len(relived) != 1 || relived[0].Kind != battleengine.EventActorRespawned || relived[0].PlayerID != 2 {
		t.Fatalf("relive events = %+v", relived)
	}
	actor, _ := actorByID(match.engine.Actors(), 2)
	if actor.State != battleengine.ActorActive || actor.Position.Cell() != (battleengine.Cell{Row: 1, Col: 4}) {
		t.Fatalf("relived actor = %+v", actor)
	}
	copies := mirrored(t, match, game.NotifyPlayerRelive)
	if len(copies) != 1 {
		t.Fatalf("relive copies = %d, want 1", len(copies))
	}
	relive, err := game.ParsePlayerReliveEvent(copies[0])
	if err != nil || relive.PlayerID != 2 || relive.Row != 1 || relive.Col != 4 {
		t.Fatalf("relive = %+v, %v", relive, err)
	}
	if len(match.outbound) != 1 || match.outbound[0].data.DataID != game.NotifyPlayerRelive {
		t.Fatalf("relive is not broadcast: %+v", match.outbound)
	}
}

func TestBubbleOnSpawnDoesNotHoldRelive(t *testing.T) {
	match := treasureMatch(t, 1)
	events, err := match.runtime.AcceptNativeElimination(2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	match.noteEliminations(events)
	advanceTo(t, match, match.eliminatedAt[2]+5000)
	if _, err := match.runtime.AcceptHumanBombPlacement(1, battleengine.Cell{Row: 1, Col: 4}, 2, 0, match.engine.ElapsedMS()); err != nil {
		t.Fatal(err)
	}
	if relived := match.respawnDue(); len(relived) != 1 {
		t.Fatalf("relive events = %+v, want one", relived)
	}
}

// A walkable map element on the spawn cell (scene grid +0xE78) holds every
// relive back, as 0x00615AAE returns at the first blocked spawn.
func TestMapElementOnSpawnHoldsRelive(t *testing.T) {
	match := treasureMatchOn(t, 1, func(grid *battleengine.Grid) {
		grid.Cells[1*5+4].MapElementOccupied = true
	})
	events, err := match.runtime.AcceptNativeElimination(2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	match.noteEliminations(events)
	advanceTo(t, match, match.eliminatedAt[2]+5000)
	if relived := match.respawnDue(); len(relived) != 0 {
		t.Fatalf("relived onto a map element: %+v", relived)
	}
}

func TestFollowerAppliesArbitratorRelive(t *testing.T) {
	match := treasureMatch(t, 2)
	if _, err := match.runtime.AcceptNativeElimination(1, 2, nil); err != nil {
		t.Fatal(err)
	}
	body, _ := (game.PlayerReliveEvent{PlayerID: 1, ClientTime: 9000, Row: 0, Col: 3}).MarshalNetworkBinary()
	message := game.BattleMessageData{DataID: game.NotifyPlayerRelive, Data: body}
	if events := match.applyRemote(1, message); len(events) != 0 {
		t.Fatal("a relive from a non-arbitrator was applied")
	}
	events := match.applyRemote(2, message)
	if len(events) != 1 || events[0].Kind != battleengine.EventActorRespawned {
		t.Fatalf("events = %+v", events)
	}
	actor, _ := actorByID(match.engine.Actors(), 1)
	if actor.State != battleengine.ActorActive || actor.Position.Cell() != (battleengine.Cell{Row: 0, Col: 3}) {
		t.Fatalf("relived actor = %+v", actor)
	}
	if !match.localActive() {
		t.Fatal("the local player cannot act after its relive")
	}
}

func TestDeathWithGemsPublishesOnlyGems(t *testing.T) {
	match := treasureMatch(t, 1)
	position := battleengine.PositionAtCellCenter(battleengine.Cell{Row: 1, Col: 2})
	if _, err := match.runtime.AcceptNativePickup(1, 152, position); err != nil {
		t.Fatal(err)
	}
	actor, _ := actorByID(match.engine.Actors(), 1)
	if actor.CarriedObjectives[2] != 1 {
		t.Fatalf("carried = %v", actor.CarriedObjectives)
	}
	if _, _, err := match.runtime.AcceptNativeActorHitFrom(1, 2, actor.Position, false); err != nil {
		t.Fatal(err)
	}
	events, err := match.engine.ConfirmTrappedDeath(1)
	if err != nil {
		t.Fatal(err)
	}
	match.publishEvents(match.engine, events)
	deaths := mirrored(t, match, game.NotifyPlayerDieEvent)
	if len(deaths) != 1 {
		t.Fatalf("death copies = %d", len(deaths))
	}
	death, err := game.ParsePlayerDeathEvent(deaths[0])
	if err != nil || len(death.Items) != 1 || death.Items[0].ItemID != 152 {
		t.Fatalf("death = %+v, %v", death, err)
	}
}
