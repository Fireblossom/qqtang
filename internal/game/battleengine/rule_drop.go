package battleengine

import "fmt"

// RuleBomb is one bubble of a rule's airborne batch (rule 2's 0x10E1).
// Power is the arm reach, one below the wire value. FromCol is the column it
// flies in from along its row.
type RuleBomb struct {
	Cell    Cell
	Power   byte
	FromCol int16
	// Style is the native bubble type; zero is an ordinary bubble. Rule 2's
	// 2x2 airdrop (0x006019BA) pairs a type-11 bubble with three type-12
	// companions that crawl in at 10 px/s, i.e. FlightMSPerCell 4000, so they
	// only go off through the chain.
	Style           byte
	FlightMSPerCell uint32
}

// DropRuleBatch applies one airborne batch the way Client.exe's 0x10E1
// receiver (0x006018A7) does. Each bubble belongs to ownerID, is placed at
// placedAtMS and takes the cell at once (0x005D388A) while it flies in from
// FromCol at 400 px/s (0x005D3C1D); its fuse runs from placedAtMS and it
// cannot go off before it lands. Each item falls from row 0 of its column at
// the same speed (0x005D9BBF) and can be picked up once it lands. The owner
// need not be alive, and the bubbles do not count against its capacity.
func (engine *Engine) DropRuleBatch(ownerID uint16, placedAtMS uint32, bombs []RuleBomb, items []Pickup) ([]Event, error) {
	if engine == nil {
		return nil, fmt.Errorf("battle engine is nil")
	}
	// A pirate Boss's falling bubbles (skill 6) are owned by the Boss.
	if engine.actorIndex(ownerID) < 0 && !engine.HasRuleNPCOwner(ownerID) {
		return nil, fmt.Errorf("rule batch owner %d is not a participant", ownerID)
	}
	for index, bomb := range bombs {
		if _, inside := engine.grid.Cell(bomb.Cell); !inside {
			return nil, fmt.Errorf("rule bubble %d at %d,%d is outside the map", index, bomb.Cell.Row, bomb.Cell.Col)
		}
		if bomb.Power == 0 {
			return nil, fmt.Errorf("rule bubble %d has no reach", index)
		}
	}
	for index, item := range items {
		if !engine.rules.pickupSupported(item.SceneID) && !nativeFieldSceneID(item.SceneID) {
			return nil, fmt.Errorf("rule item %d uses unsupported scene ID %d", index, item.SceneID)
		}
		if _, inside := engine.grid.Cell(item.Cell); !inside {
			return nil, fmt.Errorf("rule item %d at %d,%d is outside the map", index, item.Cell.Row, item.Cell.Col)
		}
	}
	events := make([]Event, 0, len(bombs)+len(items))
	for _, drop := range bombs {
		cols := drop.Cell.Col - drop.FromCol
		perCell := drop.FlightMSPerCell
		if perCell == 0 {
			perCell = NativeBombFlightMSPerCell
		}
		bomb := Bomb{
			ID: engine.nextBombID, OwnerID: ownerID, Cell: drop.Cell, Power: drop.Power, RuleOwned: true, NativeStyle: drop.Style,
			ExplodeAtMS:   saturatingAdd(saturatingAdd(placedAtMS, engine.rules.BombFuseMS), 1),
			FlightUntilMS: saturatingAdd(placedAtMS, perCell*uint32(max(cols, -cols))),
		}
		engine.nextBombID++
		engine.bombs = append(engine.bombs, bomb)
		events = append(events, Event{Kind: EventBombPlaced, TimeMS: engine.elapsedMS, PlayerID: ownerID, BombID: bomb.ID, Cell: bomb.Cell})
	}
	engine.syncNativeBombLinks()
	for _, item := range items {
		item.State = PickupAvailable
		engine.retirePendingPickupDispatchesAtCell(item.Cell)
		engine.retirePickupsAtCell(item.Cell)
		engine.retireFieldObjectsAtCell(item.Cell)
		engine.pendingPickupDispatches = append(engine.pendingPickupDispatches, pendingPickupDispatch{
			Pickup: item, ActivateAtMS: saturatingAdd(placedAtMS, NativeBombFlightMSPerCell*uint32(max(item.Cell.Row, 0))),
		})
		events = append(events, Event{Kind: EventPickupDispatched, TimeMS: engine.elapsedMS, Cell: item.Cell, Position: PositionAtCellCenter(item.Cell), SceneID: item.SceneID})
	}
	engine.activatePendingPickupDispatches()
	return events, nil
}
