package battleengine

import "fmt"

// Rule 7's charged skills (招式, 0x115C/0x115B; docs/wrestle-skills-re.md).
// The bubble throws and the kick reuse the verified 0x1159 and 0x139C paths;
// only the dodge needs its own state change.

// NativeMachineSkillDodgeProtectionMS is the countdown 0x005FFAE3 puts on the
// player's harm-protection object (+0x318, 0x005B5202(300)).
const NativeMachineSkillDodgeProtectionMS = 300

// ApplyMachineSkillDodge is the dodge half of the kick-or-dodge skill
// (types 6/7, Client.exe 0x005FFA03), which every client runs for itself on
// 0x115B. checkRow/col is the cell behind the player taken from the
// message's packed position; it must hold no map element. The player then
// moves to col's centre at its current height if that cell can be entered
// (0x005B7999, which in rule 7 also refuses the centre column 7). Whether or
// not it moved, the player's harm protection is set to 300 ms from now; the
// native countdown replaces a longer one rather than extending it.
func (engine *Engine) ApplyMachineSkillDodge(playerID uint16, checkRow, col int16) (bool, error) {
	if engine == nil || !engine.rules.NativeMachineSkills {
		return false, fmt.Errorf("native machine skills are not enabled")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return false, fmt.Errorf("machine skill dodge player %d is not a participant", playerID)
	}
	actor := &engine.actors[index]
	if actor.State == ActorEliminated {
		return false, fmt.Errorf("machine skill dodge player %d is eliminated", playerID)
	}
	moved := false
	if col >= 0 && col <= 14 && engine.machineSkillCellClear(Cell{Row: checkRow, Col: col}) {
		target := Position{X: int32(col)*CellSizePixels + CellSizePixels/2, Y: actor.Position.Y}
		cell := target.Cell()
		if col != 7 && actor.fieldAllows(cell) && engine.machineSkillCellClear(cell) {
			if err := engine.ApplyVerifiedMovementCheckpoint(playerID, target); err != nil {
				return false, err
			}
			moved = true
		}
	}
	engine.actors[index].HarmProtectionExpiresAt = saturatingAdd(engine.elapsedMS, NativeMachineSkillDodgeProtectionMS)
	return moved, nil
}

func (engine *Engine) machineSkillCellClear(cell Cell) bool {
	tile, inside := engine.grid.Cell(cell)
	return inside && tile.Kind == CellOpen && !tile.MapElementOccupied && engine.bombAt(cell) < 0
}
