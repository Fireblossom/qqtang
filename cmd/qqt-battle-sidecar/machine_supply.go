package main

import (
	"slices"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

// 00890940..0089096B: integer division, using both candidate counts and
// the complete player vector length (not health, teams or alive players).
func (rule *machineRule) supplyInterval(players int) uint32 {
	cells := len(rule.supplyCells[0]) + len(rule.supplyCells[1])
	if players <= 0 || cells == 0 {
		return 0
	}
	return uint32(240000 / (14*cells + 3*players))
}

func machineSupplyScene(draw int) uint32 {
	switch {
	case draw < 30:
		return 0
	case draw < 60:
		return 1
	case draw < 85:
		return 2
	default:
		return 3
	}
}

// The native controller skips a whole half when ANY of its supply floor
// cells has a ground item. Actors, bombs, flames and other layers are not
// queried here. A zero weighted choice returns from the entire frame.
func (match *onlineMatch) machineSupplyTick() []battleengine.Event {
	rule := match.machine
	if rule == nil || rule.finishing || !match.isArbitrator() {
		return nil
	}
	interval := rule.supplyInterval(len(match.uins))
	if interval == 0 {
		return nil
	}
	for side := range rule.destroyed {
		if rule.destroyed[side] {
			rule.supplyCells[side] = nil
		}
	}
	now := match.engine.ElapsedMS()
	var events []battleengine.Event
	for side, cells := range rule.supplyCells {
		if len(cells) == 0 || now < rule.supplyLast[side] || now-rule.supplyLast[side] < interval {
			continue
		}
		occupied := false
		for _, pickup := range match.engine.Pickups() {
			if pickup.State == battleengine.PickupAvailable && slices.Contains(cells, pickup.Cell) {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		cell := cells[rule.rng.Intn(len(cells))]
		scene := machineSupplyScene(rule.rng.Intn(100))
		if scene == 0 {
			return events
		}
		item := game.ProduceItem{RowAndCol: byte(cell.Row)<<4 | byte(cell.Col), ItemID: scene}
		body, err := item.MarshalNetworkBinary()
		if err != nil {
			continue
		}
		applied := match.applyMachineSupply(item, now)
		if len(applied) == 0 {
			continue
		}
		events = append(events, applied...)
		match.queue(game.NotifyProduceItem, now, now, body)
		match.mirror(game.NotifyProduceItem, body)
	}
	return events
}

func (match *onlineMatch) applyMachineSupply(item game.ProduceItem, at uint32) []battleengine.Event {
	rule := match.machine
	if rule == nil || rule.finishing || item.ItemID < 1 || item.ItemID > 3 {
		return nil
	}
	cell := battleengine.Cell{Row: int16(item.RowAndCol >> 4), Col: int16(item.RowAndCol & 15)}
	side := 0
	if cell.Col > 8 {
		side = 1
	}
	// One production per half per frame. Keeping this clock on followers
	// rejects old/repeated datagrams and preserves the cooldown on takeover.
	if rule.destroyed[side] || at <= rule.supplyLast[side] || !slices.Contains(rule.supplyCells[side], cell) {
		return nil
	}
	event, err := match.engine.RevealNativeMachinePickup(item.ItemID, cell)
	if err != nil {
		return nil
	}
	rule.supplyLast[side] = at
	match.logf("machine supply %d at %d,%d time %d", item.ItemID, cell.Row, cell.Col, at)
	delete(match.collected, pickupKey{scene: item.ItemID, cell: cell})
	delete(match.pickupRequests, pickupKey{scene: item.ItemID, cell: cell})
	return []battleengine.Event{event}
}
