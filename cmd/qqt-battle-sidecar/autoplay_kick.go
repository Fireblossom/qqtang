package main

// The test bot's rule-2 play: it cannot place bubbles, so it walks up to a
// dropped bubble and kicks it where its blast would reach an opponent. With
// bubbles everywhere, a cell is only avoided when a blast reaches it while
// the bot would be there.

import (
	"qqtang/internal/game/battleengine"
)

const (
	kickUrgentMS = 1200 // a blast this close sends the bot away before anything else
	kickSlackMS  = 300
)

func (bot *autoplayBot) actKick(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, action battleengine.Action, msPerCell uint32) battleengine.Action {
	now := engine.ElapsedMS()
	here := actor.Position.Cell()
	timely := func(cell battleengine.Cell, steps int) bool {
		if cell == here {
			return true
		}
		impact, threatened := bot.timeline.ImpactAt(cell)
		if !threatened {
			return true
		}
		arrive := now + uint32(steps)*msPerCell
		clear, _ := bot.timeline.ClearAt(cell)
		return impact > arrive+msPerCell+kickSlackMS || clear < arrive
	}
	if impact, threatened := bot.timeline.ImpactAt(here); threatened && impact < now+kickUrgentMS {
		if path := world.escape(here, bot.timeline, now, msPerCell); len(path) > 1 {
			action.Move = steer(actor.Position, path[1])
		}
		return action
	}
	var kickDirection battleengine.Direction
	kickFrom := func(cell battleengine.Cell, _ int) bool {
		kickDirection = world.kickToward(engine, cell, actor, now)
		return kickDirection != battleengine.DirectionNone
	}
	if path := world.search(here, timely, kickFrom, -1); len(path) > 0 {
		if len(path) == 1 {
			if onCentre(actor.Position) {
				action.Move = kickDirection
			} else {
				action.Move = steer(actor.Position, here)
			}
			return action
		}
		return bot.follow(action, actor, path)
	}
	if path := world.search(here, timely, func(cell battleengine.Cell, _ int) bool { return world.pickupAt(cell) }, -1); len(path) > 0 {
		return bot.follow(action, actor, path)
	}
	if _, threatened := bot.timeline.ImpactAt(here); threatened {
		if path := world.escape(here, bot.timeline, now, msPerCell); len(path) > 1 {
			action.Move = steer(actor.Position, path[1])
		}
	}
	return action
}

// kickToward is the direction from cell into a landed bubble next to it
// whose kick lands where its blast reaches an active opponent, or
// DirectionNone.
func (world autoplayWorld) kickToward(engine *battleengine.Engine, cell battleengine.Cell, self battleengine.Actor, now uint32) battleengine.Direction {
	for _, direction := range autoplayDirections {
		next := stepCell(cell, direction)
		if !world.bombs[next] {
			continue
		}
		for _, bomb := range engine.Bombs() {
			if bomb.Cell != next || bomb.FlightUntilMS > now {
				continue
			}
			target, found := engine.KickDestination(next, direction)
			if !found {
				continue
			}
			for _, actor := range engine.Actors() {
				if actor.TeamID != self.TeamID && actor.State == battleengine.ActorActive && world.blastCovers(target, bomb.Power, actor.Position.Cell()) {
					return direction
				}
			}
		}
	}
	return battleengine.DirectionNone
}

// blastCovers: a bubble of reach power at from reaches target.
func (world autoplayWorld) blastCovers(from battleengine.Cell, power byte, target battleengine.Cell) bool {
	if from == target {
		return true
	}
	if from.Row != target.Row && from.Col != target.Col {
		return false
	}
	for _, direction := range autoplayDirections {
		cell := from
		for reach := byte(0); reach < power; reach++ {
			cell = stepCell(cell, direction)
			tile, inside := world.grid.Cell(cell)
			if !inside || tile.Kind == battleengine.CellSolid {
				break
			}
			if cell == target {
				return true
			}
			if tile.Kind != battleengine.CellOpen {
				break
			}
		}
	}
	return false
}
