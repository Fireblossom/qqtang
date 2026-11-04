package main

// Boss overlay on rule-1/2 maps (docs/boss-lifecycle-re.md). The server
// sends CREATE_NPC_BOSS (0x1389) shortly after GAME_BEGIN; every client
// creates the same Boss (0x005FCBA0 -> 0x005CB3B3/0x005AFDFA) on the first
// spawn point no player uses. The arbitrator runs its controller, decides
// explosion hits (0x00607DDC/0x00607A23 -> 0x10F4), the partial drops after a
// hit (0x116B, rule slot 7 0x005F4A7A) and the death (0x0FA7, slot 6
// 0x005F4B16), and kills trapped players it touches (0x0060C148). Receivers
// take exactly one HP per 0x10F4 and are immune for 3 s (0x00601FEC).

import (
	"fmt"
	"math/rand"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	bossFirstID = 30001
	bossLastID  = 30199
	// 0x00601FEC -> 0x005B5202(3000): immunity and flash2 after a hit.
	bossHitProtectionMS = 3000
	// 0x005AFDFA drops the whole NormalItems list beyond 10 entries.
	bossMaxNormalItems = 10
	// 0x0060C148: contact range on both axes.
	bossContactPixels = 41
	// 0x005B8574 returns at most 64 drop cells.
	bossMaxDrops = 64
)

type bossActor struct {
	boxNPCActor
	info           game.CompetitiveBossInfo
	hp             int
	bornAt         uint32
	birthFrameAt   uint32
	birth          bossBirthState
	protectedUntil uint32
	normal         []game.BossItemInfo // NormalItems still to drop
	ai             bossController
	// The demon transformation of skill 3 (item 110): until when, and the
	// speed/capacity it replaced.
	avatarUntil              uint32
	savedSpeed, savedBubbles byte
}

// bossGreetings are the lines in 0x005AE672's greeting branch, as GBK
// bytes from 0x007F2A00/04/10 (the original's four leading spaces kept).
// 0x005FDA38 shows nothing on receipt; the sailor has no line.
var bossGreetings = map[uint16]string{
	24: "\x20\x20\x20\x20\xce\xd2\xd3\xd0\xd2\xbb\xb8\xf6\xb4\xf3\xc3\xd8\xc3\xdc\xa3\xac\xbf\xc9\xc8\xdd\xb2\xbb\xb5\xc3\xc4\xe3\xc3\xc7\xc0\xb4\xb4\xf2\xc8\xc5\xa3\xa1",                                                                                                                                                                                                                                                                         // 我有一个大秘密，可容不得你们来打扰！
	28: "\x20\x20\x20\x20\xd5\xe2\xc9\xed\xd2\xc2\xb7\xfe\xba\xdc\xba\xcf\xc9\xed\xb0\xa1\xa3\xa1",                                                                                                                                                                                                                                                                                                                                                 // 这身衣服很合身啊！
	34: "\x20\x20\x20\x20\xbb\xb6\xd3\xad\xb8\xf7\xce\xbb\xdd\xb0\xc1\xd9\xb2\xc6\xb8\xbb\xb5\xc4\xb9\xac\xb5\xee\xa3\xac\xd5\xe2\xc0\xef\xb5\xc4\xc3\xbf\xb8\xf6\xd5\xe4\xb1\xa6\xb6\xbc\xbd\xfe\xc8\xbe\xd7\xc5\xd2\xf3\xba\xec\xb5\xc4\xcf\xca\xd1\xaa\xa3\xac\xbd\xf1\xcc\xec\xc4\xe3\xc3\xc7\xb5\xc4\xcf\xca\xd1\xaa\xbd\xab\xca\xb9\xd5\xe2\xd0\xa9\xd5\xe4\xb1\xa6\xbb\xc0\xb7\xa2\xb3\xf6\xb8\xfc\xc3\xd4\xc8\xcb\xb5\xc4\xb9\xe2\xb2\xca", // 欢迎各位莅临财富的宫殿……
}

func (match *onlineMatch) bossGreeting(b *bossActor, now uint32) {
	words, ok := bossGreetings[b.role]
	if !ok {
		return
	}
	body, err := (game.NPCTalkEvent{ObjectID: b.id, Words: []byte(words)}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queueNPC(&match.boss.outbox, b.id, game.NotifyNPCTalk, now, body)
	match.mirror(game.NotifyNPCTalk, body)
}

// bossController is the arbitrator's AI (FUN_005df20b's controller at +0x888).
type bossController interface {
	tick(match *onlineMatch, b *bossActor, now uint32)
}

type bossRule struct {
	moveCommand npcMoveCommand
	actors      map[uint16]*bossActor
	outbox      []boxNPCOutbound
	// The interleaved spawn table (0x005C18E2): A0, B0, A1, B1, ..., then the
	// rest of A, then the rest of B. Boss i stands on entry [players + i].
	spawns  []battleengine.Cell
	players int
	rng     *rand.Rand
	seen    map[string]bool
	events  []battleengine.Event // local outcomes for the next step's result
	flights []bossFlight         // skill objects in the air
	// Pirate skill 6's bubbles flying in along their rows, for drawing.
	bombFlights map[uint32]kickFlight
	// The local player's bubble lock: skill 5 adds one, skill 8 clears it,
	// a Boss's death adds one (P+0x28c).
	bubbleLock int
	// 0x005B9390 jumps the game clock to 10 s before the end when a Boss dies
	// (recordings show the timer at 00:09 right after); only the shown end
	// moves here, the scene clock stays shared with the server.
	roundMS, endsAt, lastDeathAt uint32
}

func competitiveRoundMS(entry mapdata.CompetitiveMap) uint32 {
	if spec, ok := mapdata.LookupCompetitiveRuleByNativeID(entry.NativeRule); ok {
		return spec.RoundDurationMS
	}
	return 0
}

type bossActorView struct {
	ID             uint16 `json:"id"`
	Role           uint16 `json:"role"`
	X              int32  `json:"x"`
	Y              int32  `json:"y"`
	Facing         byte   `json:"facing"`
	Moving         bool   `json:"moving"`
	HP             int    `json:"hp"`
	MaxHP          int    `json:"max_hp"`
	Born           uint32 `json:"born"`
	BirthAge       uint32 `json:"birth_age"`
	BirthMode      byte   `json:"birth_mode"`
	ProtectedUntil uint32 `json:"protected_until"`
	AvatarRole     uint16 `json:"avatar_role,omitempty"`
}

type bossView struct {
	Actors      []bossActorView       `json:"actors"`
	Flights     []bossFlightView      `json:"flights,omitempty"`
	BombFlights map[uint32]kickFlight `json:"bomb_flights,omitempty"`
	BubbleLock  bool                  `json:"bubble_lock,omitempty"`
	EndsAt      uint32                `json:"ends_at,omitempty"`
}

// bossSpeedTable is 0x007F4988, the NPCs' speed per level in pixels per
// 25 ms (players use 0x007F495C).
var bossSpeedTable = [11]float64{0, 1, 2.5, 3.077, 3.333, 4, 5, 5.714, 6.667, 8, 13.333}

func bossPixelsPerSecond(level byte) int32 {
	return int32(bossSpeedTable[min(int(level), len(bossSpeedTable)-1)]*40 + 0.5)
}

// newBossRule prepares the overlay for a map that can host a Boss; it stays
// empty until the server's CREATE_NPC_BOSS arrives.
func newBossRule(entry mapdata.CompetitiveMap, begin game.GameBeginData) *bossRule {
	if len(mapdata.LookupCompetitiveBossCandidates(entry.ID)) == 0 {
		return nil
	}
	rule := &bossRule{actors: map[uint16]*bossActor{}, players: len(begin.Players), seen: map[string]bool{}, bombFlights: map[uint32]kickFlight{},
		roundMS: competitiveRoundMS(entry),
		rng:     rand.New(rand.NewSource(int64(begin.SpawnSeed)<<32 | int64(begin.ItemSeed)))}
	a, b := entry.SpawnGroupA, entry.SpawnGroupB
	cell := func(c mapdata.CompetitiveCell) battleengine.Cell {
		return battleengine.Cell{Row: int16(c.Row), Col: int16(c.Col)}
	}
	for i := 0; i < max(len(a), len(b)); i++ {
		if i < len(a) && i < len(b) {
			rule.spawns = append(rule.spawns, cell(a[i]), cell(b[i]))
		}
	}
	for i := len(b); i < len(a); i++ {
		rule.spawns = append(rule.spawns, cell(a[i]))
	}
	for i := len(a); i < len(b); i++ {
		rule.spawns = append(rule.spawns, cell(b[i]))
	}
	return rule
}

func (rule *bossRule) ordered() []*bossActor {
	ids := make([]int, 0, len(rule.actors))
	for id := range rule.actors {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	out := make([]*bossActor, 0, len(ids))
	for _, id := range ids {
		out = append(out, rule.actors[uint16(id)])
	}
	return out
}

func isBossID(id uint16) bool { return id >= bossFirstID && id <= bossLastID }

func (match *onlineMatch) bossProxy(sender, actor uint16) bool {
	return match.boss != nil && sender == match.arbitrator && match.boss.actors[actor] != nil
}

// createBosses is 0x005FCBA0 for rule 1/2: Row/Col are ignored.
func (match *onlineMatch) createBosses(data game.CreateNPCBoss, now uint32) {
	rule := match.boss
	if rule == nil {
		match.logf("CREATE_NPC_BOSS on a map without a Boss overlay")
		return
	}
	for index, info := range data.Bosses {
		if _, exists := rule.actors[info.BossID]; exists {
			continue
		}
		if !isBossID(info.BossID) || info.AIType != 3 && info.AIType != 5 {
			match.logf("Boss %d AIType %d is not a rule-1/2 Boss", info.BossID, info.AIType)
			return
		}
		at := rule.players + index
		if at >= len(rule.spawns) {
			match.logf("Boss %d has no spawn point (%d players, %d points)", info.BossID, rule.players, len(rule.spawns))
			return
		}
		if err := match.engine.RegisterRuleNPCOwner(info.BossID); err != nil {
			match.logf("Boss %d: %v", info.BossID, err)
			return
		}
		if len(info.NormalItems) > bossMaxNormalItems {
			info.NormalItems = nil
		}
		for _, item := range append(append([]game.BossItemInfo(nil), info.NormalItems...), info.OutfitItems...) {
			if err := match.engine.AddBossRewardScene(item.ItemID); err != nil {
				match.logf("Boss %d reward %d: %v", info.BossID, item.ItemID, err)
			}
		}
		cell := rule.spawns[at]
		born := now
		if match.actorFrameTimeSet {
			born = match.actorFrameTime
		}
		b := &bossActor{info: info, bornAt: born, birthFrameAt: born, birth: newBossBirthState(), normal: append([]game.BossItemInfo(nil), info.NormalItems...)}
		b.id, b.role, b.pos, b.facing, b.lastTick = info.BossID, uint16(info.RoleID), battleengine.PositionAtCellCenter(cell), 3, now
		// 0x005AA58D writes nothing when any value is out of range.
		if info.Bubble <= 8 && info.Power >= 1 && info.Power <= 9 && info.Rate >= 1 && info.Rate <= 8 && info.HP <= 10 {
			b.capacity, b.power, b.speed, b.hp = byte(info.Bubble), byte(info.Power), byte(info.Rate), int(info.HP)
		}
		b.maxCapacity, b.maxPower, b.maxSpeed = 8, 9, 8
		b.ai = newBossController(info)
		rule.actors[info.BossID] = b
		match.logf("Boss %d role %d AIType %d HP %d spawned at %d,%d", info.BossID, info.RoleID, info.AIType, b.hp, cell.Row, cell.Col)
	}
}

const (
	// 0x0060546B grants an item without a ground check when the 0x0FAC time
	// carries this mark; the demon avatar is role 42 (object/player/player42).
	bossGrantTimeMark   = 1_000_000
	bossAvatarRole      = 42
	bossAvatarMS        = 30_000
	bossAvatarSpeed     = 8
	bossAvatarBubbles   = 8
	bossAvatarProtectMS = 3000
)

func (b *bossActor) transformed() bool { return b.avatarUntil != 0 }

func (b *bossActor) avatarRole() uint16 {
	if b.transformed() {
		return bossAvatarRole
	}
	return 0
}

// bossTransform installs the demon (0x00611BEB): 30 s, speed level 8, up to
// 8 bubbles, power kept, 3 s of immunity.
func (match *onlineMatch) bossTransform(b *bossActor, now uint32) {
	if !b.transformed() {
		b.savedSpeed, b.savedBubbles = b.speed, b.capacity
	}
	b.avatarUntil = now + bossAvatarMS
	b.speed, b.capacity = bossAvatarSpeed, bossAvatarBubbles
	b.protectedUntil = max(b.protectedUntil, now+bossAvatarProtectMS)
	match.logf("Boss %d transforms until %d", b.id, b.avatarUntil)
}

// bossUntransform takes the demon off: on expiry (3 s immunity, 0x005F78BF)
// or when a hit removes it (no HP, no drop; only the arbitrator, which
// controls the Boss, adds 3 s of immunity in 0x005ADF5A).
func (match *onlineMatch) bossUntransform(b *bossActor, now uint32, protect bool) {
	if !b.transformed() {
		return
	}
	b.avatarUntil = 0
	b.speed, b.capacity = b.savedSpeed, b.savedBubbles
	if protect {
		b.protectedUntil = max(b.protectedUntil, now+bossAvatarProtectMS)
	}
	match.logf("Boss %d loses its transformation at %d", b.id, now)
}

func (match *onlineMatch) bossTick(now uint32, events []battleengine.Event) {
	rule := match.boss
	if rule == nil || len(rule.actors) == 0 {
		return
	}
	if now <= battleengine.NativeRoundStartClockMS {
		return
	}
	match.landBossFlights(now)
	// Once every player is dead the server concludes the round; a Boss that
	// kept bombing would only send mirrors that arrive after settlement.
	anyoneLeft := false
	for _, actor := range match.engine.Actors() {
		anyoneLeft = anyoneLeft || actor.State != battleengine.ActorEliminated
	}
	if !anyoneLeft {
		return
	}
	exploded := false
	for _, event := range events {
		if event.Kind == battleengine.EventBombExploded {
			exploded = true
		}
	}
	for _, b := range rule.ordered() {
		if b.dead {
			continue
		}
		if b.transformed() && now >= b.avatarUntil {
			match.bossUntransform(b, now, true)
		}
		if !match.isArbitrator() {
			match.interpolateBoxNPC(&b.boxNPCActor, now)
			continue
		}
		if b.birth.mode == 9 {
			b.lastTick = now
			continue
		}
		if now-b.lastTick > 4*tickMS {
			// A new arbitrator takes the Boss over from its interpolated state.
			b.lastTick = now - tickMS
		}
		if exploded && match.bossExplosionHit(b, now) {
			continue
		}
		if b.ai != nil {
			b.ai.tick(match, b, now)
		}
		match.bossContact(b, now)
		match.publishNPCMove(&rule.outbox, &b.boxNPCActor, now, false)
		b.lastTick = now
	}
	if match.isArbitrator() {
		match.flushNPC(&rule.outbox)
	}
}

// bossMove walks the Boss along b.path at its native speed (shared by the
// controllers).
func (match *onlineMatch) bossMove(b *bossActor, now uint32) {
	match.bossMoveNPC(&b.boxNPCActor, now)
}

// bossMoveNPC walks any scripted NPC along its path by the NPC speed table.
func (match *onlineMatch) bossMoveNPC(b *boxNPCActor, now uint32) {
	dt := now - b.lastTick
	b.moving = false
	b.fraction += uint32(bossPixelsPerSecond(b.speed)) * dt
	travel := int32(b.fraction / 1000)
	b.fraction %= 1000
	for len(b.path) > 0 && travel > 0 {
		targetCell := b.path[0]
		target := battleengine.PositionAtCellCenter(targetCell)
		if !match.npcOpen(targetCell, b.pos.Cell()) {
			b.path = nil
			break
		}
		dx, dy := target.X-b.pos.X, target.Y-b.pos.Y
		if dx == 0 && dy == 0 {
			b.path = b.path[1:]
			continue
		}
		b.moving = true
		if dx != 0 {
			step := min(travel, abs32(dx))
			b.facing = 0
			if dx < 0 {
				step, b.facing = -step, 2
			}
			b.pos.X += step
			travel -= abs32(step)
		} else {
			step := min(travel, abs32(dy))
			b.facing = 3
			if dy < 0 {
				step, b.facing = -step, 1
			}
			b.pos.Y += step
			travel -= abs32(step)
		}
		if b.pos == target {
			b.path = b.path[1:]
		}
	}
}

func (b *bossActor) vulnerable(now uint32) bool {
	return !b.dead && b.hp > 0 && b.birth.mode != 9 && now >= b.protectedUntil
}

// bossExplosionHit is 0x00607DDC: at least 4 of the 5 sample points (the
// centre and the corners 3 px away) must be in burning cells.
func (match *onlineMatch) bossExplosionHit(b *bossActor, now uint32) bool {
	if !b.vulnerable(now) {
		return false
	}
	burning := map[battleengine.Cell]int{}
	for _, flame := range match.engine.Flames() {
		if flame.ImpactAtMS <= now && now < flame.ExpiresAtMS {
			burning[flame.Cell]++
		}
	}
	inside := 0
	for _, offset := range [5][2]int32{{0, 0}, {-3, -3}, {3, -3}, {-3, 3}, {3, 3}} {
		if burning[battleengine.Position{X: b.pos.X + offset[0], Y: b.pos.Y + offset[1]}.Cell()] > 0 {
			inside++
		}
	}
	if inside < 4 {
		return false
	}
	// LossHP is the sum of the flame objects' +0x24 in the Boss's cell; the
	// captures show 500 each. Rule-1/2 receivers ignore it.
	harm := game.EntityHarmedEvent{ObjectID: b.id, Time: now, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y), IsAvatar: b.transformed(), LossHP: uint16(min(burning[b.pos.Cell()], 100) * 500)}
	body, err := harm.MarshalNetworkBinary()
	if err != nil {
		return false
	}
	match.queueNPC(&match.boss.outbox, b.id, game.PlayerBeHarmed, now, body)
	match.mirror(game.PlayerBeHarmed, body)
	if harm.IsAvatar {
		match.bossUntransform(b, now, true)
		return false
	}
	match.bossHarmed(b, now)
	if b.hp >= 1 {
		match.bossHitDrops(b, now)
		return false
	}
	match.bossDeath(b, now)
	return true
}

// bossHarmed is the receiver side shared by every client: one HP per 0x10F4.
func (match *onlineMatch) bossHarmed(b *bossActor, now uint32) {
	b.hp--
	b.protectedUntil = now + bossHitProtectionMS
	match.logf("Boss %d hit at %d: HP %d", b.id, now, b.hp)
}

// bossDropCells is 0x005B8574: free cells over the whole map, a random
// subset kept in scan order.
func (match *onlineMatch) bossDropCells(count int) []battleengine.Cell {
	grid := match.engine.Grid()
	taken := map[battleengine.Cell]bool{}
	for _, bomb := range match.engine.Bombs() {
		taken[bomb.Cell] = true
	}
	for _, flame := range match.engine.Flames() {
		taken[flame.Cell] = true
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
			tile, ok := match.engine.TileAt(c)
			if ok && tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied && !taken[c] {
				cells = append(cells, c)
			}
		}
	}
	count = min(count, bossMaxDrops)
	for len(cells) > count {
		i := match.boss.rng.Intn(len(cells))
		cells = append(cells[:i], cells[i+1:]...)
	}
	return cells
}

// bossHitDrops is rule slot 7 (0x005F4A7A): one each of the first
// n - rand()%n NormalItems kinds.
func (match *onlineMatch) bossHitDrops(b *bossActor, now uint32) {
	n := len(b.normal)
	if n == 0 {
		return
	}
	kinds := n - match.boss.rng.Intn(n)
	cells := match.bossDropCells(kinds)
	items := make([]game.GameItem, 0, len(cells))
	for i, cell := range cells {
		items = append(items, game.GameItem{ItemID: b.normal[i].ItemID, Row: byte(cell.Row), Col: byte(cell.Col)})
	}
	if len(items) == 0 {
		return
	}
	drop := game.NPCDropItemEvent{ObjectID: b.id, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y), Items: items}
	body, err := drop.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queueNPC(&match.boss.outbox, b.id, game.NotifyNPCDropItem, now, body)
	match.mirror(game.NotifyNPCDropItem, body)
	match.applyBossDrops(b, items, true)
}

// applyBossDrops installs dropped items; partial drops also use up one of
// each kind from NormalItems (0x005FD837 -> 0x005A8C1B).
func (match *onlineMatch) applyBossDrops(b *bossActor, items []game.GameItem, partial bool) {
	if len(items) == 0 {
		return
	}
	events, err := match.engine.InstallRuleNPCDrops(b.id, pickupsFromItems(items))
	if err != nil {
		match.logf("Boss %d drops: %v", b.id, err)
		return
	}
	match.forgetPickups(events)
	if !partial {
		return
	}
	for _, item := range items {
		for i := range b.normal {
			if b.normal[i].ItemID == item.ItemID {
				b.normal[i].ItemCount--
				if b.normal[i].ItemCount == 0 {
					b.normal = append(b.normal[:i], b.normal[i+1:]...)
				}
				break
			}
		}
	}
}

// bossDeath is the HP-below-1 branch: 0x0FA7 with what the Boss picked up
// (scenes 1..3, at most 10 each), the remaining NormalItems and every
// OutfitItem, at random free cells.
func (match *onlineMatch) bossDeath(b *bossActor, now uint32) {
	var scenes []uint32
	for i, count := range b.basic {
		for j := 0; j < min(int(count), 10); j++ {
			scenes = append(scenes, uint32(i+1))
		}
	}
	for _, list := range [][]game.BossItemInfo{b.normal, b.info.OutfitItems} {
		for _, item := range list {
			for j := 0; j < int(item.ItemCount); j++ {
				scenes = append(scenes, item.ItemID)
			}
		}
	}
	cells := match.bossDropCells(len(scenes))
	items := make([]game.GameItem, 0, len(cells))
	for i, cell := range cells {
		items = append(items, game.GameItem{ItemID: scenes[i], Row: byte(cell.Row), Col: byte(cell.Col)})
	}
	death := game.PlayerDeathEvent{PlayerID: b.id, ClientTime: now, PosX: uint16(b.pos.X), PosY: uint16(b.pos.Y), Items: items}
	body, err := death.MarshalNetworkBinary()
	if err != nil {
		match.logf("Boss %d death: %v", b.id, err)
		return
	}
	match.queueNPC(&match.boss.outbox, b.id, game.NotifyPlayerDieEvent, now, body)
	match.mirror(game.NotifyPlayerDieEvent, body)
	match.applyBossDeath(b, now, items)
}

// applyBossDeath is 0x00606606 for a Boss: the items fly out and the Boss is
// removed at once (no die animation is played).
func (match *onlineMatch) applyBossDeath(b *bossActor, now uint32, items []game.GameItem) {
	if b.dead {
		return
	}
	b.dead, b.deadAt, b.hp, b.moving, b.path = true, now, 0, false, nil
	match.boss.bubbleLock++
	if rule := match.boss; rule.roundMS > now && rule.roundMS-max(now, rule.lastDeathAt) > 10_000 {
		rule.endsAt = now + 10_000
		rule.lastDeathAt = now
	}
	match.applyBossDrops(b, items, false)
	match.logf("Boss %d died at %d", b.id, now)
}

// bossContact is 0x0060C148 for the Boss: a trapped player within 41 px of
// it is killed (different team). Untrapped players are not touched.
func (match *onlineMatch) bossContact(b *bossActor, now uint32) {
	for _, actor := range match.engine.Actors() {
		if actor.State != battleengine.ActorTrapped || abs32(actor.Position.X-b.pos.X) >= bossContactPixels || abs32(actor.Position.Y-b.pos.Y) >= bossContactPixels {
			continue
		}
		events, err := match.runtime.AcceptNativeElimination(actor.PlayerID, b.id, nil)
		if err != nil {
			match.logf("Boss %d kill of %d: %v", b.id, actor.PlayerID, err)
			continue
		}
		var drops []game.GameItem
		for _, event := range events {
			if event.Kind == battleengine.EventPickupDropped {
				drops = append(drops, game.GameItem{ItemID: event.SceneID, Row: byte(event.Cell.Row), Col: byte(event.Cell.Col)})
			}
		}
		if drops == nil {
			drops = []game.GameItem{}
		}
		killed := game.PlayerKilledEvent{PlayerInteractionEvent: game.PlayerInteractionEvent{PlayerID: b.id, ClientTime: now, DestinationPlayerID: actor.PlayerID,
			PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y)}, Items: drops}
		body, err := killed.MarshalNetworkBinary()
		if err == nil {
			match.queueNPC(&match.boss.outbox, b.id, game.NotifyPlayerKilled, now, body)
			match.mirror(game.NotifyPlayerKilled, body)
		}
		match.forgetPickups(events)
		match.boss.events = append(match.boss.events, events...)
		match.logf("Boss %d kills trapped player %d", b.id, actor.PlayerID)
	}
}

// bossRemote applies a Boss's messages relayed by the arbitrator (UDP
// packages authored by the Boss, or the server's TCP notifications).
func (match *onlineMatch) bossRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	rule := match.boss
	if rule == nil {
		return nil, false
	}
	if event.Schema == game.CreateNPCBossEvent {
		if data, err := game.ParseCreateNPCBossNetwork(event.Body); err == nil {
			match.createBosses(data, match.engine.ElapsedMS())
		} else {
			match.logf("CREATE_NPC_BOSS: %v", err)
		}
		return nil, true
	}
	if !isBossID(sender) || len(event.Body) < 2 {
		return nil, false
	}
	b := rule.actors[sender]
	if b == nil || match.isArbitrator() {
		return nil, b != nil
	}
	key := fmt.Sprintf("%04x:%x", event.Schema, event.Body)
	if event.Schema != game.PlayerMoveSchema {
		if rule.seen[key] {
			return nil, true
		}
		rule.seen[key] = true
	}
	switch event.Schema {
	case game.PlayerMoveSchema:
		movement, err := game.ParsePlayerMove(event.Body)
		if err != nil || b.dead {
			return nil, true
		}
		for _, entry := range movement.Entries {
			m := entry.Move
			if entry.PlayerID != b.id {
				continue
			}
			match.receiveNPCMove(&b.boxNPCActor, m)
		}
	case game.PlayerUseBomb, game.NotifyPlayerUseBomb:
		q, err := game.ParsePlayerUseBombEvent(game.GameEvent{Schema: game.PlayerUseBomb, Body: event.Body})
		if err != nil || q.PlayerID != b.id {
			return nil, true
		}
		cell := battleengine.Cell{Row: int16(q.Row), Col: int16(q.Column)}
		if _, exists := match.wireBombID(b.id, q.ClientTime, cell); exists {
			return nil, true
		}
		placed, err := match.engine.PlaceRuleNPCBomb(b.id, cell, q.Power, q.Property, q.ClientTime)
		if err != nil {
			match.logf("Boss bubble: %v", err)
			return nil, true
		}
		match.bombs[placed.BombID] = wireBomb{owner: b.id, placedAt: q.ClientTime, cell: cell}
		return []battleengine.Event{placed}, true
	case game.PlayerBeHarmed:
		harm, err := game.ParseEntityHarmedEvent(event)
		if err == nil && harm.ObjectID == b.id && !b.dead && b.hp > 0 {
			if harm.IsAvatar {
				match.bossUntransform(b, harm.Time, false)
			} else {
				match.bossHarmed(b, harm.Time)
			}
		}
	case game.NotifyNPCDropItem:
		drop, err := game.ParseNPCDropItemEvent(event)
		if err == nil && drop.ObjectID == b.id && !b.dead {
			match.applyBossDrops(b, drop.Items, true)
		}
	case game.NotifyPlayerDieEvent:
		death, err := game.ParsePlayerDeathEvent(event)
		if err == nil && death.PlayerID == b.id {
			match.applyBossDeath(b, death.ClientTime, death.Items)
		}
	case game.NotifyPlayerKilled, game.NotifyPlayerMoveBomb:
		// The Boss killed a trapped player or kicked a bubble: outcomes the
		// arbitrator decided for it.
		return match.applyRemote(match.arbitrator, game.BattleMessageData{DataID: uint32(event.Schema), Data: event.Body}), true
	case game.NotifyPlayerGetItem:
		q, err := game.ParsePlayerItemEvent(event)
		if err == nil && q.PlayerID == b.id && !b.dead {
			if q.ItemID == bossTransformItem && q.ClientTime >= bossGrantTimeMark {
				match.bossTransform(b, match.engine.ElapsedMS())
				return nil, true
			}
			match.engine.CollectRuleNPCPickup(b.id, q.ItemID, battleengine.Position{X: int32(q.PosX), Y: int32(q.PosY)}.Cell())
			b.pickup(q.ItemID, q.ClientTime)
		}
	case game.NotifyNPCUseSkill:
		match.bossSkillRemote(b, event)
	case game.NotifyNPCTalk:
		// 0x005FDA38 passes the text to an empty debug function.
	}
	return nil, true
}

func (match *onlineMatch) bossView() *bossView {
	if match.boss == nil {
		return nil
	}
	view := &bossView{}
	for _, b := range match.boss.ordered() {
		if b.dead {
			continue
		}
		view.Actors = append(view.Actors, bossActorView{ID: b.id, Role: b.role, X: b.pos.X, Y: b.pos.Y, Facing: byte(battleDirection(b.facing)),
			Moving: b.moving, HP: b.hp, MaxHP: int(b.info.HP), Born: b.bornAt, BirthAge: b.birthFrameAt - b.bornAt, BirthMode: b.birth.mode,
			ProtectedUntil: b.protectedUntil, AvatarRole: b.avatarRole()})
	}
	now := match.engine.ElapsedMS()
	for id, f := range match.boss.bombFlights {
		if f.Until <= now {
			delete(match.boss.bombFlights, id)
		}
	}
	if len(match.boss.bombFlights) > 0 {
		view.BombFlights = match.boss.bombFlights
	}
	view.BubbleLock, view.EndsAt = match.boss.bubbleLock > 0, match.boss.endsAt
	for _, f := range match.boss.flights {
		view.Flights = append(view.Flights, bossFlightView{Scene: f.scene, FromRow: f.from.Row, FromCol: f.from.Col, Row: f.to.Row, Col: f.to.Col, Start: f.start, Lands: f.lands})
	}
	return view
}

func (match *onlineMatch) takeBossEvents() []battleengine.Event {
	if match.boss == nil {
		return nil
	}
	events := match.boss.events
	match.boss.events = nil
	return events
}
