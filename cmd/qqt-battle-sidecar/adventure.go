package main

// Adventure (native rule 12; docs/adventure-combat-re.md). Players and NPCs
// have HP instead of being trapped: a bubble's damage is its owner's
// pvedata bombpow (NPCs subtract their Defense), all flames in a cell add up,
// standing in fire keeps hurting, and every hit gives 3 s of protection.
// 0x10F4 carries the HP left. Each client judges its own player (bubbles
// and NPC contact, which costs the NPC's Harm); the arbitrator judges NPCs,
// sends every death (0x0FA7: the base NPCID to the server, the runtime ID to
// peers), and spawns the stage's NPCs one battlefield at a time (0x138C).
// After every NPC has spawned and died, the local player standing on the
// door cell 10 s later asks for the next stage (0x1178).
//
// Not the original yet: the PVE NPC controller (MoveStrategy 1..5 at
// 0x005BAE70, BattleStrategy, aggro, skills such as 21025) is a stand-in
// that chases a player within View cells and otherwise wanders inside
// NPCBounds; it never places bubbles. Battlefield locking (0x0061667E) is
// not played.

import (
	"fmt"
	"math/rand"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	adventureFirstNPCID     = 30001
	adventureLastNPCID      = 30199
	adventureProtectMS      = 3000 // 0x005B5202(3000) after a hit
	adventureProduceFromMS  = 3000 // components run 3 s into the round
	adventureDoorWaitMS     = 10_000
	adventureNPCDecisionMS  = 500
	adventureDropBoxRadius  = 2 // drops land in the 5x5 square around the death cell
	adventureDyingMS        = 3000
	adventureNPCDieMS       = 400
	adventurePlayerMaxSpeed = 10
)

// adventureDropRanges are rule 12 slot 6 (0x006094DA): the item IDs an NPC
// actually drops from its inventory (open intervals).
var adventureDropRanges = [][2]uint32{{401, 1001}, {24000, 24500}, {30000, 32000}, {80, 99}, {25000, 29001}, {9000, 9011}}

type advPlayer struct {
	hp, maxHP      int
	damage         int // pvedata bombpow
	protectedUntil uint32
	dyingSince     uint32
	dead           bool
}

type advNPC struct {
	boxNPCActor
	base           uint16
	info           game.PVENPC
	hp, maxHP      int
	defense, harm  int
	view           int
	bounds         mapdata.AdventureNPCBounds
	protectedUntil uint32
}

type adventureRule struct {
	stage   mapdata.AdventureStage
	field   mapdata.AdventureBattlefield
	players map[uint16]*advPlayer
	npcs    map[uint16]*advNPC
	// produced marks the stage NPCs (by XML index) already sent in 0x138C.
	produced []bool
	fieldIdx int
	ids      []uint16 // runtime ID of each XML NPC
	drops    map[uint16][]game.BossItemInfo
	outbox   []boxNPCOutbound
	rng      *rand.Rand
	seen     map[string]bool
	clearAt  uint32 // first frame with every NPC spawned and dead
	doorSent bool
	roundMS  uint32
	events   []battleengine.Event
}

type advActorView struct {
	ID             uint16 `json:"id"`
	Role           uint32 `json:"role"`
	Boss           bool   `json:"boss,omitempty"`
	X              int32  `json:"x"`
	Y              int32  `json:"y"`
	Facing         byte   `json:"facing"`
	Moving         bool   `json:"moving"`
	HP             int    `json:"hp"`
	MaxHP          int    `json:"max_hp"`
	ProtectedUntil uint32 `json:"protected_until"`
	Name           string `json:"name,omitempty"`
}

type advPlayerView struct {
	HP             int    `json:"hp"`
	MaxHP          int    `json:"max_hp"`
	ProtectedUntil uint32 `json:"protected_until"`
	DyingSince     uint32 `json:"dying_since,omitempty"`
}

type adventureView struct {
	Players map[uint16]advPlayerView `json:"players"`
	NPCs    []advActorView           `json:"npcs"`
	Field   mapdata.AdventureField   `json:"field"`
	DoorRow int                      `json:"door_row"`
	DoorCol int                      `json:"door_col"`
	Open    bool                     `json:"door_open"`
	Width   int                      `json:"width"`
	Height  int                      `json:"height"`
}

func newAdventureRule(stage mapdata.AdventureStage, field mapdata.AdventureBattlefield, begin game.GameBeginData) *adventureRule {
	rule := &adventureRule{stage: stage, field: field, players: map[uint16]*advPlayer{}, npcs: map[uint16]*advNPC{},
		produced: make([]bool, len(stage.NPCs)), drops: map[uint16][]game.BossItemInfo{}, seen: map[string]bool{},
		rng:     rand.New(rand.NewSource(int64(begin.SpawnSeed)<<32 | int64(begin.ItemSeed))),
		roundMS: begin.GameTimeMS * 1000}
	// The runtime IDs are unique per NPC (0x005E818F marks production by
	// them), while the server counts the XML NPCID (the base type).
	for i := range stage.NPCs {
		rule.ids = append(rule.ids, uint16(adventureFirstNPCID+i))
	}
	return rule
}

// adventureParticipant applies the pvedata level of the courage points.
func adventureParticipant(p battleengine.Participant, level mapdata.AdventureLevel) battleengine.Participant {
	p.BombCapacity = max(level.BombNum, 1)
	p.MaxBombCapacity = max(p.MaxBombCapacity, p.BombCapacity)
	if level.Speed > 0 {
		p.SpeedRate = level.Speed
		p.MaxSpeedRate = max(p.MaxSpeedRate, level.Speed, adventurePlayerMaxSpeed)
	}
	p.BombResumeMS = uint32(level.ReSpeed*1000 + 0.5)
	return p
}

func (rule *adventureRule) currentField() mapdata.AdventureField {
	if rule.fieldIdx < len(rule.stage.Fields) {
		return rule.stage.Fields[rule.fieldIdx]
	}
	return mapdata.AdventureField{Col: 0, Row: 0, W: int(rule.field.Battlefield.Width), H: int(rule.field.Battlefield.Height)}
}

func (rule *adventureRule) living() int {
	n := 0
	for _, npc := range rule.npcs {
		if !npc.dead {
			n++
		}
	}
	return n
}

func (rule *adventureRule) allProduced() bool {
	for _, done := range rule.produced {
		if !done {
			return false
		}
	}
	return true
}

func (rule *adventureRule) orderedNPCs() []*advNPC {
	ids := make([]int, 0, len(rule.npcs))
	for id := range rule.npcs {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	out := make([]*advNPC, 0, len(ids))
	for _, id := range ids {
		out = append(out, rule.npcs[uint16(id)])
	}
	return out
}

func isAdventureNPC(id uint16) bool { return id >= adventureFirstNPCID && id <= adventureLastNPCID }

func (match *onlineMatch) adventureProxy(sender, actor uint16) bool {
	return match.adventure != nil && sender == match.arbitrator && match.adventure.npcs[actor] != nil
}

// produceNPCs is CQQTPVENPCProduceChecker 0x006162F6: with no NPC alive,
// the unproduced NPCs born inside the current battlefield (bounds
// inclusive, 0x005E80B9) are announced in one 0x138C.
func (match *onlineMatch) produceAdventureNPCs(now uint32) {
	rule := match.adventure
	if rule.living() != 0 || now < adventureProduceFromMS {
		return
	}
	f := rule.currentField()
	var batch game.CreatePVENPCEvent
	for i, npc := range rule.stage.NPCs {
		if rule.produced[i] || npc.BornRow < f.Row || npc.BornRow > f.Row+f.H || npc.BornCol < f.Col || npc.BornCol > f.Col+f.W {
			continue
		}
		boss := npc.IsBoss
		batch.NPCs = append(batch.NPCs, game.PVENPC{NPCID: uint32(rule.ids[i]), NPCBaseID: uint32(npc.NPCID), IsBoss: boss, NickName: encodeGBK(npc.NickName),
			ImageID: npc.ImageID, MoveStrategy: uint32(npc.MoveStrategy), Defense: uint32(npc.Defense), HP: uint32(npc.HP), Speed: uint32(npc.Speed),
			Harm: uint32(npc.Harm), BattleStrategy: uint32(npc.BattleStrategy), BornRow: uint32(npc.BornRow), BornCol: uint32(npc.BornCol), View: uint32(npc.View)})
		if len(batch.NPCs) == game.PVENPCMaxCount {
			break
		}
	}
	if len(batch.NPCs) == 0 {
		if !rule.allProduced() {
			// Nothing left here: the next battlefield (0x005E801B).
			rule.fieldIdx++
		}
		return
	}
	body, err := batch.MarshalNetworkBinary()
	if err != nil {
		match.logf("NOTIFY_CREATE_PVENPC: %v", err)
		return
	}
	match.queueNPC(&rule.outbox, match.local, game.NotifyCreatePVENPC, now, body)
	match.mirror(game.NotifyCreatePVENPC, body)
	match.createAdventureNPCs(batch, now)
}

// createAdventureNPCs is the 0x138C receiver 0x005FCFD3.
func (match *onlineMatch) createAdventureNPCs(batch game.CreatePVENPCEvent, now uint32) {
	rule := match.adventure
	for _, info := range batch.NPCs {
		id := uint16(info.NPCID)
		if !isAdventureNPC(id) || rule.npcs[id] != nil {
			continue
		}
		if err := match.engine.RegisterRuleNPCOwner(id); err != nil {
			match.logf("adventure NPC %d: %v", id, err)
			continue
		}
		index := int(id) - adventureFirstNPCID
		var bounds mapdata.AdventureNPCBounds
		if index >= 0 && index < len(rule.stage.NPCs) {
			bounds = rule.stage.NPCs[index].Bounds
			rule.produced[index] = true
		}
		cell := battleengine.Cell{Row: int16(info.BornRow), Col: int16(info.BornCol)}
		n := &advNPC{base: uint16(info.NPCBaseID), info: info, hp: int(info.HP), maxHP: int(info.HP), defense: int(info.Defense), harm: int(info.Harm), view: int(info.View), bounds: bounds}
		n.id, n.role, n.pos, n.facing, n.lastTick, n.speed, n.bossSpeed = id, uint16(info.ImageID), battleengine.PositionAtCellCenter(cell), 3, now, byte(min(info.Speed, 10)), true
		rule.npcs[id] = n
	}
	match.logf("adventure: %d NPCs in battlefield %d", len(batch.NPCs), rule.fieldIdx)
}

func (match *onlineMatch) adventurePlayer(id uint16) *advPlayer {
	return match.adventure.players[id]
}

// flameDamage is the sum of the flames' damage in a cell: a player's bubble
// carries its owner's bombpow, an NPC's its Harm (the NPC bubble's damage
// is not known yet, docs/adventure-combat-re.md §10.3).
func (match *onlineMatch) adventureFlameDamage(cell battleengine.Cell, now uint32) int {
	rule := match.adventure
	sum := 0
	for _, flame := range match.engine.Flames() {
		if flame.Cell != cell || flame.ImpactAtMS > now || now >= flame.ExpiresAtMS {
			continue
		}
		if p := rule.players[flame.OwnerID]; p != nil {
			sum += p.damage
		} else if n := rule.npcs[flame.OwnerID]; n != nil {
			sum += n.harm
		}
	}
	return sum
}

// burningSample is 0x0060C63A's test: 4 of the 5 sample points burn.
func (match *onlineMatch) burningSample(pos battleengine.Position, now uint32) bool {
	burning := map[battleengine.Cell]bool{}
	for _, flame := range match.engine.Flames() {
		if flame.ImpactAtMS <= now && now < flame.ExpiresAtMS {
			burning[flame.Cell] = true
		}
	}
	inside := 0
	for _, offset := range [5][2]int32{{0, 0}, {-3, -3}, {3, -3}, {-3, 3}, {3, 3}} {
		if burning[battleengine.Position{X: pos.X + offset[0], Y: pos.Y + offset[1]}.Cell()] {
			inside++
		}
	}
	return inside >= 4
}

func (match *onlineMatch) adventureTick(now uint32) {
	rule := match.adventure
	if rule == nil {
		return
	}
	arbitrator := match.isArbitrator()
	if arbitrator {
		match.produceAdventureNPCs(now)
	}
	// The local player's own damage: fire, then NPC contact (0x00615E6D).
	if actor, ok := actorByID(match.engine.Actors(), match.local); ok && actor.State == battleengine.ActorActive {
		p := rule.players[match.local]
		if p != nil && !p.dead && p.hp > 0 && now >= p.protectedUntil {
			if match.burningSample(actor.Position, now) {
				if dmg := match.adventureFlameDamage(actor.Position.Cell(), now); dmg > 0 {
					match.adventureHarmPlayer(match.local, actor.Position, max(0, p.hp-dmg), now)
				}
			}
		}
		if p != nil && !p.dead && p.hp > 0 && now >= p.protectedUntil {
			for _, n := range rule.orderedNPCs() {
				if !n.dead && n.pos.Cell() == actor.Position.Cell() {
					match.adventureHarmPlayer(match.local, actor.Position, max(0, p.hp-n.harm), now)
					break
				}
			}
		}
	}
	for _, n := range rule.orderedNPCs() {
		if n.dead {
			continue
		}
		if !arbitrator {
			match.interpolateBoxNPC(&n.boxNPCActor, now)
			continue
		}
		if now >= n.protectedUntil && match.burningSample(n.pos, now) {
			if dmg := match.adventureFlameDamage(n.pos.Cell(), now) - n.defense; dmg > 0 {
				match.adventureHarmNPC(n, max(0, n.hp-dmg), now)
			}
		}
		if n.hp < 1 {
			match.adventureNPCDeath(n, now)
			continue
		}
		match.adventureNPCMove(n, now)
		match.publishNPCMove(&rule.outbox, &n.boxNPCActor, now, false)
		n.lastTick = now
	}
	if arbitrator {
		// CQQTPlayerDieChecker 0x0060DE79: a player at HP 0 dies at once.
		for id, p := range rule.players {
			if p.hp <= 0 && !p.dead {
				match.adventurePlayerDeath(id, now)
			}
		}
	}
	match.adventureDoor(now)
	if arbitrator {
		match.flushNPC(&rule.outbox)
	}
}

func (match *onlineMatch) adventureHarmPlayer(id uint16, pos battleengine.Position, hpLeft int, now uint32) {
	harm := game.EntityHarmedEvent{ObjectID: id, Time: now, PosX: uint16(pos.X), PosY: uint16(pos.Y), LossHP: uint16(max(0, hpLeft))}
	body, err := harm.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.PlayerBeHarmed, now, now, body)
	match.mirror(game.PlayerBeHarmed, body)
	match.applyAdventureHarm(harm)
}

func (match *onlineMatch) adventureHarmNPC(n *advNPC, hpLeft int, now uint32) {
	harm := game.EntityHarmedEvent{ObjectID: n.id, Time: now, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y), LossHP: uint16(max(0, hpLeft))}
	body, err := harm.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queueNPC(&match.adventure.outbox, n.id, game.PlayerBeHarmed, now, body)
	match.mirror(game.PlayerBeHarmed, body)
	match.applyAdventureHarm(harm)
}

// applyAdventureHarm is the rule-12 branch of 0x00601FEC: only a lower HP
// counts; HP above 0 gives 3 s of protection, HP 0 starts dying.
func (match *onlineMatch) applyAdventureHarm(harm game.EntityHarmedEvent) {
	rule := match.adventure
	now := harm.Time
	if p := rule.players[harm.ObjectID]; p != nil {
		if int(harm.LossHP) >= p.hp || p.dead {
			return
		}
		p.hp = int(harm.LossHP)
		if p.hp > 0 {
			p.protectedUntil = now + adventureProtectMS
		} else if p.dyingSince == 0 {
			p.dyingSince = now
		}
		match.logf("adventure player %d HP %d/%d", harm.ObjectID, p.hp, p.maxHP)
		return
	}
	if n := rule.npcs[harm.ObjectID]; n != nil && !n.dead && int(harm.LossHP) < n.hp {
		n.hp = int(harm.LossHP)
		if n.hp > 0 {
			n.protectedUntil = now + adventureProtectMS
		}
	}
}

func (match *onlineMatch) adventurePlayerDeath(id uint16, now uint32) {
	actor, ok := actorByID(match.engine.Actors(), id)
	if !ok {
		return
	}
	death := game.PlayerDeathEvent{PlayerID: id, ClientTime: now, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y), Items: []game.GameItem{}}
	body, err := death.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.NotifyPlayerDieEvent, now, now, body)
	match.mirror(game.NotifyPlayerDieEvent, body)
	match.applyAdventurePlayerDeath(id)
}

func (match *onlineMatch) applyAdventurePlayerDeath(id uint16) {
	p := match.adventure.players[id]
	if p == nil || p.dead {
		return
	}
	p.dead, p.hp = true, 0
	events, err := match.runtime.AcceptNativeElimination(id, 0, []battleengine.Pickup{})
	if err != nil {
		match.logf("adventure death of %d: %v", id, err)
	}
	match.adventure.events = append(match.adventure.events, events...)
	match.logf("adventure player %d died", id)
}

// adventureNPCDeath sends 0x0FA7: the server gets the base NPCID by TCP,
// peers the runtime ID; drops fall in the 5x5 square around the NPC.
func (match *onlineMatch) adventureNPCDeath(n *advNPC, now uint32) {
	items := match.adventureDrops(n)
	death := game.PlayerDeathEvent{PlayerID: n.base, ClientTime: now, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y), Items: items}
	if body, err := death.MarshalNetworkBinary(); err == nil {
		match.mirror(game.NotifyPlayerDieEvent, body)
	}
	death.PlayerID = n.id
	if body, err := death.MarshalNetworkBinary(); err == nil {
		match.queueNPC(&match.adventure.outbox, match.local, game.NotifyPlayerDieEvent, now, body)
	}
	match.applyAdventureNPCDeath(n, items)
}

func (match *onlineMatch) applyAdventureNPCDeath(n *advNPC, items []game.GameItem) {
	if n.dead {
		return
	}
	n.dead, n.hp, n.moving, n.path = true, 0, false, nil
	if len(items) > 0 {
		events, err := match.engine.InstallRuleNPCDrops(n.id, pickupsFromItems(items))
		if err != nil {
			match.logf("adventure NPC %d drops: %v", n.id, err)
		} else {
			match.forgetPickups(events)
		}
	}
	match.logf("adventure NPC %d (%d) died", n.id, n.base)
}

func inDropRange(id uint32) bool {
	for _, r := range adventureDropRanges {
		if id > r[0] && id < r[1] {
			return true
		}
	}
	return false
}

// adventureDrops is slot 6 (0x006094DA) over this NPC type's 0x138A list.
func (match *onlineMatch) adventureDrops(n *advNPC) []game.GameItem {
	rule := match.adventure
	var scenes []uint32
	for _, item := range rule.drops[n.base] {
		if inDropRange(item.ItemID) {
			for i := 0; i < int(item.ItemCount); i++ {
				scenes = append(scenes, item.ItemID)
			}
		}
	}
	if len(scenes) == 0 {
		return nil
	}
	here := n.pos.Cell()
	top := battleengine.Cell{Row: max(int16(n.bounds.BeginRow), here.Row-adventureDropBoxRadius), Col: max(int16(n.bounds.BeginCol), here.Col-adventureDropBoxRadius)}
	var free []battleengine.Cell
	taken := map[battleengine.Cell]bool{}
	for _, p := range match.engine.Pickups() {
		if p.State == battleengine.PickupAvailable {
			taken[p.Cell] = true
		}
	}
	for r := top.Row; r < top.Row+5; r++ {
		for c := top.Col; c < top.Col+5; c++ {
			cell := battleengine.Cell{Row: r, Col: c}
			if tile, ok := match.engine.TileAt(cell); ok && tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied && !taken[cell] {
				free = append(free, cell)
			}
		}
	}
	var items []game.GameItem
	for _, scene := range scenes {
		if len(free) == 0 {
			break
		}
		i := rule.rng.Intn(len(free))
		items = append(items, game.GameItem{ItemID: scene, Row: byte(free[i].Row), Col: byte(free[i].Col)})
		free = append(free[:i], free[i+1:]...)
	}
	return items
}

// adventureNPCMove is the stand-in controller: chase the nearest living
// player within View cells, otherwise wander inside NPCBounds.
func (match *onlineMatch) adventureNPCMove(n *advNPC, now uint32) {
	inside := func(c battleengine.Cell) bool {
		b := n.bounds
		return b.EndRow == 0 && b.EndCol == 0 || int(c.Row) >= b.BeginRow && int(c.Row) <= b.EndRow && int(c.Col) >= b.BeginCol && int(c.Col) <= b.EndCol
	}
	if len(n.path) == 0 && now >= n.nextDecision {
		n.nextDecision = now + adventureNPCDecisionMS
		here := n.pos.Cell()
		var target *battleengine.Cell
		best := 1 << 30
		for _, a := range match.engine.Actors() {
			if a.State != battleengine.ActorActive {
				continue
			}
			c := a.Position.Cell()
			dr, dc := int(abs32(int32(c.Row-here.Row))), int(abs32(int32(c.Col-here.Col)))
			if dr <= n.view && dc <= n.view && dr+dc < best {
				best, target = dr+dc, &c
			}
		}
		var options []battleengine.Cell
		for d := byte(0); d < 4; d++ {
			next := boxStep(here, d)
			if inside(next) && match.npcOpen(next, here) {
				options = append(options, next)
			}
		}
		if len(options) > 0 {
			choice := options[match.adventure.rng.Intn(len(options))]
			if target != nil {
				for _, o := range options {
					if abs32(int32(o.Row-target.Row))+abs32(int32(o.Col-target.Col)) < abs32(int32(choice.Row-target.Row))+abs32(int32(choice.Col-target.Col)) {
						choice = o
					}
				}
			}
			n.path = []battleengine.Cell{choice}
		}
	}
	match.bossMoveNPC(&n.boxNPCActor, now)
}

// adventureDoor is 0x00615CAC.
func (match *onlineMatch) adventureDoor(now uint32) {
	rule := match.adventure
	if rule.living() != 0 || !rule.allProduced() || rule.doorSent {
		return
	}
	if rule.clearAt == 0 {
		rule.clearAt = max(now, 1)
		match.logf("adventure: every NPC is down at %d", now)
		return
	}
	if now-rule.clearAt < adventureDoorWaitMS {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorActive {
		return
	}
	if c := actor.Position.Cell(); int(c.Row) != rule.stage.LevelRow || int(c.Col) != rule.stage.LevelCol {
		return
	}
	rule.doorSent = true
	body, err := (game.GameNextMapRequest{PlayerID: match.local, ClientTime: now, ContinueID: uint32(rule.stage.ContinueID), NextMapID: rule.stage.NextMapIndex}).MarshalNetworkBinary()
	if err == nil {
		match.mirror(game.RequestGameNextMap, body) // TCP only (0x00886494)
	}
	match.logf("adventure: player %d takes the door to %d", match.local, rule.stage.NextMapIndex)
}

// adventureRemote applies peers' adventure facts. NPC packages are authored
// by the NPC (the arbitrator sends them).
func (match *onlineMatch) adventureRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	rule := match.adventure
	if rule == nil {
		return nil, false
	}
	switch event.Schema {
	case game.NotifyCreatePVENPC:
		if sender == match.arbitrator && !match.isArbitrator() {
			if batch, err := game.ParseCreatePVENPCEvent(event); err == nil {
				match.createAdventureNPCs(batch, match.engine.ElapsedMS())
			} else {
				match.logf("NOTIFY_CREATE_PVENPC: %v", err)
			}
		}
		return nil, true
	case game.PlayerBeHarmed:
		if harm, err := game.ParseEntityHarmedEvent(event); err == nil {
			match.applyAdventureHarm(harm)
		}
		return nil, true
	case game.NotifyPlayerDieEvent:
		death, err := game.ParsePlayerDeathEvent(event)
		if err != nil {
			return nil, true
		}
		if n := rule.npcs[death.PlayerID]; n != nil {
			match.applyAdventureNPCDeath(n, death.Items)
			return nil, true
		}
		if rule.players[death.PlayerID] != nil && sender == match.arbitrator {
			match.applyAdventurePlayerDeath(death.PlayerID)
			return nil, true
		}
		return nil, false
	}
	if !isAdventureNPC(sender) {
		return nil, false
	}
	n := rule.npcs[sender]
	if n == nil || match.isArbitrator() {
		return nil, n != nil
	}
	if event.Schema == game.PlayerMoveSchema {
		movement, err := game.ParsePlayerMove(event.Body)
		if err != nil || n.dead {
			return nil, true
		}
		for _, entry := range movement.Entries {
			m := entry.Move
			if entry.PlayerID != n.id || n.netMoveSet && int16(m.Sequence-n.netMove.Sequence) < 0 {
				continue
			}
			n.netMove, n.netMoveSet, n.sequence, n.speed, n.path = m, true, m.Sequence, m.Speed, nil
			match.interpolateBoxNPC(&n.boxNPCActor, match.engine.ElapsedMS())
		}
	}
	return nil, true
}

// handleAdventureTCP takes the server's 0x138A drop table.
func (match *onlineMatch) handleAdventureTCP(schema uint16, body []byte) bool {
	if match.adventure == nil || schema != game.CreatePVENPCBossEvent {
		return false
	}
	table, err := game.ParseCreatePVENPCBossNetwork(body)
	if err != nil {
		match.logf("CREATE_PVENPC_BOSS: %v", err)
		return true
	}
	for _, group := range table.Bosses {
		match.adventure.drops[group.BossID] = group.NormalItems
		for _, item := range group.NormalItems {
			_ = match.engine.AddBossRewardScene(item.ItemID)
		}
	}
	return true
}

func (match *onlineMatch) adventureView() *adventureView {
	rule := match.adventure
	if rule == nil {
		return nil
	}
	view := &adventureView{Players: map[uint16]advPlayerView{}, Field: rule.currentField(), DoorRow: rule.stage.LevelRow, DoorCol: rule.stage.LevelCol,
		Open: rule.clearAt != 0, Width: int(rule.field.Battlefield.Width), Height: int(rule.field.Battlefield.Height)}
	for id, p := range rule.players {
		view.Players[id] = advPlayerView{HP: p.hp, MaxHP: p.maxHP, ProtectedUntil: p.protectedUntil, DyingSince: p.dyingSince}
	}
	for _, n := range rule.orderedNPCs() {
		if n.dead {
			continue
		}
		view.NPCs = append(view.NPCs, advActorView{ID: n.id, Role: n.info.ImageID, Boss: n.info.IsBoss, X: n.pos.X, Y: n.pos.Y, Facing: byte(battleDirection(n.facing)),
			Moving: n.moving, HP: n.hp, MaxHP: n.maxHP, ProtectedUntil: n.protectedUntil, Name: decodeGBK(n.info.NickName)})
	}
	return view
}

func (match *onlineMatch) takeAdventureEvents() []battleengine.Event {
	if match.adventure == nil {
		return nil
	}
	events := match.adventure.events
	match.adventure.events = nil
	return events
}

func encodeGBK(text string) []byte {
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	if err != nil {
		return nil
	}
	if len(encoded) > 20 {
		encoded = encoded[:20]
	}
	return encoded
}

func decodeGBK(raw []byte) string {
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		return fmt.Sprintf("%x", raw)
	}
	return string(decoded)
}
