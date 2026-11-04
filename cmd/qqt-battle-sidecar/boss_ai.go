package main

// The Boss controllers of FUN_005df20b (docs/boss-ai-re.md). Only the
// arbitrator runs them. AIType 3 (0x005DF058/0x005DF0EB) decides a direction
// every 7th frame from scratch: chase a trapped player within 10 cells, flee
// danger, chase the nearest player, collect an item within 8 cells, run the
// generic skills (0x005B0D86), then follow its old path or go bomb a box. It
// checks every frame whether to place a bubble. The pathfinder is
// WFPathFinder (0x005E006A).
//
// Commands retain their direction between decisions. Default, untransformed
// general Bosses use the shared native float32 movement and endpoint forecast.
// One engine tick (20ms) is one decision frame; planner/path consumption and
// special capabilities still have separate compatibility work.

import (
	"math"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

const (
	bossDirStop = 9
	// 0x006112AF: decide when the counter exceeds 5, i.e. every 7th frame.
	bossDecideFrames = 5
	// 0x005B0D86: the generic skills' master gate and cooldowns.
	bossSkillGateMS = 10_000
	bossSkillOneMS  = 10_000
	bossSkillLongMS = 30_000
	bossGlueCells   = 5
	bossTrapCells   = 10
	// 0x005D9BBF(obj, row, col, 400, 0): thrown objects fly at 400 px/s.
	bossFlightPixelsPerSecond = 400
	bossSceneGlue             = 43
	bossSceneTrap             = 41
	bossTransformItem         = 110
	pathMaxF                  = 3900
	pathMaxOpen               = 195
)

// The direction codes and deltas of 0x007F58A8: right, up, left, down.
var bossDelta = [4][2]int16{{1, 0}, {0, -1}, {-1, 0}, {0, 1}}

func bossStep(cell battleengine.Cell, dir int) battleengine.Cell {
	return battleengine.Cell{Row: cell.Row + bossDelta[dir][1], Col: cell.Col + bossDelta[dir][0]}
}

func newBossController(info game.CompetitiveBossInfo) bossController {
	switch info.AIType {
	case 3:
		return &bossGeneral{}
	case 5:
		return &bossKick{}
	}
	return &bossWander{}
}

// pathNode is one item of N+0x75C: cell, direction (9 at the goal),
// kind (0 start, 1 box, 2 danger, 3 plain, 4 bubble) and native padding.
type pathNode struct {
	cell    battleengine.Cell
	dir     int
	kind    byte
	padding byte
}

type bossGeneral struct {
	// path[0] is the current cell (the original vector's last item).
	path           []pathNode
	pathMismatches uint32
	gate           uint32
	last           [4]uint32 // skills 1..3 by index
	inited         bool
	pirate         pirateState
}

// bossWorld is the per-frame view the original reads from the scene.
type bossWorld struct {
	match    *onlineMatch
	burning  map[battleengine.Cell]bool
	bombs    map[battleengine.Cell]bool
	timeline battleengine.DangerTimeline
	now      uint32
}

func (match *onlineMatch) bossWorld(now uint32) *bossWorld {
	w := &bossWorld{match: match, burning: map[battleengine.Cell]bool{}, bombs: map[battleengine.Cell]bool{}, now: now}
	for _, flame := range match.engine.Flames() {
		if flame.ImpactAtMS <= now && now < flame.ExpiresAtMS {
			w.burning[flame.Cell] = true
		}
	}
	for _, bomb := range match.engine.Bombs() {
		w.bombs[bomb.Cell] = true
	}
	w.timeline, _ = match.engine.DangerTimeline(4000)
	return w
}

func (w *bossWorld) inside(c battleengine.Cell) bool {
	grid := w.match.engine.Grid()
	return c.Row >= 0 && c.Col >= 0 && int(c.Row) < int(grid.Height) && int(c.Col) < int(grid.Width)
}

// danger is 0x005D37C4: 2 while burning, 1 when a bubble will reach it.
func (w *bossWorld) danger(c battleengine.Cell) int {
	if w.burning[c] {
		return 2
	}
	if at, bad := w.timeline.ImpactAt(c); bad && at >= w.now {
		return 1
	}
	return 0
}

// element reports a map element on the cell and whether it can be destroyed.
func (w *bossWorld) element(c battleengine.Cell) (present, breakable bool) {
	tile, ok := w.match.engine.TileAt(c)
	if !ok {
		return true, false
	}
	if tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied {
		return false, false
	}
	return true, tile.Kind == battleengine.CellBreakable
}

// safe is 0x005B26AF.
func (w *bossWorld) safe(c battleengine.Cell) bool {
	present, _ := w.element(c)
	return w.inside(c) && w.danger(c) == 0 && !w.bombs[c] && !present
}

// edge is 0x005DFE32 with mask 0x1F and destroyCost 20.
func (w *bossWorld) edge(to battleengine.Cell, blockDanger, blockBox bool) (int, byte) {
	var flags byte
	if w.bombs[to] {
		flags |= 1
	}
	if present, breakable := w.element(to); present {
		if breakable {
			flags |= 2
		} else {
			flags |= 1
		}
	}
	switch w.danger(to) {
	case 1:
		flags |= 4
	case 2:
		flags |= 1
	}
	cost := 0
	if flags&1 != 0 {
		cost = 9999
	}
	if flags&2 != 0 {
		if blockBox {
			cost += 9999
		} else {
			cost += 40
		}
	}
	if flags&4 != 0 {
		if blockDanger {
			cost += 9999
		} else {
			cost += 20
		}
	}
	cost = min(max(cost, 1), 9999)
	kind := byte(3)
	if flags&4 != 0 {
		kind = 2
	} else if flags&2 != 0 {
		kind = 1
	}
	return cost, kind
}

type wfNode struct {
	cell   battleengine.Cell
	g, f   int
	dir    int
	kind   byte
	parent *wfNode
	next   *wfNode
}

// find is WFPathFinder 0x005E006A: best-first over an f-sorted list, a new
// node goes after existing ones of equal f, and the search succeeds as soon
// as the goal is generated. It returns start..goal with dir pointing to the
// next node, and the goal's g.
func (w *bossWorld) find(start, goal battleengine.Cell, maxF, maxOpen int, blockDanger, blockBox bool) ([]pathNode, int, bool) {
	h := func(c battleengine.Cell) int {
		return int(abs32(int32(c.Col-goal.Col)) + abs32(int32(c.Row-goal.Row)))
	}
	root := &wfNode{cell: start, f: h(start), dir: bossDirStop}
	head := root
	count := 1
	for cur := root; ; {
		for d := 0; d < 4; d++ {
			next := bossStep(cur.cell, d)
			if !w.inside(next) {
				continue
			}
			cost, kind := w.edge(next, blockDanger, blockBox)
			if cost >= 9999 {
				continue
			}
			var prev *wfNode
			old := head
			for old != nil && old.cell != next {
				prev, old = old, old.next
			}
			if old != nil {
				if old.g <= cur.g+cost || old == cur {
					continue
				}
				if prev == nil {
					head = old.next
				} else {
					prev.next = old.next
				}
				count--
			}
			n := &wfNode{cell: next, g: cur.g + cost, dir: d, kind: kind, parent: cur}
			n.f = n.g + h(next)
			var before *wfNode
			for at := cur; at != nil && at.f <= n.f; at = at.next {
				before = at
			}
			if before == nil {
				n.next, head = head, n
			} else {
				n.next, before.next = before.next, n
			}
			if next == goal {
				return extractPath(n), n.g, true
			}
			count++
		}
		cur = cur.next
		if cur == nil || cur.f >= maxF || count >= maxOpen {
			return nil, 0, false
		}
	}
}

// extractPath is 0x005DFF8D: [i].dir is the step from item i to item i+1.
func extractPath(goal *wfNode) []pathNode {
	var nodes []*wfNode
	for n := goal; n != nil; n = n.parent {
		nodes = append(nodes, n)
	}
	out := make([]pathNode, len(nodes))
	for i := range nodes {
		n := nodes[len(nodes)-1-i]
		out[i] = pathNode{cell: n.cell, kind: n.kind, dir: bossDirStop}
		if i > 0 {
			out[i-1].dir = n.dir
		}
	}
	out[0].kind = 0
	return out
}

// pathTo is 0x005B1704. With truncate the stored path stops before the first
// danger or box item; a box truncation marks the current cell 4.
func (ai *bossGeneral) pathTo(w *bossWorld, from, to battleengine.Cell, truncate, blockDanger, blockBox bool) (int, bool) {
	ai.path = nil
	nodes, _, ok := w.find(from, to, pathMaxF, pathMaxOpen, blockDanger, blockBox)
	if !ok || len(nodes) > pathMaxOpen {
		return -1, false
	}
	length := len(nodes)
	cut, box := len(nodes), false
	if truncate {
		for i, n := range nodes {
			if n.kind == 2 || n.kind == 1 {
				cut, box = i, n.kind == 1
				break
			}
		}
	}
	ai.path = append([]pathNode(nil), nodes[:cut]...)
	if len(ai.path) == 0 {
		return length, false
	}
	ai.path[0].kind = 0
	if box {
		ai.path[0].kind = 4
	}
	return length, true
}

func (ai *bossGeneral) lastDir() int {
	if len(ai.path) == 0 {
		return bossDirStop
	}
	return ai.path[0].dir
}

func (ai *bossGeneral) tick(match *onlineMatch, b *bossActor, now uint32) {
	if !ai.inited {
		ai.inited = true
		ai.gate, ai.last = now, [4]uint32{now, now, now, now}
		b.navigator.walking, b.navigator.direction = b.moving, b.facing
	}
	w := match.bossWorld(now)
	movementMode := byte(1)
	if b.navigator.walking {
		movementMode = 2
	}
	match.boss.moveCommand.generalFrame(now, b.birth.mode, movementMode, func() byte { return byte(ai.decide(w, b)) }, func(direction byte) {
		if direction == bossDirStop {
			b.path = nil
			b.navigator.walking = false
		} else {
			b.path = []battleengine.Cell{bossStep(b.pos.Cell(), int(direction))}
			b.navigator.walking, b.navigator.direction = true, direction
		}
	}, func() { ai.bombTick(w, b) })
	if b.transformed() {
		match.bossMove(b, now)
	} else {
		match.moveDefaultNPC(&b.boxNPCActor, now-b.lastTick)
		// Retire the old centre target as the continuous command passes it.
		// The special-capability fallback must not later walk back to it.
		if len(b.path) > 0 {
			target := battleengine.PositionAtCellCenter(b.path[0])
			reached := false
			switch b.facing {
			case 0:
				reached = b.pos.Y == target.Y && b.pos.X >= target.X
			case 1:
				reached = b.pos.X == target.X && b.pos.Y <= target.Y
			case 2:
				reached = b.pos.Y == target.Y && b.pos.X <= target.X
			case 3:
				reached = b.pos.X == target.X && b.pos.Y >= target.Y
			}
			if reached {
				b.path = b.path[1:]
			}
		}
	}
}

// decide is 0x005DF058.
func (ai *bossGeneral) decide(w *bossWorld, b *bossActor) int {
	here := b.pos.Cell()
	if ai.chaseTrapped(w, here) {
		return ai.lastDir()
	}
	if w.danger(here) != 0 {
		return ai.flee(w, here)
	}
	if ai.chaseAlive(w, here) {
		return ai.lastDir()
	}
	if ai.collectItem(w, here) {
		return ai.lastDir()
	}
	if r := b.role; r == pirateFirstMate || r == pirateCaptainHook {
		ai.pirateSkills(w, b)
	} else if r == 24 || r == 28 || r > 28 {
		ai.genericSkills(w, b)
	}
	if ai.followPath(&b.boxNPCActor, tickMS, func(x, y int32, facing byte) bool {
		return w.match.engine.RuleNPCPositionPassable(b.id, battleengine.Position{X: x, Y: y}, battleDirection(facing))
	}) && !ai.bombNearestBox(w, here) {
		return bossDirStop
	}
	return ai.lastDir()
}

func (w *bossWorld) players(keep func(battleengine.Actor) bool) []battleengine.Actor {
	var out []battleengine.Actor
	for _, a := range w.match.engine.Actors() {
		if keep(a) {
			out = append(out, a)
		}
	}
	return out
}

// chaseTrapped is 0x005B190A. The 10-cell check reads the length left by
// the loop's last call, not the best one (0x005B1AA5).
func (ai *bossGeneral) chaseTrapped(w *bossWorld, here battleengine.Cell) bool {
	targets := w.players(func(a battleengine.Actor) bool { return a.State == battleengine.ActorTrapped })
	best, bestLen, last := -1, 0, -1
	for i, a := range targets {
		length, ok := ai.pathTo(w, here, a.Position.Cell(), true, false, true)
		last = length
		if ok && length > 0 && (best < 0 || length < bestLen) {
			best, bestLen = i, length
		}
	}
	if best < 0 || last > 10 || last <= 0 {
		ai.path = nil
		return false
	}
	_, ok := ai.pathTo(w, here, targets[best].Position.Cell(), true, false, true)
	return ok
}

// flee is 0x005B214D: a random reachable safe cell, else the fallback table
// with the original's inverted tests for directions 2 and 3.
func (ai *bossGeneral) flee(w *bossWorld, here battleengine.Cell) int {
	if ai.findSafeCell(w, here) {
		return ai.lastDir()
	}
	can := func(d int) bool { return w.match.npcOpen(bossStep(here, d), here) }
	switch {
	case can(0):
		return 0
	case can(1):
		return 1
	case !can(2):
		return 2
	case can(3):
		return bossDirStop
	}
	return 3
}

// ring enumerates (row±i, col±j) for d = 1..r, i, j < r, i+j <= d, in the
// original's order (duplicates included) until visit returns true.
func ring(here battleengine.Cell, r int, visit func(battleengine.Cell) bool) {
	for d := 1; d <= r; d++ {
		for i := 0; i < r; i++ {
			for j := 0; j < r; j++ {
				if i+j > d {
					continue
				}
				for _, s := range [4][2]int16{{1, 1}, {-1, 1}, {1, -1}, {-1, -1}} {
					if visit(battleengine.Cell{Row: here.Row + s[0]*int16(i), Col: here.Col + s[1]*int16(j)}) {
						return
					}
				}
			}
		}
	}
}

// findSafeCell is 0x005B22FC with R = 10.
func (ai *bossGeneral) findSafeCell(w *bossWorld, here battleengine.Cell) bool {
	var ok bool
	ai.path, ok = w.safePlan(here, 10, w.match.boss.rng.Intn)
	return ok
}

// safePlan is 005B22FC: preserve duplicate candidates and stop after R+1
// reachable entries. The caller supplies its arbitrator-local RNG.
func (w *bossWorld) safePlan(here battleengine.Cell, r int, choose func(int) int) ([]pathNode, bool) {
	var candidates []battleengine.Cell
	ring(here, r, func(c battleengine.Cell) bool {
		if w.safe(c) {
			candidates = append(candidates, c)
		}
		return false
	})
	var reachable []battleengine.Cell
	for _, c := range candidates {
		if _, _, ok := w.find(here, c, pathMaxF, pathMaxOpen, false, true); ok {
			reachable = append(reachable, c)
			if len(reachable) > r {
				break
			}
		}
	}
	if len(reachable) == 0 {
		return nil, false
	}
	path, _, ok := w.find(here, reachable[choose(len(reachable))], pathMaxF, pathMaxOpen, false, true)
	return path, ok
}

// chaseAlive is 0x005B1BB8: pick the nearest player allowing danger and
// boxes, then path to it without crossing danger. Reaching the target's cell
// marks the farthest item 4.
func (ai *bossGeneral) chaseAlive(w *bossWorld, here battleengine.Cell) bool {
	targets := w.players(func(a battleengine.Actor) bool { return a.State != battleengine.ActorEliminated })
	best, bestLen := -1, 0
	for i, a := range targets {
		if length, ok := ai.pathTo(w, here, a.Position.Cell(), true, false, false); ok && length > 0 && (best < 0 || length < bestLen) {
			best, bestLen = i, length
		}
	}
	if best < 0 {
		ai.path = nil
		return false
	}
	goal := targets[best].Position.Cell()
	if _, ok := ai.pathTo(w, here, goal, true, true, false); !ok {
		return false
	}
	if far := len(ai.path) - 1; ai.path[far].cell == goal {
		ai.path[far].kind = 4
	}
	return true
}

// collectItem is 0x005B13F5: the first reachable cell with an item (not glue
// or trap) within radius 8, starting with the Boss's own cell.
func (ai *bossGeneral) collectItem(w *bossWorld, here battleengine.Cell) bool {
	items := map[battleengine.Cell]bool{}
	for _, p := range w.match.engine.Pickups() {
		if p.State == battleengine.PickupAvailable {
			items[p.Cell] = true
		}
	}
	for _, f := range w.match.engine.FieldObjects() {
		delete(items, f.Cell)
	}
	found := false
	if items[here] {
		_, found = ai.pathTo(w, here, here, false, true, true)
	}
	ring(here, 8, func(c battleengine.Cell) bool {
		if found {
			return true
		}
		present, _ := w.element(c)
		if !items[c] || present {
			return false
		}
		_, found = ai.pathTo(w, here, c, false, true, true)
		return found
	})
	if !found {
		ai.path = nil
	}
	return found
}

// followPath uses the same 005B1EA6 state machine as default roaming NPCs.
func (ai *bossGeneral) followPath(n *boxNPCActor, dt uint32, canPass func(int32, int32, byte) bool) bool {
	native := n.nativePosition()
	position := npcFollowPosition{native.X, native.Y}
	path := npcPathFollower{nodes: ai.path, mismatches: ai.pathMismatches}
	fallback := path.follow(&position, n.facing, !n.navigator.walking, int(n.effectiveNPCSpeed()), dt, canPass)
	ai.path, ai.pathMismatches = path.nodes, path.mismatches
	n.motion = battleengine.NativeNPCPosition{X: position.x, Y: position.y}
	n.pos = n.motion.Pixels()
	return fallback
}

func (ai *bossGeneral) appendEscape(nodes []pathNode) {
	path := npcPathFollower{nodes: ai.path, mismatches: ai.pathMismatches}
	path.appendEscape(nodes)
	ai.path = path.nodes
}

// bombNearestBox is 0x005B1B15: the first breakable element in scan order
// it can walk towards; the path stops before the box, marked 4.
func (ai *bossGeneral) bombNearestBox(w *bossWorld, here battleengine.Cell) bool {
	grid := w.match.engine.Grid()
	for row := int16(0); row < int16(grid.Height); row++ {
		for col := int16(0); col < int16(grid.Width); col++ {
			c := battleengine.Cell{Row: row, Col: col}
			if _, breakable := w.element(c); !breakable {
				continue
			}
			if _, ok := ai.pathTo(w, here, c, true, true, false); ok {
				return true
			}
		}
	}
	ai.path = nil
	return false
}

// bombTick is 0x005DF0EB, every frame.
func (ai *bossGeneral) bombTick(w *bossWorld, b *bossActor) {
	here := b.pos.Cell()
	if !npcBubbleEligible(ai.path, func() bool { return w.playerInLine(here) }) {
		return
	}
	escape, ok := ai.escapePlan(w, here)
	if !ok || !w.match.bossPlaceBubble(b, w.now) {
		return
	}
	ai.appendEscape(escape)
	b.path = nil
	if len(escape) > 1 {
		b.path = []battleengine.Cell{escape[1].cell}
	}
}

// playerInLine is 0x005B0406: any player in the same row or column within 2
// cells (walls and life are not checked).
func (w *bossWorld) playerInLine(here battleengine.Cell) bool {
	for _, a := range w.match.engine.Actors() {
		c := a.Position.Cell()
		if c.Row == here.Row && abs32(int32(c.Col-here.Col)) <= 2 || c.Col == here.Col && abs32(int32(c.Row-here.Row)) <= 2 {
			return true
		}
	}
	return false
}

// escapePlan is 0x005B0598: a safe cell off the Boss's row and column whose
// route costs at most 10.
func (ai *bossGeneral) escapePlan(w *bossWorld, here battleengine.Cell) ([]pathNode, bool) {
	var route []pathNode
	ring(here, 10, func(c battleengine.Cell) bool {
		if c.Row == here.Row || c.Col == here.Col || !w.safe(c) {
			return false
		}
		nodes, g, ok := w.find(here, c, pathMaxOpen, pathMaxOpen, true, true)
		if ok && g <= 10 {
			route = nodes
			return true
		}
		return false
	})
	return route, route != nil
}

// bossPlaceBubble is 0x005B08E0 and its 0x0FA3.
func (match *onlineMatch) bossPlaceBubble(b *bossActor, now uint32) bool {
	cell := b.pos.Cell()
	active := 0
	for _, bomb := range match.engine.Bombs() {
		if bomb.OwnerID == b.id {
			active++
		}
		if bomb.Cell == cell {
			return false
		}
	}
	if active >= int(b.capacity) || !match.npcOpen(cell, cell) {
		return false
	}
	wire := uint16(b.power) + 1
	placed, err := match.engine.PlaceRuleNPCBomb(b.id, cell, wire, 0, now)
	if err != nil {
		return false
	}
	match.bombs[placed.BombID] = wireBomb{owner: b.id, placedAt: now, cell: cell}
	body, _ := (game.PlayerUseBombEvent{PlayerID: b.id, ClientTime: now, BombID: 1, Row: byte(cell.Row), Column: byte(cell.Col), Power: wire}).MarshalNetworkBinary()
	match.queueNPC(&match.boss.outbox, b.id, game.PlayerUseBomb, now, body)
	match.mirror(game.PlayerUseBomb, body)
	match.logf("Boss %d places a bubble at %d,%d", b.id, cell.Row, cell.Col)
	return true
}

// genericSkills is 0x005B0D86 (skills 1..4): a 10 s master gate, then the
// list in order; a skill still cooling down ends the pass.
func (ai *bossGeneral) genericSkills(w *bossWorld, b *bossActor) {
	now := w.now
	if now-ai.gate < bossSkillGateMS {
		return
	}
	ai.gate = now
	match := w.match
	body := game.NPCUseSkillEvent{ObjectID: b.id, Time: now, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y)}
	for _, skill := range b.info.Skills {
		body.SkillID = uint16(skill)
		switch skill {
		case 1:
			if now-ai.last[1] < bossSkillOneMS {
				return
			}
			cells := match.bossSkillCells(bossGlueCells)
			if len(cells) < bossGlueCells {
				return
			}
			body.Items = skillItems(bossSceneGlue, cells)
			match.bossUseSkill(b, body)
			ai.last[1] = now
		case 2:
			// The petrifying wave replaces a player's control state; its
			// duration is not known yet (docs/boss-ai-re.md §11.7).
			return
		case 3:
			if now-ai.last[3] < bossSkillLongMS || b.transformed() {
				return
			}
			body.ObjectID = b.id
			match.bossUseSkill(b, body)
			ai.last[3] += bossSkillLongMS
		case 4:
			if now-ai.last[3] < bossSkillLongMS {
				return
			}
			cells := match.bossSkillCells(bossTrapCells)
			if len(cells) < bossTrapCells {
				return
			}
			body.Items = skillItems(bossSceneTrap, cells)
			match.bossUseSkill(b, body)
			ai.last[1] = now // the original updates skill 1's timer here
		}
	}
}

func skillItems(scene uint32, cells []battleengine.Cell) []game.NPCSkillItem {
	items := make([]game.NPCSkillItem, 0, len(cells))
	for _, c := range cells {
		items = append(items, game.NPCSkillItem{SceneID: scene, Row: byte(c.Row), Col: byte(c.Col)})
	}
	return items
}

// bossSkillCells is 0x005B8574 as the skills use it: free cells without an
// element, bubble, field object or item; players' cells are not excluded.
func (match *onlineMatch) bossSkillCells(n int) []battleengine.Cell {
	cells := match.bossFreeCells()
	for len(cells) > n {
		i := match.boss.rng.Intn(len(cells))
		cells = append(cells[:i], cells[i+1:]...)
	}
	return cells
}

func (match *onlineMatch) bossFreeCells() []battleengine.Cell {
	grid := match.engine.Grid()
	taken := map[battleengine.Cell]bool{}
	for _, bomb := range match.engine.Bombs() {
		taken[bomb.Cell] = true
	}
	for _, f := range match.engine.FieldObjects() {
		taken[f.Cell] = true
	}
	for _, p := range match.engine.Pickups() {
		if p.State == battleengine.PickupAvailable {
			taken[p.Cell] = true
		}
	}
	var cells []battleengine.Cell
	for row := int16(0); row < int16(grid.Height); row++ {
		for col := int16(0); col < int16(grid.Width); col++ {
			c := battleengine.Cell{Row: row, Col: col}
			if tile, ok := match.engine.TileAt(c); ok && tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied && !taken[c] {
				cells = append(cells, c)
			}
		}
	}
	return cells
}

// bossUseSkill sends 0x1169 and dispatches it locally, as the original does.
func (match *onlineMatch) bossUseSkill(b *bossActor, skill game.NPCUseSkillEvent) {
	body, err := skill.MarshalNetworkBinary()
	if err != nil {
		match.logf("Boss %d skill %d: %v", b.id, skill.SkillID, err)
		return
	}
	match.queueNPC(&match.boss.outbox, b.id, game.NotifyNPCUseSkill, skill.Time, body)
	match.mirror(game.NotifyNPCUseSkill, body)
	match.applyBossSkill(b, skill)
}

// bossSkillRemote applies the arbitrator's NOTIFY_NPCUSE_SKILL.
func (match *onlineMatch) bossSkillRemote(b *bossActor, event game.GameEvent) {
	skill, err := game.ParseNPCUseSkillEvent(event)
	if err != nil {
		match.logf("Boss %d skill: %v", b.id, err)
		return
	}
	match.applyBossSkill(b, skill)
}

type bossFlight struct {
	owner        uint16
	scene        uint32
	from, to     battleengine.Cell
	start, lands uint32
}

type bossFlightView struct {
	Scene   uint32 `json:"scene"`
	FromRow int16  `json:"from_row"`
	FromCol int16  `json:"from_col"`
	Row     int16  `json:"row"`
	Col     int16  `json:"col"`
	Start   uint32 `json:"start"`
	Lands   uint32 `json:"lands"`
}

// applyBossSkill is the 0x1169 receiver 0x005FDB18: skills 1 and 4 throw
// their scene objects from the Boss's cell to their cells.
func (match *onlineMatch) applyBossSkill(b *bossActor, skill game.NPCUseSkillEvent) {
	switch skill.SkillID {
	case 1, 4, 10:
		from := battleengine.Position{X: int32(skill.PosX), Y: int32(skill.PosY)}.Cell()
		for _, item := range skill.Items {
			to := battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}
			match.boss.flights = append(match.boss.flights, bossFlight{owner: b.id, scene: item.SceneID, from: from, to: to,
				start: skill.Time, lands: skill.Time + bossFlightMS(from, to)})
		}
		match.logf("Boss %d skill %d throws %d objects", b.id, skill.SkillID, len(skill.Items))
	case 3:
		// 0x5FDC57: only the arbitrator answers, by granting item 110 to the
		// Boss (0x0FAC with the +1000000 mark, answered as 0x0FAD at once).
		if match.isArbitrator() {
			now := match.engine.ElapsedMS()
			grant := game.PlayerItemEvent{PlayerID: b.id, ClientTime: now + bossGrantTimeMark, ItemID: bossTransformItem, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y)}
			if body, err := grant.MarshalNetworkBinary(); err == nil {
				match.queueNPC(&match.boss.outbox, b.id, game.NotifyPlayerGetItem, now, body)
				match.mirror(game.NotifyPlayerGetItem, body)
			}
			match.bossTransform(b, now)
		}
	case 5:
		match.boss.bubbleLock++
		match.logf("Boss %d locks the bubbles", b.id)
	case 8:
		match.boss.bubbleLock = 0
		match.logf("Boss %d unlocks the bubbles", b.id)
	case 6:
		match.pirateWave(b, skill)
	case 7, 9:
		// No receiver branch (0x005FE05D).
	case 12:
		// 0x005FDF84: one power-8 type-11 bubble per item, in place.
		for _, item := range skill.Items {
			cell := battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}
			placed, err := match.engine.PlaceRuleNPCBigBomb(b.id, cell, kickBossBigPower, skill.Time)
			if err != nil {
				match.logf("Boss %d 2x2 bubble: %v", b.id, err)
				continue
			}
			match.bombs[placed.BombID] = wireBomb{owner: b.id, placedAt: skill.Time, cell: cell}
			match.boss.events = append(match.boss.events, placed)
		}
		match.logf("Boss %d places %d 2x2 bubbles", b.id, len(skill.Items))
	case 11:
		// 0x005B5202(10000) on the Boss's +0x318, flash2, on every client.
		b.protectedUntil = max(b.protectedUntil, skill.Time+kickBossShieldMS)
		match.logf("Boss %d shields itself", b.id)
	default:
		match.logf("Boss %d skill %d is not played yet", b.id, skill.SkillID)
	}
}

// bossFlightMS is the straight flight time between two cell centres.
func bossFlightMS(from, to battleengine.Cell) uint32 {
	dx, dy := float64(to.Col-from.Col)*battleengine.CellSizePixels, float64(to.Row-from.Row)*battleengine.CellSizePixels
	return uint32(math.Sqrt(dx*dx+dy*dy)*1000/bossFlightPixelsPerSecond + 0.5)
}

// landBossFlights installs thrown objects whose flight has ended.
func (match *onlineMatch) landBossFlights(now uint32) {
	rule := match.boss
	kept := rule.flights[:0]
	for _, f := range rule.flights {
		if now < f.lands {
			kept = append(kept, f)
			continue
		}
		if f.scene == bossSceneGlue || f.scene == bossSceneTrap {
			if err := match.engine.InstallRuleNPCFieldObjects(f.owner, uint8(f.scene), []battleengine.Cell{f.to}); err != nil {
				match.logf("Boss %d object %d: %v", f.owner, f.scene, err)
			}
		}
	}
	rule.flights = kept
}

// bossWander is the stand-in for AI types without a port (0, 1, 2, 4): it
// escapes danger, otherwise walks to a random open neighbour every 500 ms.
type bossWander struct {
	next uint32
}

func (ai *bossWander) tick(match *onlineMatch, b *bossActor, now uint32) {
	if now >= ai.next && len(b.path) == 0 {
		ai.next = now + 500
		danger, _ := match.engine.DangerTimeline(4000)
		if _, bad := danger.ImpactAt(b.pos.Cell()); bad {
			b.path = match.npcPath(&b.boxNPCActor, danger, 6)
		} else {
			var dirs []byte
			for d := byte(0); d < 4; d++ {
				if match.npcOpen(boxStep(b.pos.Cell(), d), b.pos.Cell()) {
					dirs = append(dirs, d)
				}
			}
			if len(dirs) > 0 {
				b.path = []battleengine.Cell{boxStep(b.pos.Cell(), dirs[match.boss.rng.Intn(len(dirs))])}
			}
		}
	}
	match.bossMove(b, now)
}
