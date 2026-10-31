package battleengine

import "testing"

func machineTestEngine(t *testing.T) *Engine {
	config := testConfig()
	config.Grid = testOpenGrid(15, 13)
	config.Participants[0].Spawn = Cell{Row: 6, Col: 2}
	config.Participants[1].Spawn = Cell{Row: 6, Col: 12}
	config.Rules.NativeDurabilityLayers = 4
	config.Rules.ExternalConclusion = true
	return mustEngine(t, config)
}

func TestNativeDurabilityKeepsPositionAndProtectsBetweenHits(t *testing.T) {
	engine := machineTestEngine(t)
	where := engine.actors[0].Position
	for hit := 0; hit < 4; hit++ {
		at := uint32(1000 + hit*3000)
		engine.elapsedMS = at
		events, err := engine.ApplyNativeDurabilityHit(1, 2, where, at)
		if err != nil || len(events) == 0 || events[0].Kind != EventActorHarmed || engine.actors[0].DurabilityLeft != byte(3-hit) {
			t.Fatalf("hit %d: %+v %v actor=%+v", hit, events, err, engine.actors[0])
		}
		if hit < 3 {
			if actor := engine.actors[0]; actor.State != ActorActive || actor.Position != where || actor.HarmProtectionExpiresAt != at+3000 {
				t.Fatalf("surviving actor did not retain its position/protection: %+v", actor)
			}
			if duplicate, err := engine.ApplyNativeDurabilityHit(1, 2, where, at+2999); err != nil || len(duplicate) != 0 {
				t.Fatalf("protection allowed another hit: %+v %v", duplicate, err)
			}
		} else if len(events) != 2 || events[1].Kind != EventActorEliminated || engine.actors[0].State != ActorEliminated {
			t.Fatalf("fourth layer did not eliminate: %+v", events)
		}
	}
}

func TestMachineAreaBombsDoNotCrossMiddleAndPersist(t *testing.T) {
	for _, style := range []byte{13, 14} {
		engine := machineTestEngine(t)
		engine.elapsedMS = 10_000
		if _, err := engine.PlaceMachineBomb(2, Cell{Row: 6, Col: 5}, style, 3); err != nil {
			t.Fatal(err)
		}
		bomb := engine.bombs[0]
		wantLanding := uint32(13000)
		if style == 14 {
			wantLanding = 10400
		}
		if bomb.ExplodeAtMS != 13_001 || bomb.FlightUntilMS != wantLanding {
			t.Fatalf("machine fuse/flight starts on creation: %+v", bomb)
		}
		engine.elapsedMS = 13_020
		engine.explodeDueBombs()
		want, duration := 20, uint32(900) // 5 rows, columns 3..6
		if style == 14 {
			want, duration = 91, 1200
		}
		if len(engine.flames) != want {
			t.Fatalf("style %d has %d cells, want %d", style, len(engine.flames), want)
		}
		for _, flame := range engine.flames {
			if flame.Cell.Col >= 7 || flame.ExpiresAtMS != 13_020+duration {
				t.Fatalf("area escaped its half or wrong lifetime: %+v", flame)
			}
		}
		// The native fire10 still hurts a player entering it after impact.
		engine.elapsedMS += 100
		engine.actors[0].Position = PositionAtCellCenter(Cell{Row: 6, Col: 4})
		if events := engine.applyFlameHazards(); len(events) != 1 || events[0].Kind != EventActorHarmed {
			t.Fatalf("late entry escaped machine fire: %+v", events)
		}
		// The peer reconstructs area fire from the style, not cross bounds.
		peer := machineTestEngine(t)
		peer.elapsedMS = 13_020
		_, err := peer.ApplyVerifiedBombExplosion([]VerifiedExplodedBomb{{OwnerID: 2, Cell: bomb.Cell, NativeStyle: style}}, nil, nil)
		if err != nil || len(peer.flames) != want {
			t.Fatalf("peer area: %d, %v", len(peer.flames), err)
		}
	}
}
