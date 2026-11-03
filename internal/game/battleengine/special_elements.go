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
