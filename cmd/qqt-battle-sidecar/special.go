package main

// Special map elements of the item-field maps and map 820:
// docs/special-elements-re.md (explode/solid stones, ice totems, halos) and
// docs/transdoor-strip-re.md (teleporters). The engine keeps the scene part
// (Rules.NativeSpecialElements); this file runs the per-client timers, the
// local player's triggers and the 0x1176 messages.

import (
	"math/rand"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	elementTransDoorA = 17026
	elementTransDoorB = 17029

	totemFirstMS    = 4000 // 0x0061AA52: +0x3C starts at 4000
	totemPeriodMS   = 4000
	totemActiveMS   = 1000
	totemRestoreMS  = 2000 // each -3 comes back after 2000 ms
	totemSpeedDelta = -3
	haloCooldownMS  = 20000
	haloBonusMS     = 15000
	haloBonus       = 8
	doorFirstMS     = 3000 // the layer manager's +0x48 starts at 3000
	doorShuffleMS   = 30000
	doorTransitMS   = 2000
	// The original repeats requests every frame until answered; one per
	// 100 ms keeps the same effect without flooding the relay.
	specialRequestMS = 100
)

type totemState struct {
	cell        battleengine.Cell
	nextAt      uint32 // activation due (the arbitrator sends 0x1176/0x10)
	activeSince uint32
	activeUntil uint32
}

type haloState struct {
	cell          battleengine.Cell
	cooldownUntil uint32
	bonus         map[uint16]uint32 // player -> end of its +8 bonus
	shownSince    uint32
}

type doorState struct {
	cell   battleengine.Cell
	target int // index into doors; -1 before the first shuffle
}

type doorTransit struct {
	door  int
	until uint32
	exit  bool // the second 2000 ms, on the exit door
}

type doorEffect struct {
	player uint16
	since  uint32
}

type specialRule struct {
	layers       mapdata.MapLayers
	totems       []*totemState
	totemRestore []uint32 // due times of the local player's -3 entries
	halos        []*haloState
	doors        []doorState
	shuffleAt    uint32
	transit      *doorTransit
	effects      []doorEffect
	rng          *rand.Rand
	requestedAt  uint32
	haloAskedAt  uint32
}

type specialTotemView struct {
	Row   int16  `json:"row"`
	Col   int16  `json:"col"`
	Since uint32 `json:"since"`
}

type specialEffectView struct {
	Player uint16 `json:"player"`
	Since  uint32 `json:"since"`
}

type specialView struct {
	Totems  []specialTotemView  `json:"totems,omitempty"`
	Doors   []cell              `json:"doors,omitempty"`
	Effects []specialEffectView `json:"effects,omitempty"`
}

// stripDirection is 0x005CB4B7's strip type per element ID.
var stripDirection = map[int32]battleengine.Direction{
	17121: battleengine.DirectionUp, 17122: battleengine.DirectionUp, 17131: battleengine.DirectionUp,
	17123: battleengine.DirectionDown, 17124: battleengine.DirectionDown, 17130: battleengine.DirectionDown,
	17125: battleengine.DirectionRight, 17126: battleengine.DirectionRight, 17132: battleengine.DirectionRight,
	17127: battleengine.DirectionLeft, 17128: battleengine.DirectionLeft, 17133: battleengine.DirectionLeft,
}

// installConveyors hands the floor-layer strips to the engine.
func (rule *specialRule) installConveyors(engine *battleengine.Engine) {
	for row := 0; row < rule.layers.Height; row++ {
		for col := 0; col < rule.layers.Width; col++ {
			if d, strip := stripDirection[rule.layers.Element(2, row, col)]; strip {
				_ = engine.SetConveyor(battleengine.Cell{Row: int16(row), Col: int16(col)}, d)
			}
		}
	}
}

// newSpecialRule scans the map; nil when it has none of these elements.
func newSpecialRule(layers mapdata.MapLayers, seed int64) *specialRule {
	rule := &specialRule{layers: layers, shuffleAt: doorFirstMS, rng: rand.New(rand.NewSource(seed))}
	found := false
	for row := 0; row < layers.Height; row++ {
		for col := 0; col < layers.Width; col++ {
			at := battleengine.Cell{Row: int16(row), Col: int16(col)}
			switch layers.Element(1, row, col) {
			case battleengine.ElementTotemIce:
				rule.totems = append(rule.totems, &totemState{cell: at, nextAt: totemFirstMS})
			case battleengine.ElementHaloExcite:
				rule.halos = append(rule.halos, &haloState{cell: at, cooldownUntil: haloCooldownMS, bonus: map[uint16]uint32{}})
			case elementTransDoorA, elementTransDoorB:
				// Registered in row-major order (0x005D8A77 updates rows
				// outer, columns inner), which the shuffle depends on.
				rule.doors = append(rule.doors, doorState{cell: at, target: -1})
			case battleengine.ElementExplodeStone, battleengine.ElementSolidStone:
				found = true
			}
			if _, strip := stripDirection[layers.Element(2, row, col)]; strip {
				found = true
			}
		}
	}
	if !found && len(rule.totems) == 0 && len(rule.halos) == 0 && len(rule.doors) == 0 {
		return nil
	}
	return rule
}

// doorLCG is the CRT rand of 0x004522F0 drawn three times (0x005D927B).
func doorLCG(seed uint32) uint32 {
	s := seed*0x41C64E6D + 0x3039
	a := (s >> 16) % 0x800
	s = s*0x41C64E6D + 0x3039
	a = a<<10 ^ (s>>16)%0x400
	s = s*0x41C64E6D + 0x3039
	return a<<10 ^ (s>>16)%0x400
}

// shuffleDoors is 0x005D927B: one random value picks pairs from the
// registration order; an odd door left over points one way to doors[r%n].
func (rule *specialRule) shuffleDoors(seed uint32) {
	n := len(rule.doors)
	if n == 0 {
		return
	}
	r := doorLCG(seed)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	pop := func() int {
		i := int(r % uint32(len(idx)))
		v := idx[i]
		idx = append(idx[:i], idx[i+1:]...)
		return v
	}
	for len(idx) > 1 {
		a := pop()
		b := pop()
		rule.doors[a].target, rule.doors[b].target = b, a
	}
	if len(idx) == 1 {
		rule.doors[idx[0]].target = int(r % uint32(n))
	}
}

func (rule *specialRule) doorAt(c battleengine.Cell) int {
	for i, d := range rule.doors {
		if d.cell == c {
			return i
		}
	}
	return -1
}

func inThree(a, b battleengine.Cell) bool {
	return abs32(int32(a.Row-b.Row)) <= 1 && abs32(int32(a.Col-b.Col)) <= 1
}

func facingCell(c battleengine.Cell, d battleengine.Direction) battleengine.Cell {
	switch d {
	case battleengine.DirectionRight:
		c.Col++
	case battleengine.DirectionUp:
		c.Row--
	case battleengine.DirectionLeft:
		c.Col--
	case battleengine.DirectionDown:
		c.Row++
	}
	return c
}

func (match *onlineMatch) sendMapElement(event game.MapElementEvent) {
	body, err := event.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.NotifyMapElement, event.Time, event.Time, body)
	match.mirror(game.NotifyMapElement, body)
}

func cellEvent(id, player uint16, now uint32, src, dst battleengine.Cell) game.MapElementEvent {
	return game.MapElementEvent{EventID: id, PlayerID: player, Time: now, SrcRow: uint16(src.Row), SrcCol: uint16(src.Col), DestRow: uint16(dst.Row), DestCol: uint16(dst.Col)}
}

// specialInput holds the local player's keys while a teleporter has it
// (vt+0x88 0x005A544A releases the direction keys and turns input off).
func (match *onlineMatch) specialInput(action *battleengine.Action) {
	if match.special != nil && match.special.transit != nil {
		action.Move, action.PlaceBomb = battleengine.DirectionNone, false
	}
}

// specialTick runs after the engine tick; before is the local actor as the
// tick began and moved whether the player was walking.
func (match *onlineMatch) specialTick(before battleengine.Actor, moved bool) []battleengine.Event {
	rule := match.special
	if rule == nil {
		return nil
	}
	now := match.engine.ElapsedMS()
	arbitrator := match.isArbitrator()
	var events []battleengine.Event
	local, ok := actorByID(match.engine.Actors(), match.local)
	alive := ok && local.State != battleengine.ActorEliminated
	here := local.Position.Cell()

	// Strips push the local player standing on them (event 6).
	if ok {
		match.engine.Convey(match.local)
	}
	// Explode stones (0x00619EE1): the local player inside a broken stone's
	// 3x3 dies, trapped or not.
	if alive && match.engine.ElementBlastCovers(here) {
		if arbitrator {
			events = append(events, match.stoneKill(match.local, now)...)
		} else if now-rule.requestedAt >= specialRequestMS || rule.requestedAt == 0 {
			rule.requestedAt = now
			match.sendMapElement(cellEvent(game.MapElementStoneKill, match.local, now, here, here))
		}
	}

	// Ice totems (0x0061AAF4): the arbitrator times each activation.
	for _, t := range rule.totems {
		if arbitrator && now >= t.nextAt && now >= t.activeUntil {
			match.sendMapElement(cellEvent(game.MapElementTotemActive, match.local, now, t.cell, t.cell))
			match.activateTotem(t, now)
		}
		if now < t.activeUntil && alive && inThree(here, t.cell) {
			// 0x0061AFE1: every frame in range, speed bonus -3 for 2000 ms.
			if err := match.engine.AdjustElementBonus(match.local, totemSpeedDelta, 0, 0); err == nil {
				rule.totemRestore = append(rule.totemRestore, now+totemRestoreMS)
			}
		}
	}
	kept := rule.totemRestore[:0]
	for _, due := range rule.totemRestore {
		if now >= due {
			_ = match.engine.AdjustElementBonus(match.local, -totemSpeedDelta, 0, 0)
		} else {
			kept = append(kept, due)
		}
	}
	rule.totemRestore = kept

	// Halos (0x00614534): pushing against one while walking.
	for _, h := range rule.halos {
		for player, until := range h.bonus {
			if now >= until {
				_ = match.engine.AdjustElementBonus(player, -haloBonus, -haloBonus, -haloBonus)
				delete(h.bonus, player)
			}
		}
		if !alive || !moved || here != before.Position.Cell() || facingCell(here, local.Facing) != h.cell || now < h.cooldownUntil {
			continue
		}
		if arbitrator {
			match.sendMapElement(cellEvent(game.MapElementHaloGrant, match.local, now, h.cell, h.cell))
			match.grantHalo(h, match.local, now)
		} else if now-rule.haloAskedAt >= specialRequestMS || rule.haloAskedAt == 0 {
			rule.haloAskedAt = now
			match.sendMapElement(cellEvent(game.MapElementHaloRequest, match.local, now, h.cell, h.cell))
		}
	}

	// Teleporters: the arbitrator reshuffles 3 s in and then every 30 s.
	if len(rule.doors) > 0 && now >= rule.shuffleAt {
		rule.shuffleAt = now + doorShuffleMS
		if arbitrator {
			seed := uint16(rule.rng.Intn(0x8000))
			match.sendMapElement(game.MapElementEvent{EventID: game.MapElementDoorShuffle, PlayerID: match.local, Time: now, DestCol: seed})
			rule.shuffleDoors(uint32(seed))
		}
	}
	if t := rule.transit; t != nil && now >= t.until {
		rule.transit = nil
		if !t.exit {
			if exit := rule.doors[t.door].target; exit >= 0 {
				from, to := rule.doors[t.door].cell, rule.doors[exit].cell
				match.sendMapElement(cellEvent(game.MapElementDoorTransfer, match.local, now, from, to))
				match.doorTransfer(match.local, to, now)
				rule.transit = &doorTransit{door: exit, until: now + doorTransitMS, exit: true}
			}
		}
	} else if alive && moved && rule.transit == nil && here != before.Position.Cell() {
		// 0x0060D2CD event 4: the centre cell changed while walking.
		if door := rule.doorAt(here); door >= 0 {
			match.sendMapElement(cellEvent(game.MapElementDoorEnter, match.local, now, here, here))
			match.doorEnter(match.local, here, now)
			rule.transit = &doorTransit{door: door, until: now + doorTransitMS}
		}
	}
	keptEffects := rule.effects[:0]
	for _, e := range rule.effects {
		if now-e.since < doorTransitMS {
			keptEffects = append(keptEffects, e)
		}
	}
	rule.effects = keptEffects
	return events
}

func (match *onlineMatch) activateTotem(t *totemState, now uint32) {
	if now < t.activeUntil {
		return // 0x0061AB88 ignores 0x10 while active
	}
	t.activeSince, t.activeUntil, t.nextAt = now, now+totemActiveMS, now+totemPeriodMS
}

// grantHalo is event 9: no cooldown check, a bonus already running is
// only extended.
func (match *onlineMatch) grantHalo(h *haloState, player uint16, now uint32) {
	if _, running := h.bonus[player]; !running {
		if err := match.engine.AdjustElementBonus(player, haloBonus, haloBonus, haloBonus); err != nil {
			return
		}
	}
	h.bonus[player] = now + haloBonusMS
	h.cooldownUntil, h.shownSince = now+haloCooldownMS, now
	match.logf("halo at %d,%d grants player %d", h.cell.Row, h.cell.Col, player)
}

func (match *onlineMatch) doorEnter(player uint16, door battleengine.Cell, now uint32) {
	_ = match.engine.ApplyVerifiedMovementCheckpoint(player, battleengine.PositionAtCellCenter(door))
	match.special.effects = append(match.special.effects, doorEffect{player: player, since: now})
}

func (match *onlineMatch) doorTransfer(player uint16, exit battleengine.Cell, now uint32) {
	_ = match.engine.ApplyVerifiedMovementCheckpoint(player, battleengine.PositionAtCellCenter(exit))
	match.special.effects = append(match.special.effects, doorEffect{player: player, since: now})
	match.logf("player %d teleports to %d,%d", player, exit.Row, exit.Col)
}

// stoneKill is the arbitrator's 0xFA7 for an explode-stone death.
func (match *onlineMatch) stoneKill(player uint16, now uint32) []battleengine.Event {
	actor, ok := actorByID(match.engine.Actors(), player)
	if !ok || actor.State == battleengine.ActorEliminated {
		return nil
	}
	events, err := match.engine.EliminateByMapElement(player)
	if err != nil {
		match.logf("explode-stone kill of %d: %v", player, err)
		return nil
	}
	items := []game.GameItem{}
	for _, event := range events {
		if event.Kind == battleengine.EventPickupDropped {
			items = append(items, game.GameItem{ItemID: event.SceneID, Row: byte(event.Cell.Row), Col: byte(event.Cell.Col)})
		}
	}
	match.forgetPickups(events)
	body, err := (game.PlayerDeathEvent{PlayerID: player, ClientTime: now, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y), Items: items}).MarshalNetworkBinary()
	if err == nil {
		match.queue(game.NotifyPlayerDieEvent, now, now, body)
		match.mirror(game.NotifyPlayerDieEvent, body)
	}
	match.logf("explode stone kills player %d", player)
	return events
}

// specialRemote applies a peer's 0x1176 (receiver 0x005FD4E6).
func (match *onlineMatch) specialRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	rule := match.special
	if rule == nil || event.Schema != game.NotifyMapElement {
		return nil, false
	}
	e, err := game.ParseMapElementEvent(event)
	if err != nil {
		match.logf("NOTIFY_EVENT_MAP_ELEM: %v", err)
		return nil, true
	}
	now := match.engine.ElapsedMS()
	dst := battleengine.Cell{Row: int16(e.DestRow), Col: int16(e.DestCol)}
	arbitrator := match.isArbitrator()
	switch e.EventID {
	case game.MapElementStoneKill:
		if arbitrator && e.PlayerID == sender {
			return match.stoneKill(e.PlayerID, now), true
		}
	case game.MapElementTotemActive:
		if sender != match.arbitrator {
			break
		}
		for _, t := range rule.totems {
			if t.cell == dst {
				match.activateTotem(t, now)
			}
		}
	case game.MapElementHaloRequest, game.MapElementHaloGrant:
		for _, h := range rule.halos {
			if h.cell != dst {
				continue
			}
			if e.EventID == game.MapElementHaloRequest {
				if arbitrator && now >= h.cooldownUntil {
					match.sendMapElement(cellEvent(game.MapElementHaloGrant, e.PlayerID, now, h.cell, h.cell))
					match.grantHalo(h, e.PlayerID, now)
				}
			} else if sender == match.arbitrator {
				match.grantHalo(h, e.PlayerID, now)
			}
		}
	case game.MapElementDoorShuffle:
		if sender == match.arbitrator {
			rule.shuffleDoors(uint32(int32(int16(e.DestRow))*0x10000 + int32(int16(e.DestCol))))
		}
	case game.MapElementDoorEnter:
		if e.PlayerID != match.local {
			match.doorEnter(e.PlayerID, dst, now)
		}
	case game.MapElementDoorTransfer:
		if e.PlayerID != match.local {
			match.doorTransfer(e.PlayerID, dst, now)
		}
	}
	return nil, true
}

func (match *onlineMatch) specialView() *specialView {
	rule := match.special
	if rule == nil {
		return nil
	}
	now := match.engine.ElapsedMS()
	view := &specialView{}
	for _, t := range rule.totems {
		if now < t.activeUntil {
			view.Totems = append(view.Totems, specialTotemView{Row: t.cell.Row, Col: t.cell.Col, Since: t.activeSince})
		}
	}
	for _, d := range rule.doors {
		view.Doors = append(view.Doors, cell{Row: d.cell.Row, Col: d.cell.Col})
	}
	for _, e := range rule.effects {
		view.Effects = append(view.Effects, specialEffectView{Player: e.player, Since: e.since})
	}
	return view
}
