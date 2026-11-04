package main

// Rule-8 AIType2: 00616D19/005B8FA7 create one roaming entity after 60s,
// replenishing an empty NPC list after 3s. 005DF20B chooses 007A358C:
// 005DEF29 gates safe decisions at 500ms and escapes danger, while 005DF009
// places a bubble only after 005B0598 finds an escape route. This controller
// shares the native WF search and candidate order with Boss controllers.
// The rule-8 move command dispatches every 12 updates (stop retries sooner).
// Default geometry uses the shared integer native movement/corner resolver;
// subpixel integration, active traversal and status interactions remain open.
// NPCs use ordinary syrup (HP field 0, 00607A23), not four-layer player HP.
// They remain outside the eight participant slots and match conclusions.

import (
	"encoding/binary"
	"fmt"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

const boxNPCFirstID uint16 = 20001

type boxNPCActor struct {
	id                              uint16
	role                            uint16
	pos                             battleengine.Position
	facing                          byte
	moving                          bool
	dead                            bool
	dropsApplied                    bool
	deadAt                          uint32
	trapped                         bool
	trappedAt                       uint32
	lastTick                        uint32
	nextDecision                    uint32
	lastDanger                      uint32
	navigator                       npcNavigator
	moveBlocked                     bool
	danger                          battleengine.DangerTimeline
	path                            []battleengine.Cell
	fraction                        uint32
	capacity, power, speed          byte
	maxCapacity, maxPower, maxSpeed byte
	basic                           [3]byte
	sceneFourUntil                  uint32
	sequence                        uint16
	lastMoveSent                    uint32
	lastMovePos                     battleengine.Position
	lastMoveEnd                     battleengine.Position
	lastMoveFacing                  byte
	lastMoveMoving                  bool
	moveInitialized                 bool
	moveEnd                         battleengine.Position
	moveEndValid                    bool
	netMove                         game.PlayerMoveSequence
	netMoveSet                      bool
}
type boxNPCOutbound struct {
	author  uint16
	message game.BattleMessageData
}
type boxNPCCreation struct {
	data     game.CreateNPCBoss
	sentAt   uint32
	attempts int
}
type boxNPCState struct {
	actors      map[uint16]*boxNPCActor
	nextID      uint16
	candidates  []battleengine.Cell
	started     bool
	emptySince  uint32
	emptyKnown  bool
	pending     *boxNPCCreation
	outbox      []boxNPCOutbound
	seen        map[[4]uint32]bool
	moveCommand npcMoveCommand
}

type boxNPCActorView struct {
	ID      uint16 `json:"id"`
	Role    uint16 `json:"role"`
	Team    byte   `json:"team"`
	X       int32  `json:"x"`
	Y       int32  `json:"y"`
	Facing  byte   `json:"facing"`
	Moving  bool   `json:"moving"`
	Dead    bool   `json:"dead"`
	Trapped bool   `json:"trapped"`
}
type boxNPCView struct {
	Actors []boxNPCActorView `json:"actors"`
}

func (match *onlineMatch) boxNPCState() *boxNPCState {
	if match.box == nil {
		return nil
	}
	s := match.box.npc
	if s == nil {
		s = &boxNPCState{}
		match.box.npc = s
	}
	if s.actors == nil {
		s.actors = map[uint16]*boxNPCActor{}
		s.seen = map[[4]uint32]bool{}
		s.nextID = boxNPCFirstID
	}
	if s.candidates == nil {
		used := map[battleengine.Cell]bool{}
		add := func(cell battleengine.Cell) {
			if !used[cell] {
				s.candidates = append(s.candidates, cell)
				used[cell] = true
			}
		}
		// 005B68D3: actual A/B spawns first, then unused full A/B source cells.
		for _, info := range match.begin.Players {
			if a, ok := actorByID(match.engine.Actors(), info.PlayerID); ok {
				add(a.Spawn)
			}
		}
		for _, cell := range match.box.entry.SpawnGroupA {
			add(battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)})
		}
		for _, cell := range match.box.entry.SpawnGroupB {
			add(battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)})
		}
	}
	return s
}
func (s *boxNPCState) ordered() []*boxNPCActor {
	ids := make([]int, 0, len(s.actors))
	for id := range s.actors {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	out := make([]*boxNPCActor, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.actors[uint16(id)])
	}
	return out
}
func (s *boxNPCState) living() int {
	n := 0
	for _, a := range s.actors {
		if !a.dead {
			n++
		}
	}
	return n
}

func (match *onlineMatch) createBoxNPC(data game.CreateNPCBoss, now uint32) error {
	s := match.boxNPCState()
	if s == nil {
		return fmt.Errorf("box NPC outside rule8")
	}
	for _, info := range data.Bosses {
		if info.BossID < boxNPCFirstID || info.BossID >= 23000 || info.RoleID < 25 || info.RoleID > 27 || info.TeamID != 7 || info.AIType != 2 {
			return fmt.Errorf("invalid rule8 NPC template")
		}
		cell := battleengine.Cell{Row: int16(info.Row), Col: int16(info.Col)}
		if _, ok := match.engine.TileAt(cell); !ok {
			return fmt.Errorf("NPC spawn outside field")
		}
		if _, exists := s.actors[info.BossID]; exists {
			continue
		}
		if err := match.engine.RegisterRuleNPCOwner(info.BossID); err != nil {
			return err
		}
		profiles := [3][6]byte{{1, 2, 2, 4, 1, 3}, {2, 4, 2, 5, 2, 4}, {3, 5, 3, 6, 3, 5}}
		// 005AFDFA checks local-control ownership before 005FCE22 inserts
		// this fresh ID into the NPC list. Its optional 3s protection branch
		// is therefore not taken by a newly generated rule-8 monster.
		p := profiles[int(info.RoleID)-25]
		s.actors[info.BossID] = &boxNPCActor{id: info.BossID, role: uint16(info.RoleID), pos: battleengine.PositionAtCellCenter(cell), facing: 3, lastTick: now, navigator: newNPCNavigator(cell, 3, now), capacity: p[0], maxCapacity: p[1], power: p[2], maxPower: p[3], speed: p[4], maxSpeed: p[5]}
		s.nextID = max(s.nextID, info.BossID+1)
		match.logf("box NPC %d role %d spawned at %d,%d", info.BossID, info.RoleID, cell.Row, cell.Col)
	}
	s.started = true
	s.emptyKnown = false
	return nil
}

// The TCP echo proves that the server registered the identity before its
// first proxy package. Creation and the first movement share one UDP batch.
func (match *onlineMatch) boxNPCAcknowledged(event game.GameEvent) {
	s := match.boxNPCState()
	if s == nil || !match.isArbitrator() || event.Schema != game.CreateNPCBossEvent || s.pending == nil {
		return
	}
	data, err := game.ParseCreateNPCBossNetwork(event.Body)
	if err != nil || len(data.Bosses) != 1 || data.Bosses[0].BossID != s.pending.data.Bosses[0].BossID {
		return
	}
	now := match.engine.ElapsedMS()
	if err = match.createBoxNPC(data, now); err != nil {
		match.logf("NPC creation ACK: %v", err)
		return
	}
	s.pending = nil
	match.queueBoxNPC(match.local, game.CreateNPCBossEvent, now, event.Body)
	for _, n := range s.ordered() {
		if !n.dead {
			match.publishBoxNPCMove(n, now, true)
		}
	}
}
func (match *onlineMatch) tryCreateBoxNPC(now uint32) {
	s := match.boxNPCState()
	if s.pending != nil {
		if now >= s.pending.sentAt+1000 && s.pending.attempts < 3 {
			body, _ := s.pending.data.MarshalNetworkBinary()
			match.mirror(game.CreateNPCBossEvent, body)
			s.pending.sentAt = now
			s.pending.attempts++
		}
		return
	}
	if s.living() != 0 {
		s.emptyKnown = false
		return
	}
	if !s.emptyKnown {
		s.emptyKnown = true
		s.emptySince = now
	}
	if now <= 60000 || s.started && now-s.emptySince <= 3000 || len(s.candidates) == 0 || s.nextID >= 23000 {
		return
	}
	var at battleengine.Cell
	found := false
	for attempt := 0; attempt < 201; attempt++ {
		at = s.candidates[match.box.rng.Intn(len(s.candidates))]
		tile, ok := match.engine.TileAt(at)
		if ok && !tile.MapElementOccupied && match.box.at(at) == nil {
			found = true
			break
		}
	}
	if !found {
		return
	}
	role := byte(25)
	roll := match.box.rng.Intn(10)
	if roll >= 3 {
		role = 26
	}
	if roll >= 6 {
		role = 27
	}
	data := game.CreateNPCBoss{Bosses: []game.CompetitiveBossInfo{{BossID: s.nextID, RoleID: role, TeamID: 7, AIType: 2, Row: uint16(at.Row), Col: uint16(at.Col), OutfitItems: []game.BossItemInfo{{ItemID: 500, ItemCount: 3}, {ItemID: 501, ItemCount: 2}}}}}
	body, err := data.MarshalNetworkBinary()
	if err != nil {
		return
	}
	s.pending = &boxNPCCreation{data: data, sentAt: now, attempts: 1}
	match.mirror(game.CreateNPCBossEvent, body)
	match.logf("box NPC %d awaiting registration ACK", s.nextID)
}

func (match *onlineMatch) queueBoxNPC(author uint16, schema uint16, now uint32, body []byte) {
	match.queueNPC(&match.boxNPCState().outbox, author, schema, now, body)
}
func (match *onlineMatch) flushBoxNPC() {
	if s := match.boxNPCState(); s != nil {
		match.flushNPC(&s.outbox)
	}
}

// queueNPC adds one message authored by a scene entity (or by the
// arbitrator about it). Each goes out as its own package whose PlayerID is
// the author, so peers attribute it to the NPC.
func (match *onlineMatch) queueNPC(outbox *[]boxNPCOutbound, author uint16, schema uint16, now uint32, body []byte) {
	match.messageSeq++
	*outbox = append(*outbox, boxNPCOutbound{author: author, message: game.BattleMessageData{Time: now, DataID: uint32(schema), Sequence: match.messageSeq, Data: body, GameTime: now}})
}
func (match *onlineMatch) flushNPC(outbox *[]boxNPCOutbound) {
	if len(*outbox) == 0 || !match.presence || match.conn == nil {
		return
	}
	if len(match.peerIDs()) == 0 {
		*outbox = nil // everyone else left; the reliable mirrors still go
		return
	}
	targets := []game.LegacyUDPMulticastTarget{}
	for _, id := range match.peerIDs() {
		targets = append(targets, game.LegacyUDPMulticastTarget{PlayerID: id, UIN: match.uins[id]})
	}
	for start := 0; start < len(*outbox); start += 16 {
		batch := game.GameplayBatch{GameID: match.begin.GameID}
		for _, out := range (*outbox)[start:min(start+16, len(*outbox))] {
			message := out.message
			batch.Entries = append(batch.Entries, game.GameplayBatchEntry{Index: uint16(message.Sequence), Package: game.GameplayDataPackage{PlayerID: out.author, Time: message.Time, GameID: match.begin.GameID, MessageIndexes: []uint32{message.Sequence}, Messages: []game.BattleMessageData{message}}})
		}
		data, err := game.EncodeQQTPPPGameplayBatch(batch)
		if err != nil {
			match.logf("NPC batch: %v", err)
			continue
		}
		match.multicastNumber++
		match.sendControl(game.LegacyUDPControlPacket{Header: match.header(game.LegacyUDPMulticastType, match.multicastNumber), Multicast: &game.LegacyUDPMulticastPayload{Targets: targets, Data: data}})
	}
	*outbox = nil
}
func (match *onlineMatch) publishBoxNPCMove(n *boxNPCActor, now uint32, force bool) {
	match.publishNPCMove(&match.boxNPCState().outbox, n, now, force)
}
func (match *onlineMatch) publishNPCMove(outbox *[]boxNPCOutbound, n *boxNPCActor, now uint32, force bool) {
	end := n.pos
	if n.moveEndValid && n.moving {
		end = n.moveEnd
	} else if !n.moveEndValid && len(n.path) > 0 {
		end = battleengine.PositionAtCellCenter(n.path[0])
	}
	if !force && n.moveInitialized && now-n.lastMoveSent < movementHeartbeat && n.moving == n.lastMoveMoving && n.facing == n.lastMoveFacing && end == n.lastMoveEnd {
		return
	}
	walk := n.facing
	if n.moving {
		walk |= 0x10
	}
	n.sequence++
	move := game.PlayerMoveSequence{Sequence: n.sequence, TimeStamp: now, CurrentPosX: uint16(n.pos.X), CurrentPosY: uint16(n.pos.Y), EndPosX: uint16(end.X), EndPosY: uint16(end.Y), WalkAndDirection: walk, Speed: n.speed}
	body, err := (game.PlayerMove{PlayerID: n.id, Entries: []game.PlayerMoveEntry{{PlayerID: n.id, Move: move}}}).MarshalNetworkBinary()
	if err == nil {
		match.queueNPC(outbox, n.id, game.PlayerMoveSchema, now, body)
	}
	n.lastMoveSent = now
	n.lastMovePos = n.pos
	n.lastMoveEnd = end
	n.lastMoveFacing = n.facing
	n.lastMoveMoving = n.moving
	n.moveInitialized = true
}

func (match *onlineMatch) boxNPCTick(now uint32) {
	s := match.boxNPCState()
	if s == nil || match.box.finishing {
		return
	}
	if match.isArbitrator() {
		match.tryCreateBoxNPC(now)
	}
	for _, n := range s.ordered() {
		if n.dead {
			continue
		}
		if match.isArbitrator() {
			match.advanceBoxNPC(n, now)
		} else {
			match.interpolateBoxNPC(n, now)
		}
		// Each human owns its contact observation; only a live, untrapped NPC
		// whose centre is strictly within 15px on both axes can knock it.
		if !n.dead && !n.trapped {
			if actor, ok := actorByID(match.engine.Actors(), match.local); ok && actor.State == battleengine.ActorActive && abs32(actor.Position.X-n.pos.X) < 15 && abs32(actor.Position.Y-n.pos.Y) < 15 {
				match.boxHitLocal(game.NotifyPlayerKnocked, 0)
			}
		}
	}
	if match.isArbitrator() {
		match.flushBoxNPC()
	}
}

// interpolateBoxNPC follows the owner's last PLAYER_MOVE segment.
func (match *onlineMatch) interpolateBoxNPC(n *boxNPCActor, now uint32) {
	if !n.netMoveSet || n.trapped || now < n.netMove.TimeStamp {
		return
	}
	m := n.netMove
	pos := battleengine.Position{X: int32(m.CurrentPosX), Y: int32(m.CurrentPosY)}
	end := battleengine.Position{X: int32(m.EndPosX), Y: int32(m.EndPosY)}
	speed := uint16(bossPixelsPerSecond(m.Speed))
	travel := int32(uint64(now-m.TimeStamp) * uint64(speed) / 1000)
	if m.WalkAndDirection&0x10 != 0 {
		if pos.X != end.X {
			d := min(travel, abs32(end.X-pos.X))
			if pos.X < end.X {
				pos.X += d
			} else {
				pos.X -= d
			}
		} else {
			d := min(travel, abs32(end.Y-pos.Y))
			if pos.Y < end.Y {
				pos.Y += d
			} else {
				pos.Y -= d
			}
		}
	}
	n.pos = pos
	n.moving = m.WalkAndDirection&0x10 != 0
	n.facing = m.WalkAndDirection & 3
	n.lastTick = now
}
func (match *onlineMatch) npcOpen(cell battleengine.Cell, origin battleengine.Cell) bool {
	tile, ok := match.engine.TileAt(cell)
	if !ok || tile.Kind != battleengine.CellOpen || tile.MapElementOccupied {
		return false
	}
	for _, bomb := range match.engine.Bombs() {
		if bomb.Cell == cell && cell != origin {
			return false
		}
	}
	return true
}
func (match *onlineMatch) npcPath(n *boxNPCActor, danger battleengine.DangerTimeline, limit int) []battleengine.Cell {
	start := n.pos.Cell()
	queue := []battleengine.Cell{start}
	previous := map[battleengine.Cell]battleengine.Cell{start: start}
	depth := map[battleengine.Cell]int{start: 0}
	speed := bossPixelsPerSecond(n.speed)
	perCell := uint32(40000 / max(1, int(speed)))
	for len(queue) > 0 {
		cell := queue[0]
		queue = queue[1:]
		_, bad := danger.ImpactAt(cell)
		if cell != start && !bad {
			path := []battleengine.Cell{cell}
			for previous[cell] != start {
				cell = previous[cell]
				path = append([]battleengine.Cell{cell}, path...)
			}
			return path
		}
		if depth[cell] >= limit {
			continue
		}
		for dir := byte(0); dir < 4; dir++ {
			next := boxStep(cell, dir)
			if _, seen := previous[next]; seen || !match.npcOpen(next, start) {
				continue
			}
			if at, bad := danger.ImpactAt(next); bad {
				clear, _ := danger.ClearAt(next)
				arrival := match.engine.ElapsedMS() + uint32(depth[cell]+1)*perCell
				if at <= arrival+perCell+150 && clear >= arrival {
					continue
				}
			}
			previous[next] = cell
			depth[next] = depth[cell] + 1
			queue = append(queue, next)
		}
	}
	return nil
}
func (match *onlineMatch) npcPlace(n *boxNPCActor, now uint32) bool {
	// 005DF009 searches/appends before 005B08E0 checks actual capacity.
	path, ok := (&bossGeneral{}).escapePlan(match.bossWorld(now), n.pos.Cell())
	if !ok {
		return false
	}
	n.navigator.path.appendEscape(path)
	active := 0
	for _, b := range match.engine.Bombs() {
		if b.OwnerID == n.id {
			active++
		}
		if b.Cell == n.pos.Cell() {
			return false
		}
	}
	if active >= int(n.capacity) || !match.npcOpen(n.pos.Cell(), n.pos.Cell()) {
		return false
	}
	property := byte(0)
	if n.sceneFourUntil > now {
		property = 1
	}
	placed, err := match.engine.PlaceRuleNPCBomb(n.id, n.pos.Cell(), uint16(n.power)+1, property, now)
	if err != nil {
		return false
	}
	match.bombs[placed.BombID] = wireBomb{owner: n.id, placedAt: now, cell: n.pos.Cell()}
	body, _ := (game.PlayerUseBombEvent{PlayerID: n.id, ClientTime: now, BombID: 1, Row: byte(n.pos.Cell().Row), Column: byte(n.pos.Cell().Col), Power: uint16(n.power) + 1, Property: property}).MarshalNetworkBinary()
	match.queueBoxNPC(n.id, game.PlayerUseBomb, now, body)
	match.mirror(game.PlayerUseBomb, body)
	match.logf("box NPC %d places bubble at %d,%d", n.id, n.pos.Cell().Row, n.pos.Cell().Col)
	return true
}
func (match *onlineMatch) advanceBoxNPC(n *boxNPCActor, now uint32) {
	if now < n.lastTick {
		return
	}
	dt := now - n.lastTick
	// Follow the last authenticated segment when taking over mid-cell.
	if len(n.path) == 0 && n.netMoveSet && n.moving {
		n.path = []battleengine.Cell{{Row: int16(n.netMove.EndPosY / 40), Col: int16(n.netMove.EndPosX / 40)}}
		n.navigator.walking, n.navigator.direction = true, n.facing
	}
	if b := match.box.movingBoxContact(n.pos); b != nil && b.Flags&4 == 0 {
		killer := b.Owner
		if b.breakable(now) {
			match.destroyBoxes(match.local, []uint32{b.ID}, now)
		}
		match.killBoxNPC(n, killer, now, true)
		return
	}
	if n.trapped {
		for _, actor := range match.engine.Actors() {
			if actor.State == battleengine.ActorActive && abs32(actor.Position.X-n.pos.X) < 41 && abs32(actor.Position.Y-n.pos.Y) < 41 {
				match.killBoxNPC(n, actor.PlayerID, now, false)
				return
			}
		}
		if now >= n.trappedAt && now-n.trappedAt >= 6000 {
			match.killBoxNPC(n, 0, now, false)
		}
		n.lastTick = now
		return
	}
	for _, f := range match.engine.Flames() {
		if f.Cell == n.pos.Cell() && f.ImpactAtMS <= now {
			match.trapBoxNPC(n, now)
			return
		}
	}
	for _, effect := range match.box.effects {
		if effect.Style != 10000 && now >= effect.Start && now < effect.Until && boxAreaContains(effect.Cell, n.pos) {
			match.trapBoxNPC(n, now)
			return
		}
	}
	if now-n.lastDanger >= 100 || n.lastDanger == 0 {
		n.danger, _ = match.engine.DangerTimeline(4000)
		n.lastDanger = now
	}
	// Bubble polling retains its existing cadence until its separate dispatch
	// call site is traced. Eligibility and vector append are independent of
	// the frame-counted move command.
	if now >= n.nextDecision {
		n.nextDecision = now + 500
		if npcBubbleEligible(n.navigator.path.nodes, func() bool { return match.bossWorld(now).playerInLine(n.pos.Cell()) }) {
			match.npcPlace(n, now)
		}
	}
	if direction, emitted := match.boxNPCState().moveCommand.tick(8, now, 1, 1, func() byte {
		return match.chooseBoxNPCDirection(n, now, dt)
	}); emitted {
		n.navigator.direction, n.navigator.walking = direction, direction != 9
		if direction == 9 {
			n.path = nil
		} else {
			n.facing = direction
			n.path = []battleengine.Cell{boxStep(n.pos.Cell(), direction)}
		}
	}
	match.moveBoxNPC(n, dt)
	match.collectBoxNPCPickup(n, now)
	match.publishBoxNPCMove(n, now, false)
	n.lastTick = now
}

// moveBoxNPC uses the shared native integer displacement/corner resolver.
// NPC rate conversion still rounds to px/s; subpixel integration, active
// traversal and transformation capabilities remain separate work.
func (match *onlineMatch) moveBoxNPC(n *boxNPCActor, dt uint32) {
	n.moveEndValid = true
	n.moveEnd = n.pos
	n.moving = false
	if !n.navigator.walking {
		n.moveBlocked = true // 005CF8D9 returns zero for a standing actor
		return
	}
	n.facing = n.navigator.direction
	n.fraction += uint32(bossPixelsPerSecond(n.speed)) * dt
	travel := int(n.fraction / 1000)
	n.fraction %= 1000
	start := n.pos
	observed := start
	if travel > 0 {
		position, err := match.engine.MoveRuleNPC(n.id, n.pos, battleDirection(n.facing), travel)
		if err != nil {
			match.logf("NPC movement: %v", err)
			return
		}
		n.pos = position
		n.moveBlocked = n.pos == start
		observed = n.pos
	} else {
		// A subpixel remainder has not produced an integer displacement yet.
		// Probe the next pixel so its wire endpoint cannot point outside the
		// field when the held command is already blocked.
		projected, err := match.engine.MoveRuleNPC(n.id, n.pos, battleDirection(n.facing), 1)
		if err == nil {
			observed = projected
			n.moveBlocked = projected == start
		}
	}
	// 005F3D0A publishes the commanded walk state even when movement fails.
	n.moving = true
	if n.moveBlocked {
		n.moveEnd = n.pos
		return
	}
	physical := n.facing
	if observed != start {
		if observed.X != start.X {
			physical = 0
			if observed.X < start.X {
				physical = 2
			}
		} else {
			physical = 3
			if observed.Y < start.Y {
				physical = 1
			}
		}
	}
	n.moveEnd = npcNextSegmentEnd(n.pos, physical)
	// A perpendicular correction that reaches the centre ends there. The
	// next update retries the commanded direction and publishes its new end.
	if physical != n.facing && (physical%2 == 0 && n.pos.X%40 == 20 || physical%2 == 1 && n.pos.Y%40 == 20) {
		n.moveEnd = n.pos
	}
}

func npcNextSegmentEnd(position battleengine.Position, direction byte) battleengine.Position {
	end := position
	if direction == 0 || direction == 2 {
		end.X = position.X/40*40 + 20
		if direction == 0 && end.X <= position.X {
			end.X += 40
		}
		if direction == 2 && end.X >= position.X {
			end.X -= 40
		}
	} else {
		end.Y = position.Y/40*40 + 20
		if direction == 3 && end.Y <= position.Y {
			end.Y += 40
		}
		if direction == 1 && end.Y >= position.Y {
			end.Y -= 40
		}
	}
	return end
}
func (match *onlineMatch) trapBoxNPC(n *boxNPCActor, now uint32) {
	n.trapped = true
	n.trappedAt = now
	n.lastTick = now
	n.path = nil
	n.moving = false
	n.navigator.walking = false
	n.netMoveSet = false
	body, _ := (game.PlayerExplodedEvent{PlayerID: n.id, ClientTime: now, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y)}).MarshalNetworkBinary()
	match.queueBoxNPC(n.id, game.PlayerBeExploded, now, body)
	match.mirror(game.PlayerBeExploded, body)
	match.logf("box NPC %d trapped at %d", n.id, now)
}
func (match *onlineMatch) npcDrops(n *boxNPCActor) []game.GameItem {
	var scenes []uint32
	for i, count := range n.basic {
		for j := 0; j < min(int(count), 10); j++ {
			scenes = append(scenes, uint32(i+1))
		}
	}
	cells := []battleengine.Cell{}
	grid := match.engine.Grid()
	for row := int16(0); row < int16(grid.Height); row++ {
		for col := int16(0); col < int16(grid.Width); col++ {
			c := battleengine.Cell{Row: row, Col: col}
			if c == n.pos.Cell() || !match.npcOpen(c, n.pos.Cell()) {
				continue
			}
			occupied := false
			for _, p := range match.engine.Pickups() {
				if p.State == battleengine.PickupAvailable && p.Cell == c {
					occupied = true
				}
			}
			if !occupied {
				cells = append(cells, c)
			}
		}
	}
	drops := []game.GameItem{}
	for _, scene := range scenes {
		if len(cells) == 0 {
			break
		}
		i := match.box.rng.Intn(len(cells))
		c := cells[i]
		cells[i] = cells[len(cells)-1]
		cells = cells[:len(cells)-1]
		drops = append(drops, game.GameItem{ItemID: scene, Row: byte(c.Row), Col: byte(c.Col)})
	}
	return drops
}
func (match *onlineMatch) applyBoxNPCDeath(n *boxNPCActor, now uint32, items []game.GameItem) {
	// 1162 can arrive before the 0FA9 drop table. Retirement and installing
	// that table are separate idempotent facts.
	if len(items) > 0 && !n.dropsApplied {
		events, err := match.engine.InstallRuleNPCDrops(n.id, pickupsFromItems(items))
		if err == nil {
			match.forgetPickups(events)
			n.dropsApplied = true
		}
	}
	if n.dead {
		return
	}
	n.dead = true
	n.deadAt = now
	n.trapped = false
	n.moving = false
	n.path = nil
	s := match.boxNPCState()
	s.emptyKnown = true
	s.emptySince = now
	match.logf("box NPC %d died at %d", n.id, now)
}
func (match *onlineMatch) killBoxNPC(n *boxNPCActor, killer uint16, now uint32, knocked bool) {
	if n.dead {
		return
	}
	drops := match.npcDrops(n)
	if _, valid := actorByID(match.engine.Actors(), killer); valid {
		body, _ := (game.PlayerKilledEvent{PlayerInteractionEvent: game.PlayerInteractionEvent{PlayerID: killer, DestinationPlayerID: n.id, ClientTime: now, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y)}, Items: drops}).MarshalNetworkBinary()
		match.queueBoxNPC(match.local, game.NotifyPlayerKilled, now, body)
		match.mirror(game.NotifyPlayerKilled, body)
	} else if !knocked || len(drops) > 0 {
		body, _ := (game.PlayerDeathEvent{PlayerID: n.id, ClientTime: now, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y), Items: drops}).MarshalNetworkBinary()
		match.queueBoxNPC(match.local, game.NotifyPlayerDieEvent, now, body)
		match.mirror(game.NotifyPlayerDieEvent, body)
	}
	if knocked {
		body, _ := (game.PlayerKnockedEvent{PlayerID: n.id, Time: now, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y)}).MarshalNetworkBinary()
		match.queueBoxNPC(match.local, game.NotifyPlayerKnocked, now, body)
		match.mirror(game.NotifyPlayerKnocked, body)
	}
	match.applyBoxNPCDeath(n, now, drops)
}
func npcPickupSupported(scene uint32) bool {
	return scene >= 1 && scene <= 4 || scene == 7 || scene == 8
}
func (n *boxNPCActor) pickup(scene uint32, now uint32) {
	switch scene {
	case 1: // 005CD196: rule8 NPC consumes it, but receives neither capacity nor item49.
	case 2:
		n.basic[1]++
		n.power = min(n.maxPower, n.power+1)
	case 3:
		n.basic[2]++
		n.speed = min(n.maxSpeed, n.speed+1)
	case 4:
		n.sceneFourUntil = now + 30000
	case 6:
		n.capacity = n.maxCapacity
	case 7:
		n.power = n.maxPower
	case 8:
		n.speed = n.maxSpeed
	}
}
func (match *onlineMatch) collectBoxNPCPickup(n *boxNPCActor, now uint32) {
	for _, p := range match.engine.Pickups() {
		if p.State != battleengine.PickupAvailable || p.Cell != n.pos.Cell() || !npcPickupSupported(p.SceneID) {
			continue
		}
		if taken, err := match.engine.CollectRuleNPCPickup(n.id, p.SceneID, p.Cell); err != nil || !taken {
			continue
		}
		n.pickup(p.SceneID, now)
		body, _ := (game.PlayerItemEvent{PlayerID: n.id, ClientTime: now, ItemID: p.SceneID, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y)}).MarshalNetworkBinary()
		match.queueBoxNPC(match.local, game.NotifyPlayerGetItem, now, body)
		match.mirror(game.NotifyPlayerGetItem, body)
	}
}

func (match *onlineMatch) boxNPCRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	s := match.boxNPCState()
	if s == nil {
		return nil, false
	}
	if event.Schema == game.CreateNPCBossEvent {
		if sender == match.arbitrator {
			if data, err := game.ParseCreateNPCBossNetwork(event.Body); err == nil {
				if err = match.createBoxNPC(data, match.engine.ElapsedMS()); err != nil {
					match.logf("NPC creation: %v", err)
				}
			}
		}
		return nil, true
	}
	if len(event.Body) < 2 {
		return nil, false
	}
	id := binary.BigEndian.Uint16(event.Body)
	if event.Schema == game.NotifyPlayerKilled {
		q, err := game.ParsePlayerKilledEvent(event)
		if err != nil {
			return nil, false
		}
		id = q.DestinationPlayerID
	}
	n := s.actors[id]
	if n == nil {
		return nil, false
	}
	if sender != match.arbitrator && sender != n.id {
		return nil, true
	}
	switch event.Schema {
	case game.PlayerMoveSchema:
		movement, err := game.ParsePlayerMove(event.Body)
		if err != nil || n.dead || n.trapped {
			return nil, true
		}
		for _, entry := range movement.Entries {
			if entry.PlayerID != n.id {
				continue
			}
			m := entry.Move
			if n.netMoveSet && (m.TimeStamp < n.netMove.TimeStamp || int16(m.Sequence-n.netMove.Sequence) < 0) {
				continue
			}
			n.netMove = m
			n.netMoveSet = true
			n.sequence = m.Sequence
			n.speed = m.Speed
			n.path = nil
			match.interpolateBoxNPC(n, match.engine.ElapsedMS())
		}
		return nil, true
	case game.PlayerUseBomb, game.NotifyPlayerUseBomb:
		q, err := game.ParsePlayerUseBombEvent(game.GameEvent{Schema: game.PlayerUseBomb, Body: event.Body})
		if err != nil {
			return nil, true
		}
		cell := battleengine.Cell{Row: int16(q.Row), Col: int16(q.Column)}
		if _, exists := match.wireBombID(n.id, q.ClientTime, cell); exists {
			return nil, true
		}
		placed, err := match.engine.PlaceRuleNPCBomb(n.id, cell, q.Power, q.Property, q.ClientTime)
		if err != nil {
			match.logf("NPC bubble: %v", err)
			return nil, true
		}
		match.bombs[placed.BombID] = wireBomb{owner: n.id, placedAt: q.ClientTime, cell: cell}
		return []battleengine.Event{placed}, true
	case game.PlayerBeExploded:
		q, err := game.ParsePlayerExplodedEvent(event)
		if err == nil && !n.dead && !q.IsAvatar {
			n.pos = battleengine.Position{X: int32(q.PosX), Y: int32(q.PosY)}
			n.trapped = true
			n.trappedAt = q.ClientTime
			n.moving = false
			n.path = nil
			n.netMoveSet = false
		}
		return nil, true
	case game.NotifyPlayerKnocked:
		q, err := game.ParsePlayerKnockedEvent(event)
		if err == nil && !q.IsAvatar {
			match.applyBoxNPCDeath(n, q.Time, nil)
		}
		return nil, true
	case game.NotifyPlayerKilled:
		q, err := game.ParsePlayerKilledEvent(event)
		if err == nil {
			match.applyBoxNPCDeath(n, q.ClientTime, q.Items)
		}
		return nil, true
	case game.NotifyPlayerDieEvent:
		q, err := game.ParsePlayerDeathEvent(event)
		if err == nil {
			match.applyBoxNPCDeath(n, q.ClientTime, q.Items)
		}
		return nil, true
	case game.NotifyPlayerGetItem:
		q, err := game.ParsePlayerItemEvent(event)
		if err != nil || n.dead {
			return nil, true
		}
		key := [4]uint32{uint32(q.PlayerID), q.ClientTime, q.ItemID, uint32(q.PosX)<<16 | uint32(q.PosY)}
		if s.seen[key] {
			return nil, true
		}
		s.seen[key] = true
		match.engine.CollectRuleNPCPickup(n.id, q.ItemID, battleengine.Position{X: int32(q.PosX), Y: int32(q.PosY)}.Cell())
		n.pickup(q.ItemID, q.ClientTime)
		return nil, true
	}
	return nil, false
}
func (match *onlineMatch) boxNPCView() *boxNPCView {
	s := match.boxNPCState()
	if s == nil {
		return nil
	}
	view := &boxNPCView{}
	now := match.engine.ElapsedMS()
	for _, n := range s.ordered() {
		if n.dead && now >= n.deadAt+1000 {
			continue
		}
		view.Actors = append(view.Actors, boxNPCActorView{ID: n.id, Role: n.role, Team: 7, X: n.pos.X, Y: n.pos.Y, Facing: byte(battleDirection(n.facing)), Moving: n.moving, Dead: n.dead, Trapped: n.trapped})
	}
	return view
}
func (match *onlineMatch) boxNPCProxy(sender, actor uint16) bool {
	return match.box != nil && sender == match.arbitrator && match.engine.HasRuleNPCOwner(actor)
}
