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

// bombsChanged reports a bubble placed, moved or gone since the last call,
// so the danger timeline is rebuilt at once instead of up to 100 ms later.
func (bot *autoplayBot) bombsChanged(engine *battleengine.Engine) bool {
	var signature uint64
	for _, bomb := range engine.Bombs() {
		signature = signature*31 + uint64(bomb.ID)<<20 ^ uint64(uint16(bomb.Cell.Row))<<8 ^ uint64(uint16(bomb.Cell.Col))
	}
	signature = signature*31 + uint64(len(engine.Bombs()))
	changed := signature != bot.bombSignature
	bot.bombSignature = signature
	return changed
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

// opponentInReach: a bubble at cell would reach an active opponent (not
// only one standing next to it), with a bubble left to place.
func (bot *autoplayBot) opponentInReach(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, cell battleengine.Cell) bool {
	if world.bombs[cell] {
		return false
	}
	active := 0
	for _, bomb := range engine.Bombs() {
		if bomb.OwnerID == actor.PlayerID {
			active++
		}
	}
	if active >= int(actor.BombCapacity) {
		return false
	}
	for _, other := range engine.Actors() {
		if other.TeamID != actor.TeamID && other.State == battleengine.ActorActive && other.HarmProtectionExpiresAt <= engine.ElapsedMS() &&
			world.blastCovers(cell, actor.BombPower, other.Position.Cell()) {
			return true
		}
	}
	return false
}

// baseAttack (rule 13) goes for the opposing base before anything else but
// trapped players: a bubble whose blast crosses its centre takes 10 HP.
func (bot *autoplayBot) baseAttack(engine *battleengine.Engine, world autoplayWorld, actor battleengine.Actor, action battleengine.Action, here battleengine.Cell, safe func(battleengine.Cell) bool, msPerCell uint32) (battleengine.Action, bool) {
	if bot.tank == nil {
		return action, false
	}
	worth := func(cell battleengine.Cell) bool {
		return bot.tank.worthBombing(world, cell, actor) && !bot.hitsTeammate(engine, world, actor, cell)
	}
	if worth(here) {
		if !onCentre(actor.Position) {
			// Line up on the cell first; deciding from the cell edge makes
			// the bot swing between two cells.
			action.Move = steer(actor.Position, here)
			return action, true
		}
		if bot.canEscapeOwnBubble(engine, actor, msPerCell) {
			action.PlaceBomb = true
			return action, true
		}
		return action, false
	}
	if path := world.nearest(here, safe, worth); len(path) > 1 && len(path) <= 20 {
		return bot.follow(action, actor, path), true
	}
	return action, false
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

// waitedTooLong tracks waiting on a good cell where no bubble can be placed;
// after 1.5 s the cell is given up for 5 s.
func (bot *autoplayBot) waitedTooLong(here battleengine.Cell, now uint32) bool {
	if bot.waitCell != here {
		bot.waitCell, bot.waitSince = here, now
		return false
	}
	if now-bot.waitSince < 1500 {
		return false
	}
	if bot.avoid == nil {
		bot.avoid = map[battleengine.Cell]uint32{}
	}
	bot.avoid[here] = now + 5000
	bot.waitCell = battleengine.Cell{Row: -1}
	return true
}

func (bot *autoplayBot) avoiding(cell battleengine.Cell, now uint32) bool {
	until, ok := bot.avoid[cell]
	return ok && now < until
}
