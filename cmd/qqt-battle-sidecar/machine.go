package main

// Rule 7: Client.exe 007A4380 / 0060A627. The machine is a stationary,
// team-owned launcher. Free/Angry/Grace/Cool name its shot patterns, not NPC
// movement states. 006014B8 chooses a pattern from the firing avatar's role;
// 005C91A5 emits its delayed 0x115D batch. See README for native addresses.

import (
	"encoding/binary"
	"math"
	"math/rand"
	"strconv"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

type machineGate struct {
	anchor  battleengine.Cell
	side    int
	index   byte
	pattern uint16
	fired   uint32
}
type machineSalvo struct {
	due   uint32
	state game.MachineBombState
}
type machineCharge struct {
	count int
	last  uint32
}

type machineRule struct {
	finishing   bool // final durability loss observed; wait for server settlement
	charges     map[uint16]*machineCharge
	destroyed   [2]bool
	teams       [2]byte
	gates       []machineGate
	last        [2]uint32
	hits        [2]int
	rng         *rand.Rand
	pending     []machineSalvo
	flights     map[uint32]kickFlight
	seen        map[[3]uint32]bool
	requestedAt uint32
	supplyCells [2][]battleengine.Cell
	supplyLast  [2]uint32
}

func newMachineRule(entry mapdata.CompetitiveMap, begin game.GameBeginData) *machineRule {
	rule := &machineRule{charges: map[uint16]*machineCharge{}, teams: firstTwoTeams(begin), rng: rand.New(rand.NewSource(int64(begin.ItemSeed))), flights: map[uint32]kickFlight{}, seen: map[[3]uint32]bool{}}
	for _, cell := range entry.MachineSupplyCells {
		side := 0
		if cell.Col > 8 {
			side = 1
		} else if cell.Col >= 6 {
			continue
		}
		rule.supplyCells[side] = append(rule.supplyCells[side], battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)})
	}
	n := len(entry.ObjectiveCells) / 2
	for side := 0; side < 2; side++ {
		for i := 0; i < n; i++ {
			c := entry.ObjectiveCells[side*n+i]
			rule.gates = append(rule.gates, machineGate{anchor: battleengine.Cell{Row: int16(c.Row), Col: int16(c.Col)}, side: side, index: byte(i)})
		}
	}
	return rule
}
func (rule *machineRule) side(team byte) int {
	if team == rule.teams[0] {
		return 0
	}
	return 1
}
func (rule *machineRule) ready(side int, now uint32) bool {
	return !rule.destroyed[side] && int64(now)-int64(rule.last[side]) >= int64(max(0, 48-rule.hits[side])*1000)
}
func (gate machineGate) trigger() battleengine.Cell {
	c := gate.anchor
	if gate.side == 1 {
		c.Col++
	}
	return c
}
func (gate machineGate) approach() battleengine.Cell {
	c := gate.trigger()
	if gate.side == 0 {
		c.Col--
	} else {
		c.Col++
	}
	return c
}
func (rule *machineRule) gate(side int, index byte) *machineGate {
	for i := range rule.gates {
		g := &rule.gates[i]
		if g.side == side && g.index == index {
			return g
		}
	}
	return nil
}

func (rule *machineRule) pattern(role uint16, now uint32) uint16 {
	// 006014B8 uses (rand()%99)%20, i.e. 5/99, from 120 seconds.
	if now >= 120000 && rule.rng.Intn(99)%20 == 0 {
		return game.NotifyMachineCool
	}
	switch role {
	case 1, 6, 8, 9, 10, 11, 13:
		return game.NotifyMachineFree
	case 2:
		return game.NotifyMachineAngry
	default:
		return game.NotifyMachineGrace
	}
}
func machineTarget(cell battleengine.Cell, side, margin int) bool {
	lo, hi := int16(0), int16(5)
	if side == 0 {
		lo, hi = 9, 14
	}
	return cell.Row >= int16(margin) && cell.Row <= int16(12-margin) && cell.Col >= lo+int16(margin) && cell.Col <= hi-int16(margin)
}
func (rule *machineRule) bombs(engine *battleengine.Engine, side int, pattern uint16) []game.DispatchBomb {
	occupied := map[battleengine.Cell]bool{}
	for _, b := range engine.Bombs() {
		occupied[b.Cell] = true
	}
	for _, p := range engine.Pickups() {
		if p.State == battleengine.PickupAvailable {
			occupied[p.Cell] = true
		}
	}
	for _, f := range engine.FieldObjects() {
		occupied[f.Cell] = true
	}
	grid := engine.Grid()
	var free []battleengine.Cell
	for r := int16(0); r < int16(grid.Height); r++ {
		for c := int16(0); c < int16(grid.Width); c++ {
			at := battleengine.Cell{Row: r, Col: c}
			tile, _ := grid.Cell(at)
			if tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied && !occupied[at] {
				free = append(free, at)
			}
		}
	}
	rule.rng.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })
	limit := 100
	if pattern == game.NotifyMachineGrace {
		limit = 190
	}
	free = free[:min(limit, len(free))]
	var result []game.DispatchBomb
	add := func(c battleengine.Cell, style uint32, power byte) {
		result = append(result, game.DispatchBomb{SceneID: style, Prop: power, Row: byte(c.Row), Col: byte(c.Col)})
	}
	if pattern == game.NotifyMachineFree {
		for _, c := range free {
			if machineTarget(c, side, 0) {
				add(c, 1, 8)
				if len(result) == 10 {
					break
				}
			}
		}
		return result
	}
	margin := 2
	if pattern == game.NotifyMachineCool {
		margin = 1
	}
	for _, centre := range free {
		if !machineTarget(centre, side, margin) {
			continue
		}
		if pattern == game.NotifyMachineAngry || pattern == game.NotifyMachineCool {
			c := battleengine.Cell{Row: centre.Row - 1, Col: centre.Col - 1}
			style, power := uint32(13), byte(3)
			if pattern == game.NotifyMachineCool {
				style, power = 14, 15
			}
			add(c, style, power)
			return result
		}
		// 005E33B9: centre followed by the four diagonal corners at radii 1 and 2.
		for i := 0; i < min(9, len(free)); i++ {
			c := centre
			if i > 0 {
				d := int16((i + 3) / 4)
				switch i % 4 {
				case 1:
					c.Row -= d
					c.Col -= d
				case 2:
					c.Row -= d
					c.Col += d
				case 3:
					c.Row += d
					c.Col += d
				case 0:
					c.Row += d
					c.Col -= d
				}
			}
			if !machineTarget(c, side, 0) {
				continue
			}
			for _, f := range free {
				if f == c {
					add(c, 1, 8)
					break
				}
			}
		}
		return result
	}
	return result
}

func (match *onlineMatch) launchMachine(action *battleengine.Action) {
	rule := match.machine
	if rule == nil || !action.PlaceBomb {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok {
		return
	}
	side, now := rule.side(actor.TeamID), match.engine.ElapsedMS()
	if !rule.ready(side, now) || rule.requestedAt != 0 && now-rule.requestedAt < 1000 {
		return
	}
	for _, g := range rule.gates {
		if g.side != side || actor.Position.Cell() != g.trigger() {
			continue
		}
		action.PlaceBomb = false
		rule.requestedAt = now
		request := game.MachineLaunchEvent{MachineIndex: g.index, FirstTeam: byte(1 - side), RoleID: byte(actor.RoleID), PlayerID: actor.PlayerID, Time: now}
		body, _ := request.MarshalNetworkBinary()
		if match.isArbitrator() {
			match.arbitrateMachine(request)
		} else {
			match.request(game.RequestLaunchMachine, now, body)
		}
		match.mirror(game.RequestLaunchMachine, body)
		// 00618709 pushes the activating actor one cell away from the door.
		match.engine.ApplyVerifiedMovementCheckpoint(actor.PlayerID, battleengine.PositionAtCellCenter(g.approach()))
		return
	}
}
func (match *onlineMatch) arbitrateMachine(request game.MachineLaunchEvent) {
	rule := match.machine
	if rule == nil || !match.isArbitrator() {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), request.PlayerID)
	if !ok || actor.State != battleengine.ActorActive {
		return
	}
	side := rule.side(actor.TeamID)
	g := rule.gate(side, request.MachineIndex)
	if g == nil || request.FirstTeam != byte(1-side) || !rule.ready(side, request.Time) {
		return
	}
	pattern := rule.pattern(actor.RoleID, request.Time)
	state := game.MachineBombState{MachineIndex: g.index, PlayerID: actor.PlayerID, Time: request.Time, Bombs: rule.bombs(match.engine, side, pattern)}
	body, err := state.MarshalNetworkBinary(pattern)
	if err != nil {
		return
	}
	match.applyMachineState(pattern, state)
	match.queue(pattern, request.Time, request.Time, body)
	match.mirror(pattern, body)
}
func (match *onlineMatch) applyMachineState(pattern uint16, state game.MachineBombState) {
	rule := match.machine
	key := [3]uint32{uint32(pattern), uint32(state.PlayerID), state.Time}
	if rule.seen[key] {
		return
	}
	rule.seen[key] = true
	actor, ok := actorByID(match.engine.Actors(), state.PlayerID)
	if !ok {
		return
	}
	side := rule.side(actor.TeamID)
	g := rule.gate(side, state.MachineIndex)
	if g == nil {
		return
	}
	rule.last[side] = state.Time
	g.pattern, g.fired = pattern, state.Time
	match.logf("machine %d team %d pattern 0x%04X by %d: %d bubbles", g.index, actor.TeamID, pattern, actor.PlayerID, len(state.Bombs))
	delay := uint32(3000)
	if pattern == game.NotifyMachineGrace {
		delay = 2667
	}
	state.MachineIndex = 0
	rule.pending = append(rule.pending, machineSalvo{due: state.Time + delay, state: state})
}
func (match *onlineMatch) applyMachineMove(state game.MachineBombState) []battleengine.Event {
	rule := match.machine
	key := [3]uint32{game.NotifyMachineMove, uint32(state.PlayerID), state.Time}
	if rule.seen[key] {
		return nil
	}
	rule.seen[key] = true
	if !match.isArbitrator() {
		kept := rule.pending[:0]
		for _, p := range rule.pending {
			if p.state.PlayerID != state.PlayerID || p.state.Time != state.Time {
				kept = append(kept, p)
			}
		}
		rule.pending = kept
	}
	var events []battleengine.Event
	for _, b := range state.Bombs {
		c := battleengine.Cell{Row: int16(b.Row), Col: int16(b.Col)}
		event, err := match.engine.PlaceMachineBomb(state.PlayerID, c, byte(b.SceneID), b.Prop)
		if err != nil {
			match.logf("machine bubble rejected: %v", err)
			continue
		}
		match.bombs[event.BombID] = wireBomb{owner: state.PlayerID, placedAt: state.Time, cell: c}
		bomb, _ := bombByID(match.engine.Bombs(), event.BombID)
		rule.flights[event.BombID] = kickFlight{From: cell{Row: max(0, c.Row-3), Col: c.Col}, Direction: 3, Cells: min(c.Row, 3), Start: match.engine.ElapsedMS(), Until: bomb.FlightUntilMS}
		events = append(events, event)
	}
	return events
}
func (match *onlineMatch) machineTick() []battleengine.Event {
	rule := match.machine
	if rule == nil || rule.finishing {
		return nil
	}
	now := match.engine.ElapsedMS()
	rule.updateCharges(match.engine, now, match.local)
	for _, g := range rule.gates {
		if rule.destroyed[g.side] {
			continue
		}
		match.engine.SetMachineGate(g.anchor, g.side == 0, rule.ready(g.side, now))
	}
	for id, f := range rule.flights {
		if f.Until <= now {
			delete(rule.flights, id)
		}
	}
	if !match.isArbitrator() {
		return nil
	}
	var events []battleengine.Event
	kept := rule.pending[:0]
	for _, p := range rule.pending {
		if p.due > now {
			kept = append(kept, p)
			continue
		}
		body, err := p.state.MarshalNetworkBinary(game.NotifyMachineMove)
		if err != nil {
			continue
		}
		events = append(events, match.applyMachineMove(p.state)...)
		match.queue(game.NotifyMachineMove, now, now, body)
		match.mirror(game.NotifyMachineMove, body)
	}
	rule.pending = kept
	events = append(events, match.machineSupplyTick()...)
	return events
}

func (match *onlineMatch) machineHarm(event battleengine.Event, avatar bool) {
	player := event.TargetID
	if avatar {
		player = event.PlayerID
	}
	if player != match.local {
		return
	}
	harm := game.EntityHarmedEvent{ObjectID: player, Time: event.TimeMS, PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y), IsAvatar: avatar}
	body := make([]byte, 13)
	binary.BigEndian.PutUint16(body, harm.ObjectID)
	binary.BigEndian.PutUint32(body[2:], harm.Time)
	binary.BigEndian.PutUint16(body[6:], harm.PosX)
	binary.BigEndian.PutUint16(body[8:], harm.PosY)
	if avatar {
		body[10] = 1
	}
	match.machine.seen[[3]uint32{game.PlayerBeHarmed, uint32(player), harm.Time}] = true
	match.queue(game.PlayerBeHarmed, harm.Time, harm.Time, body)
	match.mirror(game.PlayerBeHarmed, body)
	match.machineHarmReport(harm)
}
func (match *onlineMatch) receiveMachineHarm(harm game.EntityHarmedEvent) []battleengine.Event {
	key := [3]uint32{game.PlayerBeHarmed, uint32(harm.ObjectID), harm.Time}
	if match.machine.seen[key] {
		return nil
	}
	var events []battleengine.Event
	pos := battleengine.Position{X: int32(harm.PosX), Y: int32(harm.PosY)}
	if harm.IsAvatar {
		e, changed, err := match.engine.ApplyVerifiedActorHit(harm.ObjectID, pos, true)
		if err != nil || !changed {
			return nil
		}
		events = append(events, e)
	} else {
		var err error
		events, err = match.engine.ApplyNativeDurabilityHit(harm.ObjectID, 0, pos, harm.Time)
		if err != nil || len(events) == 0 {
			return nil
		}
	}
	match.machine.seen[key] = true
	match.machineHarmReport(harm)
	return events
}

// A final death may beat the victim's harm packet to a third client because
// the messages have different senders. Commit its final hit once; the engine
// elimination also zeros the displayed layers, regardless of arrival order.
func (match *onlineMatch) machineFinalDeath(death game.PlayerDeathEvent) {
	key := [3]uint32{game.PlayerBeHarmed, uint32(death.PlayerID), death.ClientTime}
	if match.machine.seen[key] {
		return
	}
	match.machine.seen[key] = true
	if actor, ok := actorByID(match.engine.Actors(), death.PlayerID); ok {
		match.machine.hits[match.machine.side(actor.TeamID)]++
	}
	match.finishMachineIfDecided()
}

func (match *onlineMatch) machineHarmReport(harm game.EntityHarmedEvent) {
	actor, ok := actorByID(match.engine.Actors(), harm.ObjectID)
	if !ok || harm.IsAvatar {
		return
	}
	match.machine.hits[match.machine.side(actor.TeamID)]++
	if actor.DurabilityLeft == 0 {
		match.finishMachineIfDecided()
	}
	match.logf("machine player %d durability %d at %d", harm.ObjectID, actor.DurabilityLeft, harm.Time)
	if !match.isArbitrator() {
		return
	}
	body, _ := (game.PlayerDeathEvent{PlayerID: harm.ObjectID, ClientTime: harm.Time, PosX: harm.PosX, PosY: harm.PosY, Items: []game.GameItem{}}).MarshalNetworkBinary()
	match.mirror(game.NotifyPlayerDieEvent, body)
	if actor.DurabilityLeft == 0 {
		match.queue(game.NotifyPlayerDieEvent, harm.Time, harm.Time, body)
	}
}

type machineGateView struct {
	Destroyed bool   `json:"destroyed"`
	Row       int16  `json:"row"`
	Col       int16  `json:"col"`
	Team      byte   `json:"team"`
	First     bool   `json:"first"`
	Ready     bool   `json:"ready"`
	Pattern   uint16 `json:"pattern"`
	Fired     uint32 `json:"fired"`
}
type machineChargeView struct {
	Count   int    `json:"count"`
	Elapsed uint32 `json:"elapsed"`
	Period  uint32 `json:"period"`
}

type machineView struct {
	Charge  *machineChargeView    `json:"charge,omitempty"`
	Aim     *cell                 `json:"aim,omitempty"`
	Gates   []machineGateView     `json:"gates"`
	Flights map[string]kickFlight `json:"flights"`
}

func (rule *machineRule) view(now uint32) *machineView {
	v := &machineView{Flights: map[string]kickFlight{}}
	for _, g := range rule.gates {
		v.Gates = append(v.Gates, machineGateView{rule.destroyed[g.side], g.anchor.Row, g.anchor.Col, rule.teams[g.side], g.side == 0, rule.ready(g.side, now), g.pattern, g.fired})
	}
	for id, f := range rule.flights {
		v.Flights[strconv.FormatUint(uint64(id), 10)] = f
	}
	return v
}

func (match *onlineMatch) machineRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	if match.machine == nil {
		return nil, false
	}
	switch event.Schema {
	case game.PlayerThrowBomb:
		request, err := game.ParseKickBombAction(event)
		if err != nil || request.PlayerID != sender {
			return nil, true
		}
		return match.applyMachineThrow(request), true
	case game.RequestLaunchMachine:
		request, err := game.ParseMachineLaunchEvent(event)
		if err == nil && request.PlayerID == sender && match.isArbitrator() {
			match.arbitrateMachine(request)
		}
		return nil, true
	case game.NotifyMachineFree, game.NotifyMachineAngry, game.NotifyMachineGrace, game.NotifyMachineCool, game.NotifyMachineMove:
		if sender != match.arbitrator {
			return nil, true
		}
		state, err := game.ParseMachineBombState(event)
		if err != nil {
			return nil, true
		}
		if event.Schema == game.NotifyMachineMove {
			return match.applyMachineMove(state), true
		}
		match.applyMachineState(event.Schema, state)
		return nil, true
	case game.PlayerBeHarmed:
		harm, err := game.ParseEntityHarmedEvent(event)
		if err != nil || (sender != harm.ObjectID && sender != match.arbitrator) {
			return nil, true
		}
		return match.receiveMachineHarm(harm), true
	}
	return nil, false
}

// machineThrowTarget is 00619019 / the cursor's 005EFD1C search. Horizontal
// throws use the other half; vertical throws may stay on the same half.
func machineThrowTarget(engine *battleengine.Engine, from battleengine.Cell, direction battleengine.Direction) (battleengine.Cell, int16, bool) {
	dr, dc := int16(0), int16(0)
	switch direction {
	case battleengine.DirectionRight:
		dc = 1
	case battleengine.DirectionLeft:
		dc = -1
	case battleengine.DirectionUp:
		dr = -1
	case battleengine.DirectionDown:
		dr = 1
	default:
		return from, 0, false
	}
	base := from.Col
	if base+dc >= 0 && base+dc < 15 {
		base += dc
	}
	first := int16(6)
	if base == from.Col {
		first = 7
	}
	occupied := map[battleengine.Cell]bool{}
	for _, b := range engine.Bombs() {
		occupied[b.Cell] = true
	}
	valid := func(n int16) (battleengine.Cell, int16, bool) {
		to := battleengine.Cell{Row: (from.Row + dr*n + 13) % 13, Col: (base + dc*n + 15) % 15}
		if dc != 0 && ((from.Col < 7 && to.Col < 7) || (from.Col >= 7 && to.Col > 7)) {
			return to, 0, false
		}
		tile, ok := engine.Grid().Cell(to)
		if !ok || tile.MapElementOccupied || tile.Kind != battleengine.CellOpen || occupied[to] || to == from {
			return to, 0, false
		}
		distance := n
		if base != from.Col {
			distance++
		}
		return to, distance, true
	}
	for n := first; n < 15; n++ {
		if c, d, ok := valid(n); ok {
			return c, d, true
		}
	}
	for n := first - 1; n > 0; n-- {
		if c, d, ok := valid(n); ok {
			return c, d, true
		}
	}
	return from, 0, false
}

func (match *onlineMatch) throwMachine(action *battleengine.Action) []battleengine.Event {
	if match.machine == nil || !action.PlaceBomb {
		return nil
	}
	action.PlaceBomb = false
	match.machine.updateCharges(match.engine, match.engine.ElapsedMS(), match.local)
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || match.machine.charges[actor.PlayerID].count == 0 || !match.engine.CanThrowMachineBomb(actor.PlayerID) {
		return nil
	}
	for _, g := range match.machine.gates {
		if !match.machine.destroyed[g.side] && actor.Position.Cell() == g.trigger() {
			return nil
		}
	}
	to, _, ok := machineThrowTarget(match.engine, actor.Position.Cell(), actor.Facing)
	if !ok {
		return nil
	}
	from := machineThrowOrigin(actor.Position.Cell(), actor.Facing)
	direction := byte(nativeWalkDirection(actor.Facing))
	request := game.KickBombAction{BombRowAndCol: byte(from.Row<<4 | from.Col), BombDestRowAndCol: byte(to.Row<<4 | to.Col), Direction: direction, Power: actor.BombPower + 1, Time: match.engine.ElapsedMS(), PlayerID: actor.PlayerID, BombPower: uint16(actor.BombPower + 1)}
	if actor.SceneFourEffectExpiresAt > match.engine.ElapsedMS() {
		request.BombProp = 1
	}
	body, _ := request.MarshalNetworkBinary()
	events := match.applyMachineThrow(request)
	if len(events) > 0 {
		match.machine.charges[actor.PlayerID].count--
		match.queue(game.PlayerThrowBomb, request.Time, request.Time, body)
		match.mirror(game.PlayerThrowBomb, body)
	}
	return events
}
func (match *onlineMatch) applyMachineThrow(request game.KickBombAction) []battleengine.Event {
	key := [3]uint32{game.PlayerThrowBomb, uint32(request.PlayerID), request.Time}
	if match.machine.seen[key] {
		return nil
	}
	from := battleengine.Cell{Row: int16(request.BombRowAndCol >> 4), Col: int16(request.BombRowAndCol & 15)}
	to := battleengine.Cell{Row: int16(request.BombDestRowAndCol >> 4), Col: int16(request.BombDestRowAndCol & 15)}
	flightMS := machineFlightTime(from, to, request.Direction)
	e, err := match.engine.ApplyVerifiedMachineThrow(request.PlayerID, to, request.Power, request.BombProp, request.Time, flightMS)
	if err != nil {
		return nil
	}
	match.machine.seen[key] = true
	match.bombs[e.BombID] = wireBomb{owner: request.PlayerID, placedAt: request.Time, cell: to}
	match.machine.flights[e.BombID] = kickFlight{From: cell{Row: from.Row, Col: from.Col}, To: &cell{Row: to.Row, Col: to.Col}, Direction: request.Direction, Start: request.Time, Until: request.Time + flightMS}
	return []battleengine.Event{e}
}

func (match *onlineMatch) machineView() *machineView {
	v := match.machine.view(match.engine.ElapsedMS())
	if actor, ok := actorByID(match.engine.Actors(), match.local); ok && actor.State == battleengine.ActorActive {
		if charge := match.machine.charges[actor.PlayerID]; charge != nil {
			elapsed := match.engine.ElapsedMS() - charge.last
			if charge.count >= 5 {
				elapsed = 0
			}
			v.Charge = &machineChargeView{charge.count, elapsed, machineChargePeriod(actor)}
		}
		if target, _, valid := machineThrowTarget(match.engine, actor.Position.Cell(), actor.Facing); valid {
			v.Aim = &cell{Row: target.Row, Col: target.Col}
		}
	}
	return v
}

func (match *onlineMatch) machineExplosion(explosion game.BombExplodeEvent) {
	if match.machine == nil {
		return
	}
	for _, bomb := range explosion.Bombs {
		if bomb.BombID != 14 || bomb.Column == 7 {
			continue
		}
		side := 0
		if bomb.Column > 7 {
			side = 1
		}
		if match.machine.destroyed[side] {
			continue
		}
		match.machine.destroyed[side] = true
		for _, gate := range match.machine.gates {
			if gate.side == side {
				match.engine.RemoveMachineGate(gate.anchor)
			}
		}
		match.logf("machine side %d equipment destroyed by Cool", side)
	}
}

func machineChargePeriod(actor battleengine.Actor) uint32 {
	count := int(actor.NativeBombPickupCount())
	if count < 0 || count > 4 {
		return 1000
	}
	return uint32(2000 - count*200)
}
func (rule *machineRule) updateCharges(engine *battleengine.Engine, now uint32, local uint16) {
	if rule.charges == nil {
		rule.charges = map[uint16]*machineCharge{}
	}
	for _, actor := range engine.Actors() {
		if actor.PlayerID != local {
			continue
		}
		charge := rule.charges[actor.PlayerID]
		if charge == nil {
			// The sidecar skips Ready ticks, but the native +414 object refills
			// during those first 3000 ms. Reconstruct only that skipped interval.
			period := machineChargePeriod(actor)
			ready := min(now, battleengine.NativeRoundStartClockMS)
			charge = &machineCharge{count: int(ready / period), last: ready / period * period}
			rule.charges[actor.PlayerID] = charge
		}
		period := machineChargePeriod(actor)
		if now >= charge.last && now-charge.last >= period {
			charge.count = min(5, charge.count+1)
			charge.last = now
		}
	}
}

func machineThrowOrigin(from battleengine.Cell, direction battleengine.Direction) battleengine.Cell {
	if direction == battleengine.DirectionDown {
		if from.Row < 12 {
			from.Row++
		}
	} else {
		if from.Row > 0 {
			from.Row--
		}
	}
	if direction == battleengine.DirectionRight && from.Col < 14 {
		from.Col++
	}
	if direction == battleengine.DirectionLeft && from.Col > 0 {
		from.Col--
	}
	return from
}
func machineFlightTime(from, to battleengine.Cell, direction byte) uint32 {
	dx, dy := int(to.Col-from.Col)*40, int(to.Row-from.Row)*40
	if direction == 0 && dx < 0 {
		dx += 600
	}
	if direction == 2 && dx > 0 {
		dx -= 600
	}
	if direction == 1 && dy > 0 {
		dy -= 520
	}
	if direction == 3 && dy < 0 {
		dy += 520
	}
	return uint32(math.Ceil(math.Hypot(float64(dx), float64(dy)) * 2))
}

// Do not author new gameplay in the server's 750ms settlement delay after
// the final durability hit. This is only a producer stop, not a local result.
// Departure alone is handled by the independent authority handover path.
func (match *onlineMatch) finishMachineIfDecided() {
	teams := map[byte]bool{}
	for _, actor := range match.engine.Actors() {
		if actor.State != battleengine.ActorEliminated && actor.DurabilityLeft > 0 {
			teams[actor.TeamID] = true
		}
	}
	if len(teams) <= 1 {
		match.machine.finishing = true
	}
}
func (match *onlineMatch) machineFinishing() bool {
	return match.machine != nil && match.machine.finishing
}
