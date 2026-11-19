package main

// The tutorial's autoplay (tests and headless screenshots, never a normal
// game): a driver that plays the local player through the free phases the
// way a beginner is told to: walk to the arrow, bomb the anemones in the
// way, eat the items, hold the top-left corner on 1504, then fight on 311.

import "qqtang/internal/game/battleengine"

type tutorialDriver struct {
	local uint16
	bot   *autoplayBot
}

func newTutorialDriver(local uint16) *tutorialDriver {
	bot := newAutoplayBot(local)
	bot.offline = true
	return &tutorialDriver{local: local, bot: bot}
}

func (driver *tutorialDriver) act(match *tutorialMatch) battleengine.Action {
	action := battleengine.Action{PlayerID: driver.local}
	actor, ok := match.actor(driver.local)
	if !ok || actor.State != battleengine.ActorActive {
		return action
	}
	switch {
	case match.begin.MapID == tutorialDefend && match.routineMode:
		return driver.defend(match, actor)
	case !tutorialStage(match.begin.MapID):
		return driver.bot.act(match.engine)
	case len(match.tasks) == 0:
		return action
	}
	switch record := match.tasks[0]; record.Action {
	case taskReach:
		return driver.reach(match, actor, func(cell battleengine.Cell) bool { return cell == record.to() })
	case taskEatItems:
		world := newAutoplayWorld(match.engine)
		if len(world.pickups) > 0 {
			if path := world.nearest(actor.Position.Cell(), world.open, world.pickupAt); len(path) > 0 {
				return driver.bot.follow(action, actor, path)
			}
		}
		// The nearest cell beside an anemone whose blast spares the teammate.
		stand := func(cell battleengine.Cell) bool {
			if world.bombs[cell] || driver.hitsTeammate(match, cell, actor.BombPower) {
				return false
			}
			for _, direction := range autoplayDirections {
				if tile, inside := world.grid.Cell(stepCell(cell, direction)); inside && tile.Kind == battleengine.CellBreakable {
					return true
				}
			}
			return false
		}
		path := world.nearest(actor.Position.Cell(), world.open, stand)
		switch {
		case len(path) == 0:
			return action
		case len(path) > 1 || !onCentre(actor.Position):
			return driver.bot.follow(action, actor, path)
		case driver.live(match) < int(actor.BombCapacity):
			action.PlaceBomb = true
		}
	}
	return action
}

func (driver *tutorialDriver) live(match *tutorialMatch) int {
	live := 0
	for _, bomb := range match.engine.Bombs() {
		if bomb.OwnerID == driver.local {
			live++
		}
	}
	return live
}

// reach walks to the nearest goal cell, bombing breakable cells on the way.
// The local player is flame-immune on 1501..1503, so it waits beside its
// own bubbles; it never bombs a line with a teammate in it.
func (driver *tutorialDriver) reach(match *tutorialMatch, actor battleengine.Actor, goal func(battleengine.Cell) bool) battleengine.Action {
	action := battleengine.Action{PlayerID: driver.local}
	world := newAutoplayWorld(match.engine)
	live := 0
	for _, bomb := range match.engine.Bombs() {
		if bomb.OwnerID == driver.local {
			live++
		}
	}
	if live >= int(actor.BombCapacity) {
		return action // wait for one to burst
	}
	here := actor.Position.Cell()
	if path := world.nearest(here, world.open, goal); len(path) > 0 {
		if goal(here) && len(path) == 1 && onCentre(actor.Position) {
			return action
		}
		return driver.bot.follow(action, actor, path)
	}
	// Through breakable cells: walk to the last open cell before the first
	// breakable one, then bomb it.
	path := driver.throughWalls(world, here, goal)
	for index := 1; index < len(path); index++ {
		tile, _ := world.grid.Cell(path[index])
		if tile.Kind != battleengine.CellBreakable {
			continue
		}
		stand := path[index-1]
		if here == stand && onCentre(actor.Position) {
			if driver.hitsTeammate(match, stand, actor.BombPower) {
				return action
			}
			action.PlaceBomb = true
			return action
		}
		return driver.bot.follow(action, actor, path[:index])
	}
	return action
}

// throughWalls is the shortest path that may cross breakable cells.
func (driver *tutorialDriver) throughWalls(world autoplayWorld, start battleengine.Cell, goal func(battleengine.Cell) bool) []battleengine.Cell {
	previous := map[battleengine.Cell]battleengine.Cell{start: start}
	queue := []battleengine.Cell{start}
	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]
		if goal(cell) && cell != start {
			path := []battleengine.Cell{cell}
			for cell != start {
				cell = previous[cell]
				path = append([]battleengine.Cell{cell}, path...)
			}
			return path
		}
		for _, direction := range autoplayDirections {
			next := stepCell(cell, direction)
			if _, seen := previous[next]; seen {
				continue
			}
			tile, inside := world.grid.Cell(next)
			if !inside || world.bombs[next] || tile.Kind == battleengine.CellSolid {
				continue
			}
			previous[next] = cell
			queue = append(queue, next)
		}
	}
	return nil
}

// hitsTeammate: a bubble at cell with this power would reach a teammate.
func (driver *tutorialDriver) hitsTeammate(match *tutorialMatch, cell battleengine.Cell, power byte) bool {
	grid := match.engine.Grid()
	reach := map[battleengine.Cell]bool{cell: true}
	for _, direction := range autoplayDirections {
		next := cell
		for step := 0; step < int(power); step++ {
			next = stepCell(next, direction)
			tile, inside := grid.Cell(next)
			if !inside || tile.Kind != battleengine.CellOpen {
				break
			}
			reach[next] = true
		}
	}
	for _, other := range match.engine.Actors() {
		if other.PlayerID != driver.local && other.TeamID == tutorialTeam && other.State != battleengine.ActorEliminated && reach[other.Position.Cell()] {
			return true
		}
	}
	return false
}

// defend holds 1504's top-left corner: wait at (5, 1) for the opponent that
// walks to (0, 1), bubble it, and stand clear at (5, 4).
func (driver *tutorialDriver) defend(match *tutorialMatch, actor battleengine.Actor) battleengine.Action {
	action := battleengine.Action{PlayerID: driver.local}
	world := newAutoplayWorld(match.engine)
	post, shelter := battleengine.Cell{Row: 1, Col: 5}, battleengine.Cell{Row: 4, Col: 5}
	target := post
	for _, bomb := range match.engine.Bombs() {
		if bomb.OwnerID == driver.local {
			target = shelter
		}
	}
	here := actor.Position.Cell()
	if target == post && here == post && onCentre(actor.Position) {
		for _, other := range match.engine.Actors() {
			cell := other.Position.Cell()
			if other.TeamID != tutorialTeam && other.State == battleengine.ActorActive && cell.Row == 1 && cell.Col >= 5 && cell.Col <= 7 {
				action.PlaceBomb = true
				return action
			}
		}
		return action
	}
	if path := world.nearest(here, world.open, func(cell battleengine.Cell) bool { return cell == target }); len(path) > 0 {
		return driver.bot.follow(action, actor, path)
	}
	return action
}
