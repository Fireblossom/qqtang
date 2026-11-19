package battleengine

import "testing"

func tutorialTestEngine(t *testing.T) *Engine {
	t.Helper()
	config := testConfig()
	config.Participants = config.Participants[:1]
	config.Rules.NativeTutorial = true
	config.Rules.ExternalConclusion = true
	config.Rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	return mustEngine(t, config)
}

func TestTutorialRulesStartWithOnePlayer(t *testing.T) {
	config := testConfig()
	config.Participants = config.Participants[:1]
	if _, err := New(config); err == nil {
		t.Fatal("one player started without tutorial rules")
	}
	engine := tutorialTestEngine(t)
	if err := engine.AddActor(Participant{PlayerID: 2, RoleID: 3, TeamID: 1, BombCapacity: 2, BombPower: 2, SpeedRate: 2}, Cell{Row: 1, Col: 3}); err != nil {
		t.Fatal(err)
	}
	if err := engine.AddActor(Participant{PlayerID: 2, TeamID: 1, BombCapacity: 2, BombPower: 2, SpeedRate: 2}, Cell{Row: 0, Col: 0}); err == nil {
		t.Fatal("the same actor was added twice")
	}
	actors := engine.Actors()
	if len(actors) != 2 || actors[1].PlayerID != 2 || actors[1].SpeedPixelsPerSecond != 72 || actors[1].Position != PositionAtCellCenter(Cell{Row: 1, Col: 3}) {
		t.Fatalf("actors %+v", actors)
	}
	// One team left alive does not end a tutorial game.
	if _, err := engine.Step([]Action{{PlayerID: 1}, {PlayerID: 2}}); err != nil || engine.Terminal().Ended {
		t.Fatalf("step: %v, outcome %+v", err, engine.Terminal())
	}
}

func TestTutorialTeleportTrapAndImmunity(t *testing.T) {
	engine := tutorialTestEngine(t)
	if err := engine.AddActor(Participant{PlayerID: 4, RoleID: 3, TeamID: 1, BombCapacity: 2, BombPower: 2, SpeedRate: 2}, Cell{Row: 1, Col: 4}); err != nil {
		t.Fatal(err)
	}
	if err := engine.TeleportActor(1, Cell{Row: 0, Col: 2}); err != nil {
		t.Fatal(err)
	}
	if position := engine.Actors()[0].Position; position != PositionAtCellCenter(Cell{Row: 0, Col: 2}) {
		t.Fatalf("teleported to %+v", position)
	}
	event, err := engine.TrapActor(4, 240_000)
	if err != nil || event.Kind != EventActorTrapped {
		t.Fatalf("trap: %+v, %v", event, err)
	}
	if trapped := engine.Actors()[1]; trapped.State != ActorTrapped || trapped.TrapExpiresAt != engine.ElapsedMS()+500+240_000 {
		t.Fatalf("trapped actor %+v at %d", trapped, engine.ElapsedMS())
	}
	// A flame-immune player stands in its own blast.
	if err := engine.SetFlameImmune(1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Step([]Action{{PlayerID: 1, PlaceBomb: true}}); err != nil {
		t.Fatal(err)
	}
	for tick := 0; tick < 6; tick++ {
		if _, err := engine.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(engine.Bombs()) != 0 || engine.Actors()[0].State != ActorActive {
		t.Fatalf("bombs %+v, player %+v", engine.Bombs(), engine.Actors()[0])
	}
}
