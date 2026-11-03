package battleengine

import "fmt"

// Special map elements of the item-field maps (docs/special-elements-re.md),
// under Rules.NativeSpecialElements. The totem and halo timers and their
// 0x1176 messages belong to the caller; the engine keeps the scene parts
// every client computes alike.
const (
	ElementExplodeStone = 17019 // CMEExplodeStone: a hit breaks the 3x3 around it
	ElementSolidStone   = 17021 // CMESolidStone: LifeTime 3
	ElementTotemIce     = 17024 // CMETotemIce
	ElementHaloExcite   = 17025 // CMEHaloExcite
	// ExplodeStoneKillMS is the 500 ms state-2 timer of the broken stone,
	// during which 0x00619EE1 kills the local player inside its 3x3.
	ExplodeStoneKillMS = NativeMapDestructionMS
)

type stoneBlast struct {
	cell  Cell
	until uint32
}

// queueElementChain notes a stone whose retirement just began; the 3x3 hit
// (0x005D8E20 kind 2, synchronous in the original) runs in resolveElementChains.
func (engine *Engine) queueElementChain(cell Cell, tile Tile, at uint32) {
	if !engine.rules.NativeSpecialElements || tile.MapElementID != ElementExplodeStone {
		return
	}
	engine.elementChain = append(engine.elementChain, cell)
	engine.expireStoneBlasts()
	engine.stoneBlasts = append(engine.stoneBlasts, stoneBlast{cell: cell, until: saturatingAdd(at, ExplodeStoneKillMS)})
}

// resolveElementChains sends event 1 to the 3x3 of each broken stone: walls
// lose one life or begin to retire, and other stones chain in the same
// tick. Chained events carry no bomb: the arbitrator's 0x0FA4 lists only
// the flame's own hits, and every receiver chains them again locally.
func (engine *Engine) resolveElementChains(at uint32) []Event {
	var events []Event
	for len(engine.elementChain) > 0 {
		center := engine.elementChain[0]
		engine.elementChain = engine.elementChain[1:]
		for row := center.Row - 1; row <= center.Row+1; row++ {
			for col := center.Col - 1; col <= center.Col+1; col++ {
				cell := Cell{Row: row, Col: col}
				tile, inside := engine.grid.Cell(cell)
				if !inside || cell == center || tile.Durability == 0 {
					continue
				}
				if _, retiring := engine.pendingMapRetirements[cell]; retiring {
					continue // state 2 ignores further hits
				}
				events = append(events, engine.hitMapElement(cell, tile, at, 0, 0)...)
			}
		}
	}
	return events
}

// hitMapElement is one event-1 hit on a destructible map cell.
func (engine *Engine) hitMapElement(cell Cell, tile Tile, at uint32, owner uint16, bombID uint32) []Event {
	if tile.Durability > 1 && tile.MapElementID != ElementExplodeStone {
		engine.grid.Cells[engine.gridIndex(cell)].Durability--
		return []Event{{Kind: EventCellDamaged, TimeMS: at, PlayerID: owner, BombID: bombID, Cell: cell, ObjectID: tile.MapElementID}}
	}
	if event, started := engine.beginMapRetirement(cell, []Cell{cell}, at, owner, bombID); started {
		return []Event{event}
	}
	return nil
}

// ElementBlastCovers reports whether a broken explode stone (still in its
// 500 ms disappearing state) has cell inside its 3x3.
func (engine *Engine) ElementBlastCovers(cell Cell) bool {
	if engine == nil {
		return false
	}
	for _, blast := range engine.stoneBlasts {
		if engine.elapsedMS < blast.until && abs16(cell.Row-blast.cell.Row) <= 1 && abs16(cell.Col-blast.cell.Col) <= 1 {
			return true
		}
	}
	return false
}

func (engine *Engine) expireStoneBlasts() {
	kept := engine.stoneBlasts[:0]
	for _, blast := range engine.stoneBlasts {
		if engine.elapsedMS < blast.until {
			kept = append(kept, blast)
		}
	}
	engine.stoneBlasts = kept
}

// EliminateByMapElement is the arbitrator's 0xFA7 for an explode-stone kill
// (0x005D8E20 kind 10): the actor dies at once, trapped or not, and drops
// its items as an ordinary death does.
func (engine *Engine) EliminateByMapElement(playerID uint16) ([]Event, error) {
	if engine == nil || !engine.rules.NativeSpecialElements {
		return nil, fmt.Errorf("map-element kills require the special-element rule")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return nil, fmt.Errorf("map-element kill player %d is not a participant", playerID)
	}
	if engine.outcome.Ended || engine.actors[index].State == ActorEliminated {
		return nil, fmt.Errorf("map-element kill player %d cannot die now", playerID)
	}
	events := engine.eliminateActor(index, 0, EliminationUnknown)
	if event, ended := engine.evaluateTerminal(); ended {
		events = append(events, event)
	}
	return events, nil
}

// AdjustElementBonus adds totem and halo attribute bonuses (P+0x7C speed,
// P+0x90 bubbles, P+0xA4 power; 0x005AA44F and 0x006147E4). Values are read
// through the role caps, and a lowered speed never goes below rate 2
// (0x005AA06B).
func (engine *Engine) AdjustElementBonus(playerID uint16, speed, count, power int) error {
	if engine == nil || !engine.rules.NativeSpecialElements {
		return fmt.Errorf("element bonuses require the special-element rule")
	}
	index := engine.actorIndex(playerID)
	if index < 0 {
		return fmt.Errorf("element bonus player %d is not a participant", playerID)
	}
	bonus := &engine.actors[index].elementBonus
	bonus[0] += speed
	bonus[1] += count
	bonus[2] += power
	return nil
}

func elementBonused(base, cap byte, bonus, low int) byte {
	if bonus == 0 {
		return base
	}
	high := max(int(cap), int(base))
	return byte(min(max(int(base)+bonus, low), high))
}

func (engine *Engine) elementBombPower(actor *Actor, power byte) byte {
	if !engine.rules.NativeSpecialElements {
		return power
	}
	return elementBonused(power, actor.MaxBombPower, actor.elementBonus[2], 1)
}

func abs16(v int16) int16 {
	if v < 0 {
		return -v
	}
	return v
}

// Conveyors (CMETransStrip, docs/transdoor-strip-re.md §2): every strip
// moves the idle bubbles on its cell and, when asked, the local player at
// 0.02 px/ms (0x3CA3D70A). Positions are kept in thousandths of a pixel.
const conveyorMilliPixelsPerMS = 20

// SetConveyor marks cell as a strip moving toward direction.
func (engine *Engine) SetConveyor(cell Cell, direction Direction) error {
	if engine == nil || !engine.rules.NativeSpecialElements {
		return fmt.Errorf("conveyors require the special-element rule")
	}
	if _, inside := engine.grid.Cell(cell); !inside || direction == DirectionNone {
		return fmt.Errorf("conveyor %v toward %d is invalid", cell, direction)
	}
	if engine.conveyors == nil {
		engine.conveyors = map[Cell]Direction{}
	}
	engine.conveyors[cell] = direction
	return nil
}

// conveyorStep is 0x00614CFF for the four straight strips: toward the
// cell centre the move is free; past it only into an enterable cell (map
// elements only, 0x00614CA1).
func (engine *Engine) conveyorStep(x, y int64, at Cell, direction Direction, v int64) (int64, int64) {
	cellOf := func(p int64) int16 { return int16(p / (CellSizePixels * 1000)) }
	half := int64(CellSizePixels/2) * 1000
	enter := func(c Cell) bool {
		tile, inside := engine.grid.Cell(c)
		return inside && tile.PlayerPassable(direction)
	}
	switch direction {
	case DirectionRight:
		if cellOf(x+half) == at.Col || enter(Cell{Row: at.Row, Col: at.Col + 1}) {
			x += v
		}
	case DirectionUp:
		if cellOf(y-half) == at.Row || enter(Cell{Row: at.Row - 1, Col: at.Col}) {
			y -= v
		}
	case DirectionLeft:
		if cellOf(x-half) == at.Col || enter(Cell{Row: at.Row, Col: at.Col - 1}) {
			x -= v
		}
	case DirectionDown:
		if cellOf(y+half) == at.Row || enter(Cell{Row: at.Row + 1, Col: at.Col}) {
			y += v
		}
	}
	return x, y
}

// conveyBombs is 0x00614B3F: bubbles lying on a strip (not flying) move
// with it and change cells as they cross. No message: every client moves
// its own copy.
func (engine *Engine) conveyBombs() {
	if len(engine.conveyors) == 0 {
		return
	}
	v := int64(engine.rules.TickMS) * conveyorMilliPixelsPerMS
	moved := false
	for index := range engine.bombs {
		bomb := &engine.bombs[index]
		direction, on := engine.conveyors[bomb.Cell]
		if !on || bomb.FlightUntilMS > engine.elapsedMS {
			delete(engine.conveyedBombs, bomb.ID)
			continue
		}
		p, tracked := engine.conveyedBombs[bomb.ID]
		if !tracked {
			center := PositionAtCellCenter(bomb.Cell)
			p = [2]int64{int64(center.X) * 1000, int64(center.Y) * 1000}
		}
		p[0], p[1] = engine.conveyorStep(p[0], p[1], bomb.Cell, direction, v)
		if engine.conveyedBombs == nil {
			engine.conveyedBombs = map[uint32][2]int64{}
		}
		engine.conveyedBombs[bomb.ID] = p
		if cell := (Cell{Row: int16(p[1] / (CellSizePixels * 1000)), Col: int16(p[0] / (CellSizePixels * 1000))}); cell != bomb.Cell {
			if _, inside := engine.grid.Cell(cell); inside {
				bomb.Cell, moved = cell, true
			}
		}
	}
	if moved {
		engine.syncNativeBombLinks()
	}
}

// ConveyedBombPosition is a strip-carried bubble's pixel position.
func (engine *Engine) ConveyedBombPosition(bombID uint32) (Position, bool) {
	p, ok := engine.conveyedBombs[bombID]
	return Position{X: int32(p[0] / 1000), Y: int32(p[1] / 1000)}, ok
}

// Convey moves an actor standing on a strip by one tick (event 6 of
// 0x00614C49): the original pushes only the players its own client runs,
// whatever their state. It reports whether the actor moved.
func (engine *Engine) Convey(playerID uint16) bool {
	if engine == nil || len(engine.conveyors) == 0 {
		return false
	}
	index := engine.actorIndex(playerID)
	if index < 0 || engine.actors[index].State == ActorEliminated {
		return false
	}
	actor := &engine.actors[index]
	at := actor.Position.Cell()
	direction, on := engine.conveyors[at]
	if !on {
		actor.conveyRemainder = [2]int64{}
		return false
	}
	x := int64(actor.Position.X)*1000 + actor.conveyRemainder[0]
	y := int64(actor.Position.Y)*1000 + actor.conveyRemainder[1]
	x, y = engine.conveyorStep(x, y, at, direction, int64(engine.rules.TickMS)*conveyorMilliPixelsPerMS)
	next := Position{X: int32(x / 1000), Y: int32(y / 1000)}
	actor.conveyRemainder = [2]int64{x - int64(next.X)*1000, y - int64(next.Y)*1000}
	if next == actor.Position {
		return false
	}
	actor.nativePreviousPosition, actor.Position = actor.Position, next
	return true
}
