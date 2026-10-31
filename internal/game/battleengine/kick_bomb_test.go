package battleengine

import "testing"

// kickBombTestConfig is rule 2 on an open 15x13 field.
func kickBombTestConfig() Config {
	config := testConfig()
	config.Grid = testOpenGrid(15, 13)
	config.Rules.DisableBombPlacement = true
	config.Rules.DisableMapElementPush = true
	config.Rules.UniversalKick = true
	config.Rules.WrappingKick = true
	config.Rules.CornerSlideTolerancePixels = 2
	config.Participants[0].Spawn = Cell{Row: 6, Col: 11}
	config.Participants[1].Spawn = Cell{Row: 1, Col: 1}
	return config
}

func TestKickBombRulesValidation(t *testing.T) {
	config := kickBombTestConfig()
	config.Rules.CornerSlideTolerancePixels = CellSizePixels / 2
	if _, err := New(config); err == nil {
		t.Fatal("a 20-pixel corner tolerance was accepted")
	}
	mustEngine(t, kickBombTestConfig())
}

func TestOrdinaryActorCannotKickByDefault(t *testing.T) {
	config := kickBombTestConfig()
	config.Rules.UniversalKick = false
	engine := mustEngine(t, config)
	engine.actors[0].Position = Position{X: 11*40 + 29, Y: 6*40 + 20}
	engine.bombs = []Bomb{{ID: 1, OwnerID: 2, Cell: Cell{Row: 6, Col: 12}, Power: 1, ExplodeAtMS: 5_000}}
	engine.nextBombID = 2
	if _, err := engine.Step([]Action{{PlayerID: 1, Move: DirectionRight}}); err != nil {
		t.Fatal(err)
	}
	if engine.bombs[0].Cell != (Cell{Row: 6, Col: 12}) {
		t.Fatalf("bubble moved without UniversalKick: %+v", engine.bombs[0])
	}
}

func TestWrappingKickGoesEightCellsAroundTheEdge(t *testing.T) {
	for name, test := range map[string]struct {
		solid    []Cell
		target   Cell
		distance uint32
	}{
		// Tutorial3.qbv: every one of its 216 kick messages lands here.
		"eight ahead modulo the width": {target: Cell{Row: 6, Col: 5}, distance: 8},
		"back to the first open cell":  {solid: []Cell{{Row: 6, Col: 5}, {Row: 6, Col: 4}}, target: Cell{Row: 6, Col: 3}, distance: 6},
	} {
		t.Run(name, func(t *testing.T) {
			config := kickBombTestConfig()
			for _, cell := range test.solid {
				config.Grid.Cells[int(cell.Row)*int(config.Grid.Width)+int(cell.Col)] = Tile{Kind: CellSolid, MapElementOccupied: true}
			}
			engine := mustEngine(t, config)
			engine.actors[0].Position = Position{X: 11*40 + 29, Y: 6*40 + 20}
			engine.bombs = []Bomb{{ID: 1, OwnerID: 2, Cell: Cell{Row: 6, Col: 12}, Power: 1, ExplodeAtMS: 5_000}}
			engine.nextBombID = 2
			events, err := engine.Step([]Action{{PlayerID: 1, Move: DirectionRight}})
			if err != nil {
				t.Fatal(err)
			}
			var kicked Event
			for _, event := range events {
				if event.Kind == EventBombKicked {
					kicked = event
				}
			}
			if kicked.Kind == 0 || engine.bombs[0].Cell != test.target {
				t.Fatalf("kick = %+v, bubble = %+v", kicked, engine.bombs[0])
			}
			if want := kicked.TimeMS + test.distance*NativeBombFlightMSPerCell; engine.bombs[0].FlightUntilMS != want || engine.bombs[0].ExplodeAtMS != 5_000 {
				t.Fatalf("bubble = %+v, want flight until %d", engine.bombs[0], want)
			}
		})
	}
}

func TestWrappingKickWaitsForTheBubbleToLand(t *testing.T) {
	engine := mustEngine(t, kickBombTestConfig())
	engine.actors[0].Position = Position{X: 11*40 + 29, Y: 6*40 + 20}
	engine.bombs = []Bomb{{ID: 1, OwnerID: 2, Cell: Cell{Row: 6, Col: 12}, Power: 1, ExplodeAtMS: 5_000, FlightUntilMS: 1_000}}
	engine.nextBombID = 2
	if _, err := engine.Step([]Action{{PlayerID: 1, Move: DirectionRight}}); err != nil {
		t.Fatal(err)
	}
	if engine.bombs[0].Cell != (Cell{Row: 6, Col: 12}) {
		t.Fatalf("a flying bubble was kicked: %+v", engine.bombs[0])
	}
}

func TestVerifiedWrappingKickFliesByDistance(t *testing.T) {
	engine := mustEngine(t, kickBombTestConfig())
	engine.bombs = []Bomb{{ID: 1, OwnerID: 2, Cell: Cell{Row: 10, Col: 10}, Power: 4, ExplodeAtMS: 5_000}}
	engine.nextBombID = 2
	// Tutorial3.qbv's first kick: down from 10,10 to 5,10.
	if _, err := engine.ApplyVerifiedBombMovement(1, 1, Cell{Row: 10, Col: 10}, Cell{Row: 5, Col: 10}); err != nil {
		t.Fatal(err)
	}
	if want := engine.elapsedMS + 8*NativeBombFlightMSPerCell; engine.bombs[0].FlightUntilMS != want {
		t.Fatalf("flight until %d, want %d", engine.bombs[0].FlightUntilMS, want)
	}
}

func TestDisableBombPlacement(t *testing.T) {
	engine := mustEngine(t, kickBombTestConfig())
	if _, err := engine.Step([]Action{{PlayerID: 1, PlaceBomb: true}}); err != nil {
		t.Fatal(err)
	}
	if len(engine.bombs) != 0 {
		t.Fatalf("placed %+v", engine.bombs)
	}
	if _, err := engine.ApplyVerifiedBombPlacementAt(2, Cell{Row: 1, Col: 1}, 2, 0, engine.elapsedMS); err != nil {
		t.Fatalf("verified placement refused: %v", err)
	}
}

func TestDisableMapElementPush(t *testing.T) {
	config := kickBombTestConfig()
	config.Rules.TickMS = NativeMapElementPushPulseMS
	config.Grid.Cells[6*15+12] = Tile{
		Kind: CellSolid, MapElementID: 9012, NormalPushable: true,
		ElementWidth: 1, ElementHeight: 1, ElementAnchor: Cell{Row: 6, Col: 12},
	}
	engine := mustEngine(t, config)
	engine.actors[0].Position = PositionAtCellCenter(Cell{Row: 6, Col: 11})
	for contact := 0; contact < 7; contact++ {
		events, err := engine.Step([]Action{{PlayerID: 1, Move: DirectionRight}})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind == EventMapElementMoved || event.Kind == EventMapElementMoveRequested {
				t.Fatalf("push with DisableMapElementPush: %+v", event)
			}
		}
	}
}

func TestCornerSlideToleranceOverride(t *testing.T) {
	for name, test := range map[string]struct{ y, want int32 }{
		"two corrects":       {y: 42, want: 34},
		"three does not":     {y: 43, want: 43},
		"six no longer does": {y: 46, want: 46},
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			config.Grid = testOpenGrid(5, 5)
			config.Grid.Cells[1*5+2] = Tile{Kind: CellSolid}
			config.Rules.ActorHalfSizePixels = NativeActorHalfSizePixels
			config.Rules.CornerSlideTolerancePixels = 2
			config.Participants[0].SpeedPixelsPerSecond = 80
			config.Participants[1].Spawn = Cell{Row: 4, Col: 4}
			engine := mustEngine(t, config)
			engine.actors[0].Position = Position{X: 60, Y: test.y}
			if _, err := engine.Step([]Action{{PlayerID: 1, Move: DirectionRight}}); err != nil {
				t.Fatal(err)
			}
			if got := engine.actors[0].Position; got != (Position{X: 60, Y: test.want}) {
				t.Fatalf("position=%+v, want X=60 Y=%d", got, test.want)
			}
		})
	}
}

func TestDropRuleBatch(t *testing.T) {
	config := kickBombTestConfig()
	config.Rules.BombFuseMS = 3_000
	config.Rules.ExternalConclusion = true // keep the round going with the owner dead
	engine := mustEngine(t, config)
	engine.actors[1].State = ActorEliminated
	bombs := []RuleBomb{{Cell: Cell{Row: 5, Col: 11}, Power: 1, FromCol: 14}, {Cell: Cell{Row: 10, Col: 10}, Power: 4, FromCol: 0}}
	items := []Pickup{{SceneID: SceneSpeedSmall, Cell: Cell{Row: 4, Col: 12}}}
	events, err := engine.DropRuleBatch(2, 0, bombs, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || len(engine.bombs) != 2 {
		t.Fatalf("events %+v, bubbles %+v", events, engine.bombs)
	}
	for index, want := range []Bomb{
		{OwnerID: 2, Cell: Cell{Row: 5, Col: 11}, Power: 1, ExplodeAtMS: 3_001, FlightUntilMS: 300},
		{OwnerID: 2, Cell: Cell{Row: 10, Col: 10}, Power: 4, ExplodeAtMS: 3_001, FlightUntilMS: 1_000},
	} {
		got := engine.bombs[index]
		got.ID = 0
		if got != want {
			t.Fatalf("bubble %d = %+v, want %+v", index, got, want)
		}
	}
	if len(engine.Pickups()) != 0 {
		t.Fatalf("item visible before landing: %+v", engine.Pickups())
	}
	for step := 0; step < 4; step++ {
		if _, err := engine.Step(nil); err != nil {
			t.Fatal(err)
		}
	}
	if engine.elapsedMS < 400 {
		t.Fatalf("engine stopped at %d", engine.elapsedMS)
	}
	if pickups := engine.Pickups(); len(pickups) != 1 || pickups[0].Cell != items[0].Cell || pickups[0].State != PickupAvailable {
		t.Fatalf("landed items = %+v", pickups)
	}
	if _, err := engine.DropRuleBatch(9, 0, bombs, nil); err == nil {
		t.Fatal("a batch from a stranger was accepted")
	}
	if _, err := engine.DropRuleBatch(2, 0, nil, []Pickup{{SceneID: 150, Cell: Cell{Row: 1, Col: 1}}}); err == nil {
		t.Fatal("an unsupported item was accepted")
	}
}
