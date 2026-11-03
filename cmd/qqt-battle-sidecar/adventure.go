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
// NPCs follow 0x005BAE70 (docs/adventure-npc-ai-re.md): enmity, the
// battle switch, a random step (MoveStrategy 3) or a chase (4); they never
// place bubbles. Their skills (props cast with 0x0FB0/0x10F7) are not
// played yet. The battlefield lock flashes flame255 once when it locks; the
// flash on bumping its edge (0x005F38F9) is not played.

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
	// 0x005E7A33 numbers a stage's NPCs 30300 + XML order.
	adventureFirstNPCID     = 30300
	adventureLastNPCID      = 30399
	adventureProtectMS      = 3000 // 0x005B5202(3000) after a hit
	adventureProduceFromMS  = 3000 // components run 3 s into the round
	adventureDoorWaitMS     = 10_000
	adventureDropBoxRadius  = 2 // drops land in the 5x5 square around the death cell
	adventurePlayerMaxSpeed = 10
)

// adventureDropRanges are rule 12 slot 6 (0x006094DA): the item IDs an NPC
// actually drops from its inventory (open intervals).
var adventureDropRanges = [][2]uint32{{401, 1001}, {24000, 24500}, {30000, 32000}, {80, 99}, {25000, 29001}, {9000, 9011}}

type advPlayer struct {
	level          int // pvedata level (P+0x2b4), the bubble enmity factor
	hp, maxHP      int
	damage         int // pvedata bombpow
	protectedUntil uint32
	dyingSince     uint32
	dead           bool
	// NPC skill control effects on the player (adventure_skill.go).
	moveLocks, bombLocks, reversed int
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
	// The controller (docs/adventure-npc-ai-re.md): enmity by player,
	// battle state, move strategy (saved while fighting), target and the
	// decision counter of the move command.
	enmity                  map[uint16]int
	battle                  bool
	strategy, savedStrategy int
	target                  uint16
	counter                 int
	route                   []battleengine.Cell // cells from here to the goal
	// Skills (adventure_skill.go): when the battle began, the end of the
	// current prepare stop, the last fire by prop Tier, and the move locks
	// of CanUseMoveKey=0 effects.
	battleSince    uint32
	preparingUntil uint32
	lastFire       map[int]uint32
	moveLocks      int
}

type adventureRule struct {
	stage   mapdata.AdventureStage
	field   mapdata.AdventureBattlefield
	players map[uint16]*advPlayer
	bubbles map[uint32]bool // player bubbles already counted for enmity
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
	// The local player's battlefield lock and when its flame255 flash began.
	locked      bool
	lockFlashAt uint32
	doorSent    bool
	roundMS     uint32
	events      []battleengine.Event
	props       map[uint32]mapdata.AdventureProp // config/errorPPP.ini
	casts       []*advCast
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
	Locked  bool                     `json:"locked,omitempty"`
	FlashAt uint32                   `json:"flash_at,omitempty"`
	Casts   []advCastView            `json:"casts,omitempty"`
}

func newAdventureRule(stage mapdata.AdventureStage, field mapdata.AdventureBattlefield, begin game.GameBeginData) *adventureRule {
	rule := &adventureRule{stage: stage, field: field, players: map[uint16]*advPlayer{}, npcs: map[uint16]*advNPC{}, bubbles: map[uint32]bool{},
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
		n := &advNPC{base: uint16(info.NPCBaseID), info: info, hp: int(info.HP), maxHP: int(info.HP), defense: int(info.Defense), harm: int(info.Harm), view: int(info.View), bounds: bounds,
			enmity: map[uint16]int{}, strategy: int(info.MoveStrategy), lastFire: map[int]uint32{}}
		n.id, n.role, n.pos, n.facing, n.lastTick, n.speed, n.bossSpeed = id, uint16(info.ImageID), battleengine.PositionAtCellCenter(cell), 3, now, byte(min(info.Speed, 10)), true
		rule.npcs[id] = n
	}
	match.logf("adventure: %d NPCs in battlefield %d", len(batch.NPCs), rule.fieldIdx)
}

// advanceAdventureField is 0x005B8ECC/0x005E801B on every client: with no
// NPC alive and none left to spawn in the current battlefield (0x005E8279,
// right/bottom edges exclusive), the next battlefield starts; after the
// last one the field is the whole map. The lock is released.
func (match *onlineMatch) advanceAdventureField(now uint32) {
	rule := match.adventure
	if rule.living() != 0 || rule.fieldIdx >= len(rule.stage.Fields) || now < adventureProduceFromMS {
		return
	}
	f := rule.currentField()
	for i, npc := range rule.stage.NPCs {
		if !rule.produced[i] && npc.BornRow >= f.Row && npc.BornRow < f.Row+f.H && npc.BornCol >= f.Col && npc.BornCol < f.Col+f.W {
			return
		}
	}
	rule.fieldIdx++
	if rule.locked {
		rule.locked = false
		_ = match.engine.SetActorFieldLock(match.local, 0, 0, 0, 0)
	}
	match.logf("adventure: Enter Next Battle %d", rule.fieldIdx)
}

// lockAdventureField is 0x0061667E: the local player stepping into the
// current battlefield locks it there and lights flame255 on every cell.
func (match *onlineMatch) lockAdventureField(now uint32) {
	rule := match.adventure
	if rule.locked || rule.fieldIdx >= len(rule.stage.Fields) {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorActive {
		return
	}
	f := rule.currentField()
	c := actor.Position.Cell()
	if int(c.Col) < f.Col || int(c.Col) >= f.Col+f.W || int(c.Row) < f.Row || int(c.Row) >= f.Row+f.H {
		return
	}
	if err := match.engine.SetActorFieldLock(match.local, int16(f.Row), int16(f.Col), int16(f.H), int16(f.W)); err != nil {
		match.logf("battlefield lock: %v", err)
		return
	}
	rule.locked, rule.lockFlashAt = true, now
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
	match.advanceAdventureField(now)
	if arbitrator {
		match.produceAdventureNPCs(now)
		match.adventureEnmity()
	}
	match.lockAdventureField(now)
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
		match.adventureNPCTick(n, now)
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
	match.adventureCastTick(now)
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

// npcBoundsContain is 0x005BAD90: NPCBounds, both edges included.
func (n *advNPC) boundsContain(c battleengine.Cell) bool {
	b := n.bounds
	if b.EndRow == 0 && b.EndCol == 0 {
		return true
	}
	return int(c.Row) >= b.BeginRow && int(c.Row) <= b.EndRow && int(c.Col) >= b.BeginCol && int(c.Col) <= b.EndCol
}

func (match *onlineMatch) livingAdventurePlayers() []battleengine.Actor {
	var out []battleengine.Actor
	for _, a := range match.engine.Actors() {
		if p := match.adventure.players[a.PlayerID]; p != nil && !p.dead && a.State != battleengine.ActorEliminated {
			out = append(out, a)
		}
	}
	return out
}

// addEnmity is 0x005BC67A.
func (n *advNPC) addEnmity(player uint16, v int) {
	old, exists := n.enmity[player]
	switch {
	case !exists:
		n.enmity[player] = max(v, 0)
	case v < 0 && -v > old:
		n.enmity[player] = 0
	default:
		n.enmity[player] = old + v
	}
}

// adventureEnmity runs the arbitrator's per-frame enmity sources: a player
// entering an NPC's view with an existing zero entry gains 20 (0x00616143),
// and a player bubble placed in the current battlefield gives every NPC in
// battle 5 x the placer's level (0x00610BA0 / 0x0060857C).
func (match *onlineMatch) adventureEnmity() {
	rule := match.adventure
	players := match.livingAdventurePlayers()
	for _, n := range rule.orderedNPCs() {
		if n.dead {
			continue
		}
		for _, a := range players {
			c, nc := a.Position.Cell(), n.pos.Cell()
			if !n.boundsContain(c) || int(abs32(int32(c.Col-nc.Col))) > n.view || int(abs32(int32(c.Row-nc.Row))) > n.view {
				continue
			}
			if v, exists := n.enmity[a.PlayerID]; exists && v == 0 {
				n.addEnmity(a.PlayerID, 20)
			}
		}
	}
	f := rule.currentField()
	live := map[uint32]bool{}
	for _, bomb := range match.engine.Bombs() {
		live[bomb.ID] = true
		p := rule.players[bomb.OwnerID]
		if p == nil || bomb.RuleOwned || rule.bubbles[bomb.ID] {
			continue
		}
		rule.bubbles[bomb.ID] = true
		if c := bomb.Cell; int(c.Col) < f.Col || int(c.Col) > f.Col+f.W-1 || int(c.Row) < f.Row || int(c.Row) > f.Row+f.H-1 {
			continue
		}
		for _, n := range rule.npcs {
			if !n.dead && n.battle {
				n.addEnmity(bomb.OwnerID, 5*p.level)
			}
		}
	}
	for id := range rule.bubbles {
		if !live[id] {
			delete(rule.bubbles, id)
		}
	}
}

// adventureNPCTick is the move command 0x006112AF for a PVE NPC: decide
// every 7th frame (at once again after a 9), then walk.
func (match *onlineMatch) adventureNPCTick(n *advNPC, now uint32) {
	if n.counter <= bossDecideFrames {
		n.counter++
	} else if now < n.preparingUntil || n.moveLocks > 0 {
		n.path = nil
	} else if d := match.adventureNPCDecide(n, now); d == bossDirStop {
		n.path = nil
	} else {
		n.counter = 0
		n.path = []battleengine.Cell{bossStep(n.pos.Cell(), d)}
	}
	match.bossMoveNPC(&n.boxNPCActor, now)
}

// adventureNPCDecide is vtbl+0x70 0x005BAE70: the battle switch, then the
// skill check 0x005BBAFA, then the move strategy (PVE NPCs never place
// bubbles).
func (match *onlineMatch) adventureNPCDecide(n *advNPC, now uint32) int {
	players := match.livingAdventurePlayers()
	// 0x005BADE5 with GEnmity 0x005BBBFF.
	total := 0
	for _, a := range players {
		if n.boundsContain(a.Position.Cell()) {
			if _, exists := n.enmity[a.PlayerID]; !exists {
				n.enmity[a.PlayerID] = 0
			}
			total += n.enmity[a.PlayerID]
		}
	}
	switch {
	case n.battle && total == 0:
		n.battle, n.strategy, n.route = false, n.savedStrategy, nil
	case !n.battle && total >= 1:
		n.battle, n.savedStrategy, n.strategy, n.route, n.battleSince = true, n.strategy, 4, nil, now
	}
	if match.adventureTrySkill(n, now) {
		return bossDirStop
	}
	here := n.pos.Cell()
	if len(n.route) > 1 && match.adventureRouteValid(n, here) {
		return directionBetween(here, n.route[1])
	}
	switch n.strategy {
	case 4:
		return match.adventureChase(n, here, players)
	default:
		// 3 (every stage NPC); 1/2 patrols are not in any stage file and
		// fall back to 3 anyway when their path fails.
		goal := here
		switch match.adventure.rng.Intn(1000) & 3 {
		case 0:
			goal.Col--
		case 1:
			goal.Col++
		case 2:
			goal.Row--
		case 3:
			goal.Row++
		}
		n.route = match.adventureFindPath(n, here, goal)
	}
	if len(n.route) < 2 {
		n.route = nil
		return bossDirStop
	}
	return directionBetween(here, n.route[1])
}

// adventureChase is MoveStrategy 4 (0x005BB33A) with the target picked by
// BattleStrategy (0x005BC134: 0 lowest enmity, 1 highest, 2 none).
func (match *onlineMatch) adventureChase(n *advNPC, here battleengine.Cell, players []battleengine.Actor) int {
	if p := match.adventure.players[n.target]; n.target != 0 && (p == nil || p.dead) {
		n.addEnmity(n.target, -10_000_000)
	}
	n.target = 0
	best := 0
	for _, a := range players {
		if !n.boundsContain(a.Position.Cell()) {
			continue
		}
		v := n.enmity[a.PlayerID]
		switch n.info.BattleStrategy {
		case 0:
			if n.target == 0 || v < best {
				n.target, best = a.PlayerID, v
			}
		case 1:
			if n.target == 0 || v > best {
				n.target, best = a.PlayerID, v
			}
		}
	}
	if n.target == 0 {
		n.route = nil
		return bossDirStop
	}
	target, _ := actorByID(match.engine.Actors(), n.target)
	n.route = match.adventureFindPath(n, here, target.Position.Cell())
	if len(n.route) < 2 {
		n.route = nil
		return bossDirStop
	}
	return directionBetween(here, n.route[1])
}

// adventureRouteValid is 0x005BB7F6: in battle the target must still stand
// on the goal; no cell may hold a bubble or a blocking element.
func (match *onlineMatch) adventureRouteValid(n *advNPC, here battleengine.Cell) bool {
	for len(n.route) > 0 && n.route[0] != here {
		n.route = n.route[1:]
	}
	if len(n.route) < 2 {
		return false
	}
	if n.battle {
		target, ok := actorByID(match.engine.Actors(), n.target)
		if !ok || target.Position.Cell() != n.route[len(n.route)-1] {
			return false
		}
	}
	for _, c := range n.route[1:] {
		if !match.npcOpen(c, here) {
			return false
		}
	}
	return true
}

// adventureFindPath is the NPC pathfinder (0x005F10D3) kept inside
// NPCBounds; bubbles and blocking elements are obstacles, players and NPCs
// are not. Unit steps, so a breadth-first search finds the same lengths as
// the original A*; ties may differ.
func (match *onlineMatch) adventureFindPath(n *advNPC, from, goal battleengine.Cell) []battleengine.Cell {
	if from == goal || !n.boundsContain(goal) || !match.npcOpen(goal, from) {
		return nil
	}
	previous := map[battleengine.Cell]battleengine.Cell{from: from}
	queue := []battleengine.Cell{from}
	for pops := 0; len(queue) > 0 && pops < 500; pops++ {
		c := queue[0]
		queue = queue[1:]
		if c == goal {
			var path []battleengine.Cell
			for ; c != from; c = previous[c] {
				path = append([]battleengine.Cell{c}, path...)
			}
			return append([]battleengine.Cell{from}, path...)
		}
		for _, d := range [4]int{1, 3, 2, 0} { // up, down, left, right (0x005F1410)
			next := bossStep(c, d)
			if _, seen := previous[next]; seen || !n.boundsContain(next) || !match.npcOpen(next, from) {
				continue
			}
			previous[next] = c
			queue = append(queue, next)
		}
	}
	return nil
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
	if (event.Schema == game.NotifyPlayerUseItem || event.Schema == game.NotifyPropTrigger) && (isAdventureNPC(sender) || sender == match.arbitrator) &&
		match.adventureSkillRemote(event) {
		return nil, true
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
		Open: rule.clearAt != 0, Width: int(rule.field.Battlefield.Width), Height: int(rule.field.Battlefield.Height),
		Locked: rule.locked, FlashAt: rule.lockFlashAt, Casts: match.adventureCastView()}
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
