package battleengine

import "testing"

func specialElementsEngine(t *testing.T) *Engine {
	t.Helper()
	cfg := testConfig()
	cfg.Grid = testOpenGrid(7, 5)
	cfg.Rules.NativeSpecialElements = true
	cfg.Participants[0].Spawn = Cell{Row: 3, Col: 1}
	cfg.Participants[1].Spawn = Cell{Row: 0, Col: 6}
	set := func(cell Cell, id uint32, life byte) {
		cfg.Grid.Cells[int(cell.Row)*7+int(cell.Col)] = Tile{Kind: CellBreakable, MapElementOccupied: true, MapElementID: id, Durability: life,
			ElementWidth: 1, ElementHeight: 1, ElementAnchor: cell}
	}
	set(Cell{Row: 2, Col: 2}, ElementExplodeStone, 1)
	set(Cell{Row: 2, Col: 3}, ElementExplodeStone, 1)
	set(Cell{Row: 2, Col: 4}, ElementSolidStone, 3)
	set(Cell{Row: 1, Col: 1}, 1001, 1)
	return mustEngine(t, cfg)
}

// verifiedHit is one 0x0FA4 map entry as ApplyVerifiedBombExplosionAt applies it.
func verifiedHit(t *testing.T, e *Engine, id uint32, cell Cell) []Event {
	t.Helper()
	event, _, changed, err := e.applyVerifiedMapElementHit(VerifiedMapElementHit{MapElementID: id, Cell: cell})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	if changed {
		events = append(events, event)
	}
	return append(events, e.resolveElementChains(e.elapsedMS)...)
}

func TestExplodeStoneChainsThroughItsThreeByThree(t *testing.T) {
	e := specialElementsEngine(t)
	events := verifiedHit(t, e, ElementExplodeStone, Cell{Row: 2, Col: 2})
	destroyed, damaged := map[Cell]bool{}, map[Cell]bool{}
	for _, event := range events {
		switch event.Kind {
		case EventCellDestroyed:
			destroyed[event.Cell] = true
		case EventCellDamaged:
			damaged[event.Cell] = true
		}
	}
	if !destroyed[Cell{Row: 2, Col: 2}] || !destroyed[Cell{Row: 2, Col: 3}] || !destroyed[Cell{Row: 1, Col: 1}] || !damaged[Cell{Row: 2, Col: 4}] {
		t.Fatalf("destroyed %v damaged %v", destroyed, damaged)
	}
	if tile, _ := e.grid.Cell(Cell{Row: 2, Col: 4}); tile.Durability != 2 {
		t.Fatalf("solid stone life %d", tile.Durability)
	}
	if !e.ElementBlastCovers(Cell{Row: 3, Col: 1}) || e.ElementBlastCovers(Cell{Row: 0, Col: 6}) {
		t.Fatal("blast area")
	}
	if _, err := e.EliminateByMapElement(1); err != nil || e.actors[0].State != ActorEliminated {
		t.Fatalf("kill: %v state %v", err, e.actors[0].State)
	}
}

func TestSolidStoneLosesOneLifePerVerifiedHit(t *testing.T) {
	e := specialElementsEngine(t)
	solid := Cell{Row: 2, Col: 4}
	for want := byte(2); want >= 1; want-- {
		verifiedHit(t, e, ElementSolidStone, solid)
		if tile, _ := e.grid.Cell(Cell{Row: 2, Col: 4}); tile.Durability != want {
			t.Fatalf("life %d, want %d", tile.Durability, want)
		}
	}
	events := verifiedHit(t, e, ElementSolidStone, solid)
	if len(events) == 0 || events[0].Kind != EventCellDestroyed {
		t.Fatalf("third hit %+v", events)
	}
}

func TestElementBonusesStayInsideTheRoleCaps(t *testing.T) {
	cfg := testConfig()
	if mustEngine(t, cfg).AdjustElementBonus(1, 8, 8, 8) == nil {
		t.Fatal("bonus outside the rule")
	}
	cfg.Rules.NativeSpecialElements = true
	cfg.Rules.SpeedPixelsPerSecondByRate = nativeSpeedPixelsPerSecondByRate
	for i := range cfg.Participants {
		p := &cfg.Participants[i]
		p.SpeedRate, p.MaxSpeedRate, p.SpeedPixelsPerSecond = 4, 9, nativeSpeedPixelsPerSecondByRate[4]
		p.MaxBombCapacity, p.MaxBombPower = 6, 7
	}
	e := mustEngine(t, cfg)
	a := &e.actors[0]
	_ = e.AdjustElementBonus(1, 8, 8, 8)
	c := e.actorCapabilities(a, DirectionUp)
	if c.EffectiveSpeedRate != 9 || c.EffectiveBombCapacity != 6 || e.elementBombPower(a, a.BombPower) != 7 {
		t.Fatalf("halo: speed %d count %d power %d", c.EffectiveSpeedRate, c.EffectiveBombCapacity, e.elementBombPower(a, a.BombPower))
	}
	_ = e.AdjustElementBonus(1, -8-3, -8, -8)
	if c := e.actorCapabilities(a, DirectionUp); c.EffectiveSpeedRate != 2 || c.EffectiveBombCapacity != 2 {
		t.Fatalf("totem: speed %d count %d", c.EffectiveSpeedRate, c.EffectiveBombCapacity)
	}
}
