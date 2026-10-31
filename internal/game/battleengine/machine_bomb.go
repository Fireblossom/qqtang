package battleengine

import "fmt"

// PlaceMachineBomb applies 0x115D (Client.exe 0x006000E0). The bubble starts
// up to three rows above its destination. The fuse starts on creation, even
// though the wire identity retains the earlier machine-launch timestamp.
func (engine *Engine) PlaceMachineBomb(owner uint16, cell Cell, style, wirePower byte) (Event, error) {
	if engine == nil || engine.actorIndex(owner) < 0 {
		return Event{}, fmt.Errorf("machine bomb owner %d is not a participant", owner)
	}
	if _, ok := engine.grid.Cell(cell); !ok || wirePower < 2 || (style != 1 && style != 13 && style != 14) {
		return Event{}, fmt.Errorf("invalid machine bomb %d at %v with power %d", style, cell, wirePower)
	}
	speed := uint32(400)
	if style == 13 {
		speed = 40
	} else if style == 14 {
		speed = 300
	}
	bomb := Bomb{ID: engine.nextBombID, OwnerID: owner, Cell: cell, Power: wirePower - 1, NativeStyle: style,
		ExplodeAtMS:   saturatingAdd(engine.elapsedMS, 3001),
		FlightUntilMS: saturatingAdd(engine.elapsedMS, (uint32(min(cell.Row, 3))*40000+speed-1)/speed)}
	return engine.commitBomb(bomb), nil
}

// 0x00607F08 creates fire10 cells directly: type 13 covers a 5x5 square,
// type 14 the entire receiving half. Neither crosses the centre column 7.
func (engine *Engine) machineBlastCells(style byte, at Cell) []Cell {
	if style != 13 && style != 14 {
		return nil
	}
	lo, hi := int16(0), int16(6)
	if at.Col > 7 {
		lo, hi = 8, 14
	}
	if at.Col == 7 {
		return nil
	}
	r0, r1 := int16(0), int16(engine.grid.Height)-1
	if style == 13 {
		r0, r1 = max(r0, at.Row-2), min(r1, at.Row+2)
		lo, hi = max(lo, at.Col-2), min(hi, at.Col+2)
	}
	hi = min(hi, int16(engine.grid.Width)-1)
	var result []Cell
	for row := r0; row <= r1; row++ {
		for col := lo; col <= hi; col++ {
			result = append(result, Cell{Row: row, Col: col})
		}
	}
	return result
}

func (engine *Engine) addMachineFlame(cell Cell, owner uint16, bomb uint32, style byte) {
	engine.addBombFlame(cell, owner, bomb)
	for i := range engine.flames {
		flame := &engine.flames[i]
		if flame.Cell != cell {
			continue
		}
		if style == 13 || style == 14 {
			duration := uint32(900)
			if style == 14 {
				duration = 1200
			}
			flame.NativeStyle = style
			flame.ExpiresAtMS = max(flame.ExpiresAtMS, saturatingAdd(engine.elapsedMS, duration))
		}
		return
	}
}

// SetMachineGate updates the two cells of native element 13016. Its source
// GridAttr is 0x1A1A; 005EF47A clears horizontal passage while cooling, and
// 005EF7DE restores it only on the owning side's entrance cell when ready.
func (engine *Engine) SetMachineGate(anchor Cell, first, ready bool) {
	for dc := int16(0); dc < 2; dc++ {
		cell := Cell{Row: anchor.Row, Col: anchor.Col + dc}
		if _, ok := engine.grid.Cell(cell); !ok {
			continue
		}
		open := ready && ((first && dc == 0) || (!first && dc == 1))
		attr := uint32(0x1010)
		kind := CellSolid
		if open {
			attr, kind = 0x1a1a, CellOpen
		}
		engine.grid.Cells[int(cell.Row)*int(engine.grid.Width)+int(cell.Col)] = Tile{
			Kind: kind, MapElementOccupied: true, MapElementID: 13016, ElementAnchor: anchor, ElementWidth: 2, ElementHeight: 1,
			NativeGridAttrSet: true, NativeGridAttr: attr, NativePlayerExitAttrSet: true, NativePlayerExitAttr: attr,
		}
	}
}
