package main

// Autoplay: a scripted stand-in for the local human, for two-client tests
// without anyone at the keyboard. It is not the server's AI and not a model
// of how people play; it only has to move, collect, place bubbles and avoid
// blasts well enough to drive the rule paths (pickups, kills, relives) on the
// client's real input and network path.
//
// It plans on whole cells with what a player can see: open cells, bubbles,
// visible items and the danger timeline of the bubbles already placed.

import (
	"qqtang/internal/game/battleengine"
)

const (
	autoplayHorizonMS  = 4000
	autoplayReplanMS   = 100
	autoplayMaxEscape  = 8   // cells
	autoplayCentreSlop = 3   // pixels off a cell centre that still count as on it
	autoplayMarginMS   = 150 // extra time asked of every escape
)

type autoplayBot struct {
	player    uint16
	timeline  battleengine.DangerTimeline
	plannedAt uint32
	planned   bool
	// The rule's objectives, when the map has them (rules 3 and 6).
	bun       *bunRule
	sculpture *sculptureRule
	tank      *tankRule
	machine   *machineRule
	box       *boxRule
	boss      *bossRule // a Boss overlay: its living Bosses are targets too
	adventure *adventureRule // rule 12: its NPCs are the targets
	// Rule 2: no own bubbles; kick the dropped ones instead.
	kick bool
}

func newAutoplayBot(player uint16) *autoplayBot {
	return &autoplayBot{player: player}
}

var autoplayDirections = [...]battleengine.Direction{
	battleengine.DirectionUp, battleengine.DirectionRight, battleengine.DirectionDown, battleengine.DirectionLeft,
}

func stepCell(cell battleengine.Cell, direction battleengine.Direction) battleengine.Cell {
	switch direction {
	case battleengine.DirectionUp:
		cell.Row--
	case battleengine.DirectionDown:
		cell.Row++
	case battleengine.DirectionLeft:
		cell.Col--
	case battleengine.DirectionRight:
		cell.Col++
	}
	return cell
}

// act chooses the local player's input for the next engine tick.
func (bot *autoplayBot) act(engine *battleengine.Engine) battleengine.Action {
	action := battleengine.Action{PlayerID: bot.player}
	actor, ok := actorByID(engine.Actors(), bot.player)
	if !ok || actor.State != battleengine.ActorActive {
		return action
	}
	now := engine.ElapsedMS()
	if !bot.planned || now-bot.plannedAt >= autoplayReplanMS {
		timeline, err := engine.DangerTimeline(autoplayHorizonMS)
		if err != nil {
			return action
		}
		bot.timeline, bot.plannedAt, bot.planned = timeline, now, true
	}
	msPerCell := uint32(1000 * battleengine.CellSizePixels / max(1, int(actor.SpeedPixelsPerSecond)))
	here := actor.Position.Cell()
	world := newAutoplayWorld(engine)
	if bot.adventure != nil {
		// Rule 12 flames keep hurting for as long as they burn, not only at
		// the impact instant: never walk into one.
		for _, flame := range engine.Flames() {
			if flame.ImpactAtMS <= now && now < flame.ExpiresAtMS && flame.Cell != here {
				world.bombs[flame.Cell] = true
			}
		}
	}
	if bot.box != nil {
		return bot.actBox(engine, world, actor, action, msPerCell)
	}
	if bot.kick {
		return bot.actKick(engine, world, actor, action, msPerCell)
	}

	// 1. Leave a cell a known blast will reach.
	if _, threatened := bot.timeline.ImpactAt(here); threatened {
		if path := world.escape(here, bot.timeline, now, msPerCell); len(path) > 1 {
			action.Move = steer(actor.Position, path[1])
		}
		return action
	}
	safe := func(cell battleengine.Cell) bool {
		_, threatened := bot.timeline.ImpactAt(cell)
		return !threatened
	}
	if bot.machine != nil && bot.machine.ready(bot.machine.side(actor.TeamID), now) {
		for _, gate := range bot.machine.gates {
			if gate.side != bot.machine.side(actor.TeamID) {
				continue
			}
			if here == gate.trigger() {
				if onCentre(actor.Position) {
					action.PlaceBomb = true
				} else {
					action.Move = steer(actor.Position, here)
				}
				return action
			}
			if here == gate.approach() {
				action.Move = steer(actor.Position, gate.trigger())
				return action
			}
			if path := world.nearest(here, safe, func(c battleengine.Cell) bool { return c == gate.approach() }); len(path) > 0 {
				return bot.follow(action, actor, path)
			}
		}
	}
	if bot.machine != nil && bot.machine.charges[actor.PlayerID] != nil && bot.machine.charges[actor.PlayerID].count > 0 && engine.CanThrowMachineBomb(actor.PlayerID) {
		// Walk to a firing line whose native cursor blast can reach an opponent.
		// Otherwise two bots can throw safely past each other for a whole round.
		aim := func(at battleengine.Cell) bool {
			facing := battleengine.DirectionRight
			if at.Col >= 7 {
				facing = battleengine.DirectionLeft
			}
			target, _, ok := machineThrowTarget(engine, at, facing)
			if !ok {
				return false
			}
			for _, other := range engine.Actors() {
				if other.TeamID != actor.TeamID && other.State == battleengine.ActorActive && world.blastCovers(target, actor.BombPower, other.Position.Cell()) {
					return true
				}
			}
			return false
		}
		if path := world.nearest(here, safe, aim); len(path) > 1 {
			return bot.follow(action, actor, path)
		}
		// Outside a launcher, space throws toward the native aiming cursor.
		// Face across the centre rather than place ordinary bubbles at our feet.
		facing := battleengine.DirectionRight
		if here.Col >= 7 {
			facing = battleengine.DirectionLeft
		}
		if actor.Facing != facing {
			action.Move = facing
			return action
		}
		if _, _, ok := machineThrowTarget(engine, here, actor.Facing); ok {
			action.PlaceBomb = true
			return action
		}
	}
	// 2. Rule objectives: carry buns home, pick loose ones up, raid the
	// opposing house.
	if path := bot.bunPath(world, actor, here, safe); len(path) > 0 {
		return bot.follow(action, actor, path)
	}
	if bot.sculpture != nil {
		// Knock a piece off the opposing statue with the style-9 bubble.
		if bot.sculpture.worthBombing(here, actor) && onCentre(actor.Position) && bot.canEscapeOwnBubble(engine, actor, msPerCell) {
			action.UseActionID = sculptureBombItem
			return action
		}
		if path := bot.sculpturePath(world, actor, here, safe); len(path) > 0 {
			return bot.follow(action, actor, path)
		}
	}
	// Rule 12: once the stage is clear, walk to the exit door.
	if a := bot.adventure; a != nil && a.clearAt != 0 && !a.doorSent {
		door := battleengine.Cell{Row: int16(a.stage.LevelRow), Col: int16(a.stage.LevelCol)}
		if here == door {
			return action
		}
		if path := world.nearest(here, safe, func(c battleengine.Cell) bool { return c == door }); len(path) > 0 {
			return bot.follow(action, actor, path)
		}
	}
	// 3. Collect the nearest visible item; gems (rule objectives) first.
	if path := world.nearest(here, safe, world.objectiveAt); len(path) > 0 {
		return bot.follow(action, actor, path)
	}
	if path := world.nearest(here, safe, world.pickupAt); len(path) > 0 {
		return bot.follow(action, actor, path)
	}
	worthBombing := func(cell battleengine.Cell) bool {
		return world.worthBombing(cell, actor) || bot.tank != nil && bot.tank.worthBombing(world, cell, actor) || bot.bossInReach(world, cell, actor) || bot.adventureNPCInReach(world, cell, actor)
	}
	// 4. Place a bubble beside a breakable wall or an opponent, if it can be
	// escaped.
	if worthBombing(here) && onCentre(actor.Position) && bot.canEscapeOwnBubble(engine, actor, msPerCell) {
		action.PlaceBomb = true
		return action
	}
	// 5. Walk toward the nearest cell where a bubble would be worth it.
	if path := world.nearest(here, safe, worthBombing); len(path) > 0 {
		return bot.follow(action, actor, path)
	}
	return action
}

// bossInReach: a bubble at cell would catch a Boss that can be hurt.
func (bot *autoplayBot) bossInReach(world autoplayWorld, cell battleengine.Cell, actor battleengine.Actor) bool {
	if bot.boss == nil || len(bot.boss.actors) == 0 {
		return false
	}
	for _, b := range bot.boss.actors {
		if !b.dead && b.hp > 0 && world.blastCovers(cell, actor.BombPower, b.pos.Cell()) {
			return true
		}
	}
	return false
}

func (bot *autoplayBot) adventureNPCInReach(world autoplayWorld, cell battleengine.Cell, actor battleengine.Actor) bool {
	if bot.adventure == nil {
		return false
	}
	for _, n := range bot.adventure.npcs {
		if !n.dead && world.blastCovers(cell, actor.BombPower, n.pos.Cell()) {
			return true
		}
	}
	return false
}

func (bot *autoplayBot) follow(action battleengine.Action, actor battleengine.Actor, path []battleengine.Cell) battleengine.Action {
	if len(path) > 1 {
		action.Move = steer(actor.Position, path[1])
	} else if !onCentre(actor.Position) {
		action.Move = steer(actor.Position, path[0])
	}
	return action
}

// canEscapeOwnBubble places the bubble on a copy of the match and looks for
// a reachable cell its blast (with every other known blast) never reaches.
func (bot *autoplayBot) canEscapeOwnBubble(engine *battleengine.Engine, actor battleengine.Actor, msPerCell uint32) bool {
	here := actor.Position.Cell()
	clone := engine.Clone()
	if _, err := clone.ApplyVerifiedBombPlacementAt(bot.player, here, uint16(actor.BombPower)+1, 0, clone.ElapsedMS()); err != nil {
		return false
	}
	timeline, err := clone.DangerTimeline(autoplayHorizonMS)
	if err != nil {
		return false
	}
	path := newAutoplayWorld(clone).escape(here, timeline, clone.ElapsedMS(), msPerCell)
	return len(path) > 1
}

// steer is the held direction that moves the actor toward the centre of the
// next cell, first lining it up on the other axis.
func steer(position battleengine.Position, next battleengine.Cell) battleengine.Direction {
	target := battleengine.PositionAtCellCenter(next)
	dx, dy := target.X-position.X, target.Y-position.Y
	horizontal := abs32(dx) > abs32(dy)
	switch {
	case horizontal && abs32(dy) > autoplayCentreSlop:
		horizontal = false
	case !horizontal && abs32(dx) > autoplayCentreSlop:
		horizontal = true
	}
	if horizontal {
		if dx > 0 {
			return battleengine.DirectionRight
		}
		return battleengine.DirectionLeft
	}
	if dy > 0 {
		return battleengine.DirectionDown
	}
	if dy < 0 {
		return battleengine.DirectionUp
	}
	return battleengine.DirectionNone
}

func onCentre(position battleengine.Position) bool {
	centre := battleengine.PositionAtCellCenter(position.Cell())
	return abs32(centre.X-position.X) <= autoplayCentreSlop && abs32(centre.Y-position.Y) <= autoplayCentreSlop
}

func abs32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

// autoplayWorld is the public map the bot plans on.
type autoplayWorld struct {
	engine  *battleengine.Engine
	grid    battleengine.Grid
	bombs   map[battleengine.Cell]bool
	pickups map[battleengine.Cell]uint32
}

func newAutoplayWorld(engine *battleengine.Engine) autoplayWorld {
	world := autoplayWorld{engine: engine, grid: engine.Grid(), bombs: map[battleengine.Cell]bool{}, pickups: map[battleengine.Cell]uint32{}}
	for _, bomb := range engine.Bombs() {
		world.bombs[bomb.Cell] = true
	}
	for _, pickup := range engine.Pickups() {
		if pickup.State == battleengine.PickupAvailable {
			world.pickups[pickup.Cell] = pickup.SceneID
		}
	}
	return world
}

func (world autoplayWorld) open(cell battleengine.Cell) bool {
	tile, inside := world.grid.Cell(cell)
	return inside && tile.Kind == battleengine.CellOpen && !world.bombs[cell]
}

func (world autoplayWorld) pickupAt(cell battleengine.Cell) bool {
	_, found := world.pickups[cell]
	return found
}

func (world autoplayWorld) objectiveAt(cell battleengine.Cell) bool {
	scene, found := world.pickups[cell]
	return found && scene >= 150 && scene <= 152
}

// worthBombing: a breakable wall or an opponent next to the cell.
func (world autoplayWorld) worthBombing(cell battleengine.Cell, self battleengine.Actor) bool {
	if world.bombs[cell] {
		return false
	}
	active := 0
	for _, bomb := range world.engine.Bombs() {
		if bomb.OwnerID == self.PlayerID {
			active++
		}
	}
	if active >= int(self.BombCapacity) {
		return false
	}
	for _, direction := range autoplayDirections {
		next := stepCell(cell, direction)
		if tile, inside := world.grid.Cell(next); inside && tile.Kind == battleengine.CellBreakable {
			return true
		}
		for _, actor := range world.engine.Actors() {
			if actor.TeamID != self.TeamID && actor.State == battleengine.ActorActive && actor.Position.Cell() == next {
				return true
			}
		}
	}
	return false
}

// nearest is the shortest path over open cells that pass allowed to the
// first cell satisfying goal, the start included; nil if none.
func (world autoplayWorld) nearest(start battleengine.Cell, allowed, goal func(battleengine.Cell) bool) []battleengine.Cell {
	return world.search(start, func(cell battleengine.Cell, _ int) bool { return cell == start || allowed(cell) }, func(cell battleengine.Cell, _ int) bool { return goal(cell) }, -1)
}

// escape is the shortest path to a cell no known blast reaches, entering
// only cells that are not hit while the actor would be in them.
func (world autoplayWorld) escape(start battleengine.Cell, timeline battleengine.DangerTimeline, now, msPerCell uint32) []battleengine.Cell {
	passable := func(cell battleengine.Cell, steps int) bool {
		if cell == start {
			return true
		}
		impact, threatened := timeline.ImpactAt(cell)
		if !threatened {
			return true
		}
		leave := now + uint32(steps+1)*msPerCell + autoplayMarginMS
		clear, _ := timeline.ClearAt(cell)
		return impact > leave || clear < now+uint32(steps)*msPerCell
	}
	goal := func(cell battleengine.Cell, _ int) bool {
		_, threatened := timeline.ImpactAt(cell)
		return !threatened
	}
	return world.search(start, passable, goal, autoplayMaxEscape)
}

func (world autoplayWorld) search(start battleengine.Cell, passable, goal func(battleengine.Cell, int) bool, limit int) []battleengine.Cell {
	previous := map[battleengine.Cell]battleengine.Cell{start: start}
	queue := []battleengine.Cell{start}
	depth := map[battleengine.Cell]int{start: 0}
	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]
		if goal(cell, depth[cell]) {
			path := []battleengine.Cell{cell}
			for cell != start {
				cell = previous[cell]
				path = append([]battleengine.Cell{cell}, path...)
			}
			return path
		}
		if limit >= 0 && depth[cell] >= limit {
			continue
		}
		for _, direction := range autoplayDirections {
			next := stepCell(cell, direction)
			if _, seen := previous[next]; seen || !world.open(next) || !passable(next, depth[cell]+1) {
				continue
			}
			from, _ := world.grid.Cell(cell)
			to, _ := world.grid.Cell(next)
			if !from.PlayerExitPassable(direction) || !to.PlayerPassable(direction) {
				continue
			}
			previous[next] = cell
			depth[next] = depth[cell] + 1
			queue = append(queue, next)
		}
	}
	return nil
}

// bunPath is the way to the bot's next bun goal, or nil.
func (bot *autoplayBot) bunPath(world autoplayWorld, actor battleengine.Actor, here battleengine.Cell, safe func(battleengine.Cell) bool) []battleengine.Cell {
	rule := bot.bun
	if rule == nil {
		return nil
	}
	if rule.carrying(actor.PlayerID) != bunNone {
		return world.nearest(here, safe, func(cell battleengine.Cell) bool {
			house := rule.houseAt(cell)
			return house != nil && house.team == actor.TeamID
		})
	}
	if path := world.nearest(here, safe, func(cell battleengine.Cell) bool { return rule.looseAt(cell, 0, true) >= 0 }); len(path) > 0 {
		return path
	}
	return world.nearest(here, safe, func(cell battleengine.Cell) bool {
		house := rule.houseAt(cell)
		return house != nil && house.team != actor.TeamID && house.own > 0
	})
}

// sculpturePath is the way to the bot's next rule-6 goal, or nil: its own
// statue while carrying, otherwise the nearest visible fragment, otherwise
// a cell beside the opposing statue while it holds a piece and the bot
// still has its style-9 bubble.
func (bot *autoplayBot) sculpturePath(world autoplayWorld, actor battleengine.Actor, here battleengine.Cell, safe func(battleengine.Cell) bool) []battleengine.Cell {
	rule := bot.sculpture
	if rule.carrying(actor.PlayerID) != bunNone {
		return world.nearest(here, safe, func(cell battleengine.Cell) bool {
			statue := rule.statueAt(cell)
			return statue != nil && statue.team == actor.TeamID
		})
	}
	if path := world.nearest(here, safe, func(cell battleengine.Cell) bool { return rule.looseAt(cell, 0, true) >= 0 }); len(path) > 0 {
		return path
	}
	return world.nearest(here, safe, func(cell battleengine.Cell) bool { return rule.worthBombing(cell, actor) })
}
