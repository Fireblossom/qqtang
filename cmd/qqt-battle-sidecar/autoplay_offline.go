package main

// Offline AI seats reuse the autoplay bot (autoplay.go) with a few additions
// that make it a usable opponent and teammate rather than a test driver.
// They only apply with bot.offline set, so the two-client tests keep the
// original bot:
//   - touch trapped players: finish opponents, free teammates;
//   - never place a bubble whose blast reaches a teammate;
//   - push a box out of the way when walled in (map 311 starts every
//     player in a pocket of pushable boxes).

import "qqtang/internal/game/battleengine"

// contactPath leads to the nearest trapped player worth touching: an
// opponent to finish, or a teammate to free. Rules without syrup traps
// (durability rules 7 and 8) never have one.
func (bot *autoplayBot) contactPath(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, here battleengine.Cell, safe func(battleengine.Cell) bool) []battleengine.Cell {
	targets := map[battleengine.Cell]bool{}
	for _, other := range engine.Actors() {
		if other.PlayerID == actor.PlayerID || other.State != battleengine.ActorTrapped {
			continue
		}
		targets[other.Position.Cell()] = true
	}
	if len(targets) == 0 {
		return nil
	}
	path := world.nearest(here, safe, func(cell battleengine.Cell) bool { return targets[cell] })
	if len(path) > 12 {
		return nil // too far to arrive in time
	}
	return path
}

// hitsTeammate: a bubble at cell would reach a teammate, active or trapped.
func (bot *autoplayBot) hitsTeammate(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, cell battleengine.Cell) bool {
	for _, other := range engine.Actors() {
		if other.PlayerID == actor.PlayerID || other.TeamID != actor.TeamID || other.State == battleengine.ActorEliminated {
			continue
		}
		if world.blastCovers(cell, actor.BombPower, other.Position.Cell()) {
			return true
		}
	}
	return false
}

// huntPath leads next to the nearest active opponent.
func (bot *autoplayBot) huntPath(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, here battleengine.Cell, safe func(battleengine.Cell) bool) []battleengine.Cell {
	near := map[battleengine.Cell]bool{}
	for _, other := range engine.Actors() {
		if other.TeamID == actor.TeamID || other.State != battleengine.ActorActive {
			continue
		}
		cell := other.Position.Cell()
		near[cell] = true
		for _, direction := range autoplayDirections {
			near[stepCell(cell, direction)] = true
		}
	}
	return world.nearest(here, safe, func(cell battleengine.Cell) bool { return near[cell] })
}

// pushOut is the direction that pushes a box into a free cell beyond it,
// or none: the way out of a pocket of pushable boxes.
func (bot *autoplayBot) pushOut(engine *battleengine.Engine, world autoplayWorld, here battleengine.Cell) battleengine.Direction {
	best, bestRoom := battleengine.DirectionNone, -1
	for _, direction := range autoplayDirections {
		next := stepCell(here, direction)
		tile, inside := world.grid.Cell(next)
		if !inside || !tile.NormalPushable || world.bombs[next] {
			continue
		}
		beyond := stepCell(next, direction)
		if !world.open(beyond) || world.pickupAt(beyond) {
			continue
		}
		if _, threatened := bot.timeline.ImpactAt(next); threatened {
			continue
		}
		occupied := false
		for _, other := range engine.Actors() {
			if other.State != battleengine.ActorEliminated && other.Position.Cell() == beyond {
				occupied = true
			}
		}
		if occupied {
			continue
		}
		// Prefer the push that opens the most room behind the box.
		room := 0
		for _, around := range autoplayDirections {
			if world.open(stepCell(beyond, around)) {
				room++
			}
		}
		if room > bestRoom {
			best, bestRoom = direction, room
		}
	}
	return best
}
