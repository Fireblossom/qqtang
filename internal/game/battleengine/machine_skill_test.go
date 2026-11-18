package battleengine

import "testing"

func machineSkillTestEngine(t *testing.T, enabled bool) *Engine {
	t.Helper()
	config := testConfig()
	config.Grid = testOpenGrid(15, 3)
	config.Participants[0].Spawn = Cell{Row: 1, Col: 3}
	config.Participants[1].Spawn = Cell{Row: 1, Col: 11}
	config.Rules.NativeMachineSkills = enabled
	return mustEngine(t, config)
}

func TestMachineSkillDodgeIsOptIn(t *testing.T) {
	engine := machineSkillTestEngine(t, false)
	if _, err := engine.ApplyMachineSkillDodge(1, 1, 2); err == nil {
		t.Fatal("ordinary engine accepted a rule-7 skill dodge")
	}
	if engine.Actors()[0].Position.Cell() != (Cell{Row: 1, Col: 3}) || engine.Actors()[0].HarmProtectionExpiresAt != 0 {
		t.Fatal("refused dodge changed the actor")
	}
}

func TestMachineSkillDodgeMovesBackAndProtects300ms(t *testing.T) {
	engine := machineSkillTestEngine(t, true)
	before := engine.Actors()[0].Position
	moved, err := engine.ApplyMachineSkillDodge(1, 1, 2)
	if err != nil || !moved {
		t.Fatalf("dodge = %t, %v", moved, err)
	}
	actor := engine.Actors()[0]
	if actor.Position != (Position{X: 2*CellSizePixels + CellSizePixels/2, Y: before.Y}) {
		t.Fatalf("dodge position = %+v", actor.Position)
	}
	if actor.HarmProtectionExpiresAt != engine.ElapsedMS()+NativeMachineSkillDodgeProtectionMS {
		t.Fatalf("protection until %d", actor.HarmProtectionExpiresAt)
	}
}

func TestMachineSkillDodgeBlockedStillProtectsAndReplacesLongerProtection(t *testing.T) {
	engine := machineSkillTestEngine(t, true)
	engine.actors[0].HarmProtectionExpiresAt = engine.ElapsedMS() + 3000
	engine.grid.Cells[1*15+2].MapElementOccupied = true
	moved, err := engine.ApplyMachineSkillDodge(1, 1, 2)
	if err != nil || moved {
		t.Fatalf("blocked dodge = %t, %v", moved, err)
	}
	if engine.Actors()[0].Position.Cell() != (Cell{Row: 1, Col: 3}) {
		t.Fatal("blocked dodge moved the actor")
	}
	if engine.Actors()[0].HarmProtectionExpiresAt != engine.ElapsedMS()+NativeMachineSkillDodgeProtectionMS {
		t.Fatal("dodge did not reset harm protection to 300 ms")
	}
	// Off the field, and the centre column of rule 7, are refused too.
	if moved, _ := engine.ApplyMachineSkillDodge(1, 1, -1); moved {
		t.Fatal("dodge left the field")
	}
	engine.actors[1].Position = PositionAtCellCenter(Cell{Row: 1, Col: 8})
	if moved, _ := engine.ApplyMachineSkillDodge(2, 1, 7); moved {
		t.Fatal("dodge entered the centre column")
	}
}

func TestMachineSkillDodgeRefusesBubbleCell(t *testing.T) {
	engine := machineSkillTestEngine(t, true)
	engine.bombs = append(engine.bombs, Bomb{ID: 99, OwnerID: 2, Cell: Cell{Row: 1, Col: 2}, Power: 1, ExplodeAtMS: 5000})
	if moved, err := engine.ApplyMachineSkillDodge(1, 1, 2); err != nil || moved {
		t.Fatalf("dodge onto a bubble = %t, %v", moved, err)
	}
}
