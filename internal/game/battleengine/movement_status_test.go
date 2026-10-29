package battleengine

import "testing"

func TestClearHarmfulMovementStatusKeepsSpeedBoost(t *testing.T) {
	grid := Grid{Width: 3, Height: 1, Cells: make([]Tile, 3)}
	engine, err := New(Config{
		Seed: 1, Grid: grid,
		Rules: Rules{TickMS: 20, RoundDurationMS: 240_000, BombFuseMS: 3000, FlameDurationMS: 500, ActorHalfSizePixels: 19},
		Participants: []Participant{
			{PlayerID: 1, TeamID: 1, Source: ParticipantHuman, Spawn: Cell{Row: 0, Col: 0}, SpeedPixelsPerSecond: 80, BombCapacity: 1, BombPower: 1},
			{PlayerID: 2, TeamID: 2, Source: ParticipantHuman, Spawn: Cell{Row: 0, Col: 2}, SpeedPixelsPerSecond: 80, BombCapacity: 1, BombPower: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := &engine.actors[engine.actorIndex(1)]
	for _, status := range []MovementStatusKind{MovementStatusSlow, MovementStatusForcedSlide} {
		engine.installMovementStatus(actor, status)
		if _, cleared := engine.ClearHarmfulMovementStatus(1); !cleared || actor.MovementStatus != MovementStatusNone {
			t.Fatalf("status %d: cleared %v, now %d", status, cleared, actor.MovementStatus)
		}
	}
	engine.installMovementStatus(actor, MovementStatusFast)
	if _, cleared := engine.ClearHarmfulMovementStatus(1); cleared || actor.MovementStatus != MovementStatusFast {
		t.Fatalf("speed boost: cleared %v, now %d", cleared, actor.MovementStatus)
	}
	if _, cleared := engine.ClearHarmfulMovementStatus(9); cleared {
		t.Fatal("unknown player cleared")
	}
}
