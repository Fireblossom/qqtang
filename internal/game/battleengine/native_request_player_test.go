package battleengine

import "testing"

func nativeRequestConfig(requestPlayerID uint16) Config {
	config := testConfig()
	config.Rules.NativeOutcomeAuthority = true
	config.Rules.TrapDurationMS = 0
	config.Rules.NativeRequestPlayerID = requestPlayerID
	return config
}

func TestNativeRequestPlayerRequiresOutcomeAuthority(t *testing.T) {
	config := testConfig()
	config.Rules.NativeRequestPlayerID = 1
	if _, err := New(config); err == nil {
		t.Fatal("request player without native outcome authority was accepted")
	}
}

func TestNativeRequestPlayerTakesVerifiedFlameHitsOthersDoNot(t *testing.T) {
	blast := []VerifiedExplodedBomb{{OwnerID: 2, Cell: Cell{Row: 1, Col: 2}, BlastRowMin: 1, BlastRowMax: 1, BlastColMin: 1, BlastColMax: 3}}
	for _, requestPlayerID := range []uint16{0, 1} {
		engine := mustEngine(t, nativeRequestConfig(requestPlayerID))
		if _, err := engine.ApplyVerifiedBombExplosionAt(engine.elapsedMS, blast, nil, nil); err != nil {
			t.Fatal(err)
		}
		events, err := engine.Step(nil)
		if err != nil {
			t.Fatal(err)
		}
		localTrapped := engine.actors[0].State == ActorTrapped && hasEvent(events, EventActorTrapped, 1)
		if localTrapped != (requestPlayerID == 1) {
			t.Fatalf("request player %d: local trapped=%t events=%+v", requestPlayerID, localTrapped, events)
		}
		// The other human reports its own hit.
		if engine.actors[1].State != ActorActive {
			t.Fatalf("request player %d: remote human was trapped locally", requestPlayerID)
		}
	}
}

func TestNativeRequestPlayerRequestsPickupWithoutCollecting(t *testing.T) {
	cell := Cell{Row: 0, Col: 1}
	for _, requestPlayerID := range []uint16{0, 1} {
		config := nativeRequestConfig(requestPlayerID)
		config.Pickups = []Pickup{{SceneID: SceneBombCapacitySmall, Cell: cell, State: PickupAvailable}}
		engine := mustEngine(t, config)
		engine.actors[0].MaxBombCapacity = engine.actors[0].BombCapacity + 1
		before := engine.actors[0].BombCapacity
		engine.actors[0].Position = PositionAtCellCenter(cell)
		events, err := engine.Step(nil)
		if err != nil {
			t.Fatal(err)
		}
		requested := false
		for _, event := range events {
			requested = requested || (event.Kind == EventPickupCollectRequested && event.PlayerID == 1 && event.SceneID == SceneBombCapacitySmall)
		}
		if requested != (requestPlayerID == 1) {
			t.Fatalf("request player %d: pickup requested=%t events=%+v", requestPlayerID, requested, events)
		}
		if engine.pickups[0].State != PickupAvailable || engine.actors[0].BombCapacity != before {
			t.Fatalf("request player %d: pickup was collected locally: %+v actor=%+v", requestPlayerID, engine.pickups[0], engine.actors[0])
		}
	}
}

func TestNativeRequestPlayerRequestsKillAndRescue(t *testing.T) {
	for _, sameTeam := range []bool{false, true} {
		config := nativeRequestConfig(1)
		config.Participants = append(config.Participants, testParticipant(3, 1, ParticipantHuman, Cell{Row: 2, Col: 4}))
		engine := mustEngine(t, config)
		target := 1 // player 2, the opponent
		if sameTeam {
			target = 2 // player 3, a teammate
		}
		engine.actors[target].State = ActorTrapped
		engine.actors[0].Position = engine.actors[target].Position
		events, err := engine.Step(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := EventActorEliminationRequested
		if sameTeam {
			want = EventActorRescueRequested
		}
		if !hasEvent(events, want, engine.actors[target].PlayerID) || engine.actors[target].State != ActorTrapped {
			t.Fatalf("same team %t: events=%+v target=%+v", sameTeam, events, engine.actors[target])
		}
	}
}

func TestNativeRequestPlayerLeavesOtherHumansToTheirClients(t *testing.T) {
	engine := mustEngine(t, nativeRequestConfig(1))
	engine.actors[0].State = ActorTrapped
	engine.actors[1].Position = engine.actors[0].Position
	events, err := engine.Step(nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasEvent(events, EventActorEliminationRequested, 1) || engine.actors[0].State != ActorTrapped {
		t.Fatalf("remote human contact produced a local request or outcome: %+v", events)
	}
}
