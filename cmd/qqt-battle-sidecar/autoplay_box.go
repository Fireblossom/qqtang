package main

import "qqtang/internal/game/battleengine"

func (bot *autoplayBot) actBox(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, action battleengine.Action, msPerCell uint32) battleengine.Action {
	now, here := engine.ElapsedMS(), actor.Position.Cell()
	unsafe := func(c battleengine.Cell) bool {
		if _, yes := bot.timeline.ImpactAt(c); yes {
			return true
		}
		pos := battleengine.PositionAtCellCenter(c)
		for _, b := range bot.box.objects {
			if !b.Moving || b.DestroyAt != 0 {
				continue
			}
			if abs32(pos.X-b.Position.X) < 65 && abs32(pos.Y-b.Position.Y) < 65 {
				return true
			}
		}
		return false
	}
	safe := func(c battleengine.Cell) bool { return !unsafe(c) }
	if unsafe(here) {
		if path := world.nearest(here, func(battleengine.Cell) bool { return true }, safe); len(path) > 1 {
			return bot.follow(action, actor, path)
		}
		return action
	}
	// Use the two rule-granted bubbles when they can safely open the field.
	held := false
	for _, slot := range actor.HeldActions {
		held = held || slot.ActionID == 49 && slot.Count > 0
	}
	if held && world.worthBombing(here, actor) && onCentre(actor.Position) {
		copy := engine.Clone()
		if _, err := copy.ApplyVerifiedBombPlacementAt(actor.PlayerID, here, 4, 0, now); err == nil {
			if danger, err := copy.DangerTimeline(autoplayHorizonMS); err == nil {
				if path := world.escape(here, danger, now, msPerCell); len(path) > 1 {
					action.UseActionID = 49
					return action
				}
			}
		}
	}
	type setup struct {
		at        battleengine.Cell
		direction byte
		attack    bool
	}
	var setups []setup
	for _, b := range bot.box.ordered() {
		if !b.breakable(now) || b.Moving || b.Hidden {
			continue
		}
		for dir := byte(0); dir < 4; dir++ {
			before := boxStep(b.Cell, (dir+2)&3)
			after := boxStep(b.Cell, dir)
			tile, inside := world.grid.Cell(after)
			if !inside || tile.MapElementOccupied && !tile.PlayerPassable(nativeDirection(dir)) || !world.open(before) {
				continue
			}
			dest := boxDestination(b.Cell, dir, actor.BombPower+4)
			attack := false
			for _, other := range engine.Actors() {
				if other.TeamID == actor.TeamID || other.State != battleengine.ActorActive {
					continue
				}
				cell := other.Position.Cell()
				if dir%2 == 0 {
					attack = attack || cell.Row == b.Cell.Row && cell.Col >= min(b.Cell.Col, dest.Col) && cell.Col <= max(b.Cell.Col, dest.Col)
				} else {
					attack = attack || cell.Col == b.Cell.Col && cell.Row >= min(b.Cell.Row, dest.Row) && cell.Row <= max(b.Cell.Row, dest.Row)
				}
			}
			setups = append(setups, setup{before, dir, attack})
		}
	}
	for _, offensive := range []bool{true, false} {
		var chosen *setup
		path := world.nearest(here, safe, func(c battleengine.Cell) bool {
			for i := range setups {
				if setups[i].at == c && (!offensive || setups[i].attack) {
					chosen = &setups[i]
					return true
				}
			}
			return false
		})
		if len(path) > 0 && chosen != nil {
			if len(path) > 1 || !onCentre(actor.Position) {
				return bot.follow(action, actor, path)
			}
			if actor.Facing != nativeDirection(chosen.direction) {
				action.Move = nativeDirection(chosen.direction)
			} else {
				action.PlaceBomb = true
			}
			return action
		}
	}
	if path := world.nearest(here, safe, world.pickupAt); len(path) > 0 {
		return bot.follow(action, actor, path)
	}
	return action
}
