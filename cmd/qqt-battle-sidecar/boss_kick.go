package main

// AIType 5, the kick-bomb Bosses (Rooney 36 on map 701, Cristiano 35 on
// 702; docs/boss-kickbomb-re.md). The controller 0x005B0C35 never places a
// bubble: it runs skills 10..12 (0x005B4947), chases trapped players, flees
// burning cells, and by the time since the last airdrop batch either goes to
// kick bubbles at players (first 2 s), collects items / wanders (4.5..7.5 s)
// or stands still. It kicks with the players' kick command (0x0060CF49): when
// it walks into a cell holding one landed ordinary bubble, the arbitrator
// sends the kick under the Boss's ID. Its pathfinder (0x005E0351) is a plain
// A* that ignores danger, treats a single bubble as passable and never steps
// up into rows 0..3.
//
// Not played: skill 12's 2x2 bubbles (scene 11; the engine has no such
// bubble). Its 60 s cooldown still advances, so the cadence of 10/11 holds.

import (
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

const (
	kickBossSkillFromMS = 10_000
	kickBossCooldownMS  = 30_000
	kickBossBigMS       = 60_000
	kickBossShieldMS    = 10_000
	kickBossTopRow      = 4  // find5 never steps up into rows 0..3
	kickBossProbePixels = 22 // 0x0060CF49 looks 22 px ahead
	kickBossKickWindow  = 2000
	kickBossItemFrom    = 4500
	kickBossItemUntil   = 7500
)

type bossKick struct {
	counter int
	path    []pathNode
	// N+0x834.. cooldown cells: checked by list position, updated by skill
	// ID (10 -> 0, 11 -> 1, 12 -> 2), zeroed at creation for roles 35/36.
	cd [10]uint32
}

func (ai *bossKick) tick(match *onlineMatch, b *bossActor, now uint32) {
	w := match.bossWorld(now)
	if ai.counter <= bossDecideFrames {
		ai.counter++
	} else if d := ai.decide(w, b); d == bossDirStop {
		b.path = nil
	} else {
		ai.counter = 0
		b.path = []battleengine.Cell{bossStep(b.pos.Cell(), d)}
	}
	match.bossKickAhead(b, now)
	match.bossMove(b, now)
}

func (ai *bossKick) lastDir() int {
	if len(ai.path) == 0 {
		return bossDirStop
	}
	return ai.path[0].dir
}

// decide is 0x005B0C35.
func (ai *bossKick) decide(w *bossWorld, b *bossActor) int {
	here := b.pos.Cell()
	ai.skills(w, b)
	if !ai.followPath(here) {
		return bossDirStop // the original reads a stale byte here
	}
	if ai.chaseTrapped(w, here) {
		return ai.lastDir()
	}
	if w.burningSoon(here) {
		return ai.flee(w, here)
	}
	t := int64(1 << 40)
	if k := w.match.kick; k != nil {
		t = int64(w.now) - k.lastBatch
	}
	if t >= 0 && t < kickBossKickWindow {
		if ai.kickAtPlayer(w, here) {
			if len(ai.path) == 0 {
				return 0
			}
			return ai.lastDir()
		}
		if goal, ok := ai.nearBubble(w, here); ok {
			if nodes, ok := w.find5(here, goal); ok {
				ai.path = nodes
				return ai.lastDir()
			}
		}
		if ai.randomItem(w, here) {
			return ai.lastDir()
		}
	}
	if t >= kickBossItemUntil || t <= kickBossItemFrom {
		return bossDirStop
	}
	general := &bossGeneral{}
	if general.collectItem(w, here) {
		ai.path = general.path
		return ai.lastDir()
	}
	if ai.wander(w, here) {
		return ai.lastDir()
	}
	return bossDirStop
}

// burningSoon is danger == 2 as AIType 5 reads it: burning, or a bubble
// about to burst on the cell.
func (w *bossWorld) burningSoon(c battleengine.Cell) bool {
	if w.burning[c] {
		return true
	}
	at, bad := w.timeline.ImpactAt(c)
	return bad && at >= w.now && at-w.now <= 200
}

// passable5 is 0x005E08B0: no blocking element; zero or one ordinary bubble.
func (w *bossWorld) passable5(c battleengine.Cell) bool {
	tile, ok := w.match.engine.TileAt(c)
	if !ok || tile.Kind != battleengine.CellOpen {
		return false
	}
	return w.bubblesAt(c) <= 1
}

func (w *bossWorld) bubblesAt(c battleengine.Cell) int {
	n := 0
	for _, bomb := range w.match.engine.Bombs() {
		if bomb.Cell == c {
			n++
		}
	}
	return n
}

// find5 is the football pathfinder 0x005E0351: A* with unit cost, the open
// list in insertion order (the first of equal f wins), neighbours left,
// right, up (never into rows 0..3), down; it succeeds once the goal is open.
func (w *bossWorld) find5(start, goal battleengine.Cell) ([]pathNode, bool) {
	if goal.Col >= 15 || goal.Row >= 13 || start == goal {
		return nil, false
	}
	type node struct {
		g, f   int
		parent battleengine.Cell
		state  byte // 1 open, 2 closed
	}
	h := func(c battleengine.Cell) int { return int(abs32(int32(c.Col-goal.Col)) + abs32(int32(c.Row-goal.Row))) }
	nodes := map[battleengine.Cell]*node{start: {f: h(start), parent: battleengine.Cell{Row: -1, Col: -1}, state: 1}}
	open := []battleengine.Cell{start}
	for len(open) > 0 && len(nodes) < pathMaxOpen {
		best := 0
		for i, c := range open {
			if nodes[c].f < nodes[open[best]].f {
				best = i
			}
		}
		cur := open[best]
		if g, ok := nodes[goal]; ok && g.state == 1 {
			break
		}
		open = append(open[:best], open[best+1:]...)
		nodes[cur].state = 2
		for _, next := range [4]battleengine.Cell{{Row: cur.Row, Col: cur.Col - 1}, {Row: cur.Row, Col: cur.Col + 1}, {Row: cur.Row - 1, Col: cur.Col}, {Row: cur.Row + 1, Col: cur.Col}} {
			if next.Col < 0 || next.Col > 14 || next.Row > 12 || next.Row < cur.Row && next.Row < kickBossTopRow || !w.passable5(next) {
				continue
			}
			n, seen := nodes[next]
			switch {
			case !seen:
				g := nodes[cur].g + 1
				nodes[next] = &node{g: g, f: g + h(next), parent: cur, state: 1}
				open = append(open, next)
			case n.state == 1 && nodes[cur].g+1 < n.g:
				n.parent, n.g, n.f = cur, nodes[cur].g+1, nodes[cur].g+1+h(next)
			}
		}
	}
	g, ok := nodes[goal]
	if !ok || g.state != 1 {
		return nil, false
	}
	var cells []battleengine.Cell
	for c := goal; c != start; c = nodes[c].parent {
		cells = append(cells, c)
	}
	cells = append(cells, start)
	out := make([]pathNode, len(cells))
	for i := range cells {
		out[i] = pathNode{cell: cells[len(cells)-1-i], dir: bossDirStop}
		if i > 0 {
			out[i-1].dir = directionBetween(out[i-1].cell, out[i].cell)
		}
	}
	return out, true
}

func directionBetween(from, to battleengine.Cell) int {
	for d := 0; d < 4; d++ {
		if bossStep(from, d) == to {
			return d
		}
	}
	return 0
}

// followPath is 0x005B3F80 on whole cells: false when the Boss has left
// its path (the original clears it 60 px away).
func (ai *bossKick) followPath(here battleengine.Cell) bool {
	if len(ai.path) == 0 {
		return true
	}
	for len(ai.path) > 0 && ai.path[0].cell != here {
		ai.path = ai.path[1:]
	}
	if len(ai.path) == 0 {
		return false
	}
	if len(ai.path) == 1 {
		ai.path = nil
	}
	return true
}

// chaseTrapped is 0x005B3A87: the first trapped player it can reach, at
// any distance.
func (ai *bossKick) chaseTrapped(w *bossWorld, here battleengine.Cell) bool {
	for _, a := range w.match.engine.Actors() {
		if a.State != battleengine.ActorTrapped {
			continue
		}
		if nodes, ok := w.find5(here, a.Position.Cell()); ok {
			ai.path = nodes
			return true
		}
	}
	return false
}

// flee is 0x005B3BFD: a random safe cell, else the old path.
func (ai *bossKick) flee(w *bossWorld, here battleengine.Cell) int {
	saved := ai.path
	if !ai.wander(w, here) {
		ai.path = saved
	}
	if len(ai.path) == 0 {
		return 0
	}
	return ai.lastDir()
}

// wander is 0x005B3CC1: one try at a random safe cell in rows 3..11,
// columns 0..13.
func (ai *bossKick) wander(w *bossWorld, here battleengine.Cell) bool {
	var cand []battleengine.Cell
	for c := int16(0); c <= 13; c++ {
		for r := int16(3); r <= 11; r++ {
			cell := battleengine.Cell{Row: r, Col: c}
			if w.danger(cell) == 0 && w.passable5(cell) {
				cand = append(cand, cell)
			}
		}
	}
	if len(cand) == 0 {
		return false
	}
	nodes, ok := w.find5(here, cand[w.match.boss.rng.Intn(len(cand))])
	if ok {
		ai.path = nodes
	}
	return ok
}

// landedBubble is kickable() 0x005B4506: the cell's first bubble has landed.
func (w *bossWorld) landedBubble(c battleengine.Cell) (battleengine.Bomb, bool) {
	for _, bomb := range w.match.engine.Bombs() {
		if bomb.Cell == c {
			return bomb, bomb.FlightUntilMS <= w.now
		}
	}
	return battleengine.Bomb{}, false
}

func hasBubbleRow(c battleengine.Cell) bool {
	return c.Row >= 4 && c.Row <= 12 && c.Col >= 0 && c.Col <= 14
}

func wrapCell(c battleengine.Cell) battleengine.Cell {
	return battleengine.Cell{Row: (c.Row%13 + 13) % 13, Col: (c.Col%15 + 15) % 15}
}

// kickAtPlayer is 0x005B4561 with the original's mix-up: it checks the
// bubble k cells up/down/left/right but aims from the cell k cells
// left/right/up/down respectively (0x005B46BB).
func (ai *bossKick) kickAtPlayer(w *bossWorld, here battleengine.Cell) bool {
	checks := [4]struct {
		dir, aim int
		ok       func(k int16) bool
	}{
		{1, 2, func(k int16) bool { return here.Row-k >= 0 }},
		{3, 0, func(k int16) bool { return here.Row+k <= 12 }},
		{2, 1, func(k int16) bool { return here.Col-k >= 0 }},
		{0, 3, func(k int16) bool { return here.Col+k <= 14 }},
	}
	for k := int16(0); k <= 4; k++ {
		for _, c := range checks {
			if !c.ok(k) {
				continue
			}
			at := battleengine.Cell{Row: here.Row + bossDelta[c.dir][1]*k, Col: here.Col + bossDelta[c.dir][0]*k}
			if _, landed := w.landedBubble(at); !hasBubbleRow(at) || !landed {
				continue
			}
			if ai.aim(w, here, c.aim, k) {
				return true
			}
		}
	}
	return false
}

func (ai *bossKick) aim(w *bossWorld, here battleengine.Cell, d int, k int16) bool {
	dc, dr := bossDelta[d][0], bossDelta[d][1]
	bubble := wrapCell(battleengine.Cell{Row: here.Row + dr*k, Col: here.Col + dc*k})
	for m := int16(8); m >= 1; m-- {
		land := wrapCell(battleengine.Cell{Row: bubble.Row + dr*m, Col: bubble.Col + dc*m})
		if tile, ok := w.match.engine.TileAt(land); !ok || tile.Kind != battleengine.CellOpen || tile.MapElementOccupied {
			continue
		}
		bomb, present := w.landedBubble(bubble)
		if !present && w.bubblesAt(bubble) == 0 {
			continue
		}
		power := int16(bomb.Power) + 1
		for _, p := range w.match.engine.Actors() {
			if p.State != battleengine.ActorActive {
				continue
			}
			pc := p.Position.Cell()
			if pc.Col == land.Col && abs32(int32(pc.Row-land.Row)) <= int32(power) || pc.Row == land.Row && abs32(int32(pc.Col-land.Col)) <= int32(power) {
				if nodes, ok := w.find5(here, bubble); ok {
					ai.path = nodes
					return true
				}
			}
		}
	}
	return false
}

// nearBubble is 0x005B38D9: a landed bubble in line within 4 cells.
func (ai *bossKick) nearBubble(w *bossWorld, here battleengine.Cell) (battleengine.Cell, bool) {
	for k := int16(1); k <= 4; k++ {
		for _, c := range [4]battleengine.Cell{{Row: here.Row - k, Col: here.Col}, {Row: here.Row + k, Col: here.Col}, {Row: here.Row, Col: here.Col - k}, {Row: here.Row, Col: here.Col + k}} {
			if c.Row < 2 || !hasBubbleRow(c) {
				continue
			}
			if _, landed := w.landedBubble(c); landed {
				return c, true
			}
		}
	}
	return battleengine.Cell{}, false
}

// randomItem is 0x005B4155: three tries at a random item cell.
func (ai *bossKick) randomItem(w *bossWorld, here battleengine.Cell) bool {
	items := map[battleengine.Cell]bool{}
	for _, p := range w.match.engine.Pickups() {
		if p.State == battleengine.PickupAvailable {
			items[p.Cell] = true
		}
	}
	for _, f := range w.match.engine.FieldObjects() {
		delete(items, f.Cell)
	}
	var cand []battleengine.Cell
	for c := int16(0); c <= 14; c++ {
		for r := int16(0); r <= 12; r++ {
			cell := battleengine.Cell{Row: r, Col: c}
			if present, _ := w.element(cell); items[cell] && !present {
				cand = append(cand, cell)
			}
		}
	}
	if len(cand) == 0 {
		return false
	}
	for try := 0; try < 3; try++ {
		if nodes, ok := w.find5(here, cand[w.match.boss.rng.Intn(len(cand))]); ok {
			ai.path = nodes
			return true
		}
	}
	return false
}

// skills is 0x005B4947.
func (ai *bossKick) skills(w *bossWorld, b *bossActor) {
	now := w.now
	if now < kickBossSkillFromMS {
		return
	}
	match := w.match
	body := game.NPCUseSkillEvent{ObjectID: b.id, Time: now, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y)}
	for i, skill := range b.info.Skills {
		if i >= len(ai.cd) || now-ai.cd[i] < kickBossCooldownMS {
			return
		}
		body.SkillID = uint16(skill)
		switch skill {
		case 10:
			cells := match.bossSkillCells(bossGlueCells)
			if len(cells) < bossGlueCells {
				return
			}
			body.Items = skillItems(bossSceneGlue, cells)
			ai.cd[0] = now
			match.bossUseSkill(b, body)
		case 11:
			body.Items = nil
			ai.cd[1] = now
			match.bossUseSkill(b, body)
		case 12:
			if now-ai.cd[2] < kickBossBigMS {
				return
			}
			ai.cd[2] = now // the 2x2 bubbles themselves are not played
		}
	}
}

// bossKickAhead is the players' kick command 0x0060CF49 run for the Boss:
// moving, the point 22 px ahead in another cell holding exactly one landed
// ordinary bubble. The arbitrator echoes it as 0x139C at once (0x006040CF).
func (match *onlineMatch) bossKickAhead(b *bossActor, now uint32) {
	if len(b.path) == 0 {
		return
	}
	here := b.pos.Cell()
	d := directionBetween(here, b.path[0])
	if bossStep(here, d) != b.path[0] {
		return
	}
	probe := battleengine.Position{X: b.pos.X + int32(bossDelta[d][0])*kickBossProbePixels, Y: b.pos.Y + int32(bossDelta[d][1])*kickBossProbePixels}
	front := b.path[0]
	if probe.Cell() != front {
		return
	}
	var bubble battleengine.Bomb
	count := 0
	for _, bomb := range match.engine.Bombs() {
		if bomb.Cell == front {
			bubble, count = bomb, count+1
		}
	}
	if count != 1 || bubble.FlightUntilMS > now {
		return
	}
	known, ok := match.bombs[bubble.ID]
	if !ok {
		return
	}
	target, ok := match.engine.KickDestination(front, battleDirection(byte(d)))
	if !ok || target == front {
		return
	}
	accepted, err := match.runtime.AcceptNativeBombMovement(b.id, bubble.ID, front, target)
	if err != nil {
		match.logf("Boss %d kick: %v", b.id, err)
		return
	}
	match.moveWireBomb(bubble.ID, target)
	body, err := (game.MoveBombEvent{
		PlayerID: b.id, ClientTime: uint16(now),
		FromRow: uint16(front.Row), FromCol: uint16(front.Col), ToRow: uint16(target.Row), ToCol: uint16(target.Col),
		Direction: byte(d), BombPlayerID: known.owner, BombTime: known.placedAt, BombPower: bubble.Power + 1,
	}).MarshalNetworkBinary()
	if err == nil {
		match.queueNPC(&match.boss.outbox, b.id, game.NotifyPlayerMoveBomb, now, body)
		match.mirror(game.NotifyPlayerMoveBomb, body)
	}
	match.noteKick(match.engine, accepted, byte(d))
	match.boss.events = append(match.boss.events, accepted)
}
