package main

// Native rule 8, Client.exe 007A4334 / 00609DF9. Space pushes a nearby
// destructible box (00616813); it does not place an ordinary bubble. Box
// identities, movement and collision notices remain in the native protocol.

import (
	"encoding/binary"
	"math/rand"
	"sort"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const boxBombItem uint8 = 49

type boxObject struct {
	ID                     uint32
	Element                uint32
	Cell                   battleengine.Cell
	Position               battleengine.Position
	Destination            battleengine.Cell
	Direction              byte
	Owner                  uint16
	Power                  byte
	Moving                 bool
	DestroyAt, FrozenUntil uint32
	BornAt                 uint32
	Hidden                 bool
	Flags                  uint32
	Life                   int
	Remaining              int
	started                uint32
	origin                 battleengine.Position
	previous               battleengine.Position
	items                  []uint32
}

func (b *boxObject) wire() game.MapElementWire {
	return game.NewMapElementWire(uint16(b.Element), b.ID, byte(b.Cell.Row), byte(b.Cell.Col))
}
func (b *boxObject) breakable(now uint32) bool {
	return b.Life >= 0 && b.DestroyAt == 0 && b.FrozenUntil <= now
}

type boxEffect struct {
	Cell         battleengine.Cell
	Style        uint32
	Start, Until uint32
}
type boxRule struct {
	events          []battleengine.Event
	entry           mapdata.CompetitiveMap
	objects         map[uint32]*boxObject
	elements        map[uint32]mapdata.NativeBoxElement
	generations     map[battleengine.Cell]uint32
	rng             *rand.Rand
	npc             *boxNPCState
	effects         []boxEffect
	seen            map[[3]uint32]bool
	finishing       bool
	lastRequest     uint32
	lastFire        uint32
	fired           [4]uint32
	blockers        [4]uint32
	initialOrdinary int
	initialSpecial  [3]int
	lastGenerate    uint32
}

func newBoxRule(entry mapdata.CompetitiveMap, begin game.GameBeginData, config *battleengine.Config, elements map[uint32]mapdata.NativeBoxElement) *boxRule {
	rule := &boxRule{entry: entry, objects: map[uint32]*boxObject{}, elements: elements, generations: map[battleengine.Cell]uint32{}, rng: rand.New(rand.NewSource(int64(begin.ItemSeed))), npc: &boxNPCState{}, seen: map[[3]uint32]bool{}}
	for r := int16(0); r < int16(config.Grid.Height); r++ {
		for c := int16(0); c < int16(config.Grid.Width); c++ {
			at := battleengine.Cell{Row: r, Col: c}
			tile, _ := config.Grid.Cell(at)
			definition, ok := elements[tile.MapElementID]
			if !ok || !tile.MapElementOccupied || definition.LifeTime < 0 || definition.Width != 1 || definition.Height != 1 {
				continue
			}
			id := uint32(r)*16 + uint32(c)
			rule.objects[id] = &boxObject{ID: id, Element: tile.MapElementID, Cell: at, Position: battleengine.PositionAtCellCenter(at), Flags: definition.Flags, Life: definition.LifeTime}
		}
	}
	kept := config.Pickups[:0]
	for _, pickup := range config.Pickups {
		if b := rule.at(pickup.Cell); b != nil && pickup.State == battleengine.PickupHidden {
			b.items = append(b.items, pickup.SceneID)
		} else {
			kept = append(kept, pickup)
		}
	}
	config.Pickups = kept
	for _, b := range rule.objects {
		if b.Cell.Row > 0 && b.Cell.Row < 12 && b.Cell.Col > 0 && b.Cell.Col < 14 {
			if b.Flags&28 == 0 {
				rule.initialOrdinary++
			}
			for i, id := range []uint32{14100, 14105, 14115} {
				if b.Element == id {
					rule.initialSpecial[i]++
				}
			}
		}
	}
	return rule
}
func (rule *boxRule) ordered() []*boxObject {
	ids := make([]uint32, 0, len(rule.objects))
	for id := range rule.objects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*boxObject, 0, len(ids))
	for _, id := range ids {
		out = append(out, rule.objects[id])
	}
	return out
}
func (rule *boxRule) at(at battleengine.Cell) *boxObject {
	for _, b := range rule.objects {
		if b.Cell == at {
			return b
		}
	}
	return nil
}
func (rule *boxRule) lookup(w game.MapElementWire) *boxObject {
	b := rule.objects[w.UniqueID()]
	if b != nil && b.Element == uint32(w.ElementID()) {
		return b
	}
	return nil
}
func (rule *boxRule) tile(element uint32) (battleengine.Tile, bool) {
	d, ok := rule.elements[element]
	if !ok || d.Width != 1 || d.Height != 1 {
		return battleengine.Tile{}, false
	}
	attr := uint32(0)
	if len(d.GridAttrs) > 0 {
		attr = d.GridAttrs[0]
	}
	kind := battleengine.CellOpen
	if attr&15 == 0 {
		kind = battleengine.CellSolid
		if d.LifeTime > 0 {
			kind = battleengine.CellBreakable
		}
	}
	return battleengine.Tile{Kind: kind, FlamePassable: attr&0xf00 != 0, NativeGridAttr: attr, NativeGridAttrSet: true, NativePlayerExitAttr: attr, NativePlayerExitAttrSet: true, MapElementOccupied: true, MapElementID: element, Durability: byte(max(0, d.LifeTime)), ElementWidth: 1, ElementHeight: 1}, true
}
func boxStep(cell battleengine.Cell, dir byte) battleengine.Cell {
	switch dir {
	case 0:
		cell.Col++
	case 1:
		cell.Row--
	case 2:
		cell.Col--
	case 3:
		cell.Row++
	}
	return cell
}
func boxDestination(from battleengine.Cell, dir, power byte) battleengine.Cell {
	out := from
	switch dir {
	case 0:
		out.Col = min(13, from.Col+int16(power))
	case 1:
		out.Row = max(1, from.Row-int16(power))
	case 2:
		out.Col = 1
	case 3:
		out.Row = min(11, from.Row+int16(power))
	}
	return out
}
func (match *onlineMatch) boxInput(action *battleengine.Action) {
	if match.box == nil || !action.PlaceBomb {
		return
	}
	action.PlaceBomb = false
	now := match.engine.ElapsedMS()
	if match.box.finishing || match.box.lastRequest != 0 && now == match.box.lastRequest {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorActive {
		return
	}
	dir := nativeWalkDirection(actor.Facing)
	pos := actor.Position
	switch dir {
	case 0:
		pos.X += 22
	case 1:
		pos.Y -= 22
	case 2:
		pos.X -= 22
	case 3:
		pos.Y += 22
	}
	if pos.Cell() == actor.Position.Cell() {
		return
	}
	b := match.box.at(pos.Cell())
	if b == nil || b.Moving || !b.breakable(now) {
		return
	}
	behind := boxStep(b.Cell, dir)
	blocked := behind.Row <= 0 || behind.Row >= 12 || behind.Col <= 0 || behind.Col >= 14
	tile, inside := match.engine.Grid().Cell(behind)
	if inside && tile.MapElementOccupied && !tile.PlayerPassable(actor.Facing) {
		return
	}
	blocked = blocked || !inside || tile.MapElementOccupied
	for _, bomb := range match.engine.Bombs() {
		blocked = blocked || bomb.Cell == behind
	}
	for _, other := range match.engine.Actors() {
		blocked = blocked || other.State == battleengine.ActorTrapped && other.Position.Cell() == behind
	}
	match.box.lastRequest = now
	if blocked {
		request := game.DestroyBoxRequest{PlayerID: actor.PlayerID, Time: now, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y), Element: b.wire()}
		body, _ := request.MarshalNetworkBinary()
		if match.isArbitrator() {
			match.destroyBoxes(actor.PlayerID, []uint32{b.ID}, now)
		} else {
			match.request(game.RequestDestroyBox, now, body)
		}
		match.mirror(game.RequestDestroyBox, body)
	} else {
		request := game.PushMapElementRequest{PlayerID: actor.PlayerID, Direction: dir, Power: actor.BombPower + 4, Time: now, Element: b.wire()}
		body, _ := request.MarshalNetworkBinary()
		if match.isArbitrator() {
			match.pushBox(actor.PlayerID, b.ID, dir, request.Power)
		} else {
			match.request(game.RequestPushMapElement, now, body)
		}
		match.mirror(game.RequestPushMapElement, body)
	}
}
func (match *onlineMatch) pushBox(playerID uint16, id uint32, dir, power byte) bool {
	if match.box == nil || !match.isArbitrator() || dir > 3 {
		return false
	}
	b := match.box.objects[id]
	now := match.engine.ElapsedMS()
	if b == nil || b.Moving || !b.breakable(now) {
		return false
	}
	dest := boxDestination(b.Cell, dir, power)
	notice := game.PushMapElementNotify{PlayerID: playerID, Direction: dir, Power: power, DestinationRow: byte(dest.Row), DestinationCol: byte(dest.Col), CurrentX: uint32(b.Position.X), CurrentY: uint32(b.Position.Y), Time: now, Element: b.wire()}
	body, err := notice.MarshalNetworkBinary()
	if err != nil {
		return false
	}
	match.applyBoxPush(notice)
	match.queue(game.NotifyPushMapElement, now, now, body)
	match.mirror(game.NotifyPushMapElement, body)
	return true
}
func (match *onlineMatch) applyBoxPush(notice game.PushMapElementNotify) {
	b := match.box.lookup(notice.Element)
	if b == nil || b.DestroyAt != 0 || notice.Time < b.started {
		return
	}
	if notice.Power < 100 {
		b.Owner = notice.PlayerID
		b.Power = notice.Power
		b.Remaining = int(notice.Power)
	} else {
		b.Position = battleengine.Position{X: int32(notice.CurrentX), Y: int32(notice.CurrentY)}
		b.Remaining = int(notice.PlayerID)
	}
	b.origin, b.previous = b.Position, b.Position
	b.started = notice.Time
	b.Direction = notice.Direction
	b.Destination = battleengine.Cell{Row: int16(notice.DestinationRow), Col: int16(notice.DestinationCol)}
	b.Moving = true
	match.logf("box %d element %d pushed by %d from %v to %v", b.ID, b.Element, b.Owner, b.Cell, b.Destination)
}
func (match *onlineMatch) destroyBoxes(playerID uint16, ids []uint32, at uint32) {
	if match.box == nil {
		return
	}
	notice := game.MapElementsExplodedEvent{PlayerID: playerID, Time: at}
	for _, id := range ids {
		b := match.box.objects[id]
		if b != nil && b.DestroyAt == 0 {
			notice.Elements = append(notice.Elements, b.wire())
		}
	}
	if len(notice.Elements) == 0 {
		return
	}
	if len(notice.Elements) > 32 {
		notice.Elements = notice.Elements[:32]
	}
	match.applyBoxDestroy(notice)
	body, _ := notice.MarshalNetworkBinary()
	match.queue(game.NotifyMapElementExploded, at, at, body)
	match.mirror(game.NotifyMapElementExploded, body)
}
func (match *onlineMatch) applyBoxDestroy(notice game.MapElementsExplodedEvent) {
	for _, w := range notice.Elements {
		b := match.box.lookup(w)
		if b == nil || b.DestroyAt != 0 || notice.Time < b.BornAt {
			continue
		}
		match.advanceBox(b, notice.Time)
		b.DestroyAt = notice.Time + 500
		b.Moving = false
		match.engine.DestroyNativeBox(b.Cell, b.Element, notice.Time)
		if b.Flags&12 != 0 {
			style := uint32(10001)
			if b.Flags&8 != 0 {
				style = 10000
			}
			match.box.effects = append(match.box.effects, boxEffect{Cell: b.Position.Cell(), Style: style, Start: b.DestroyAt, Until: b.DestroyAt + 500})
			match.boxSpecialHit(b.Position.Cell(), style, notice.Time, true)
			match.boxSpecialNPCHit(b.Position.Cell(), style, notice.Time)
		}
	}
}
func (match *onlineMatch) stopBox(b *boxObject, at uint32) {
	current := b.Position.Cell()
	notice := game.MapElementStopEvent{Time: at, Boxes: []game.BoxWire{{Row: byte(b.Cell.Row), Col: byte(b.Cell.Col), StopRow: byte(current.Row), StopCol: byte(current.Col), MapElementID: b.Element, UniqueID: b.ID}}}
	match.applyBoxStop(notice)
	body, _ := notice.MarshalNetworkBinary()
	match.queue(game.NotifyMapElementStop, at, at, body)
	match.mirror(game.NotifyMapElementStop, body)
}
func (match *onlineMatch) applyBoxStop(notice game.MapElementStopEvent) {
	for _, wire := range notice.Boxes {
		b := match.box.objects[wire.UniqueID]
		if b == nil || b.DestroyAt != 0 || notice.Time < b.started {
			continue
		}
		dest := battleengine.Cell{Row: int16(wire.StopRow), Col: int16(wire.StopCol)}
		if err := match.engine.MoveNativeBox(b.Cell, dest, b.Element); err != nil {
			continue
		}
		b.Cell = dest
		b.Position = battleengine.PositionAtCellCenter(dest)
		b.Moving = false
		b.Owner = 0
		b.Remaining = 0
	}
}
func (match *onlineMatch) addBox(w game.MapElementWire, at uint32, hidden bool) {
	rule := match.box
	// 005E50F9 refuses a repeated generated UID before installing it.
	if !hidden && rule.objects[w.UniqueID()] != nil {
		return
	}
	cell := battleengine.Cell{Row: int16(w[6]), Col: int16(w[7])}
	tile, ok := rule.tile(uint32(w.ElementID()))
	if !ok {
		return
	}
	if !hidden {
		tile.Durability = 0
	}
	if err := match.engine.SetNativeBoxCell(cell, tile); err != nil {
		return
	}
	rule.objects[w.UniqueID()] = &boxObject{ID: w.UniqueID(), Element: uint32(w.ElementID()), Cell: cell, Position: battleengine.PositionAtCellCenter(cell), BornAt: at, Hidden: hidden, Flags: rule.elements[uint32(w.ElementID())].Flags, Life: rule.elements[uint32(w.ElementID())].LifeTime}
	if !hidden {
		rule.objects[w.UniqueID()].FrozenUntil = at + 1500
	}
	rule.generations[cell] = max(rule.generations[cell], w.UniqueID()>>8)
}
func (match *onlineMatch) boxRemote(sender uint16, event game.GameEvent) ([]battleengine.Event, bool) {
	if match.box == nil {
		return nil, false
	}
	if events, handled := match.boxNPCRemote(sender, event); handled {
		return events, true
	}
	switch event.Schema {
	case game.RequestPushMapElement:
		q, err := game.ParsePushMapElementRequest(event)
		if err == nil && q.PlayerID == sender && match.isArbitrator() {
			if b := match.box.lookup(q.Element); b != nil {
				match.pushBox(q.PlayerID, b.ID, q.Direction, q.Power)
			}
		}
		return nil, true
	case game.RequestDestroyBox:
		q, err := game.ParseDestroyBoxRequest(event)
		if err == nil && q.PlayerID == sender && match.isArbitrator() {
			if b := match.box.lookup(q.Element); b != nil {
				match.destroyBoxes(q.PlayerID, []uint32{b.ID}, q.Time)
			}
		}
		return nil, true
	case game.NotifyPushMapElement:
		if sender == match.arbitrator {
			if q, err := game.ParsePushMapElementNotify(event); err == nil {
				match.applyBoxPush(q)
			}
		}
		return nil, true
	case game.NotifyMapElementStop:
		if q, err := game.ParseMapElementStopEvent(event); err == nil {
			match.applyBoxStop(q)
		}
		return nil, true
	case game.NotifyMapElementExploded:
		if q, err := game.ParseMapElementsExplodedEvent(event); err == nil {
			match.applyBoxDestroy(q)
		}
		return nil, true
	case game.NotifyGenerateBox:
		if sender == match.arbitrator {
			if q, err := game.ParseGenerateBoxesEvent(event); err == nil {
				for _, w := range q.Elements {
					match.addBox(w, match.engine.ElapsedMS(), false)
				}
			}
		}
		return nil, true
	case game.NotifyMapElementKnock:
		if sender == match.arbitrator {
			if q, err := game.ParseMapElementKnockEvent(event); err == nil {
				notice := game.MapElementsExplodedEvent{PlayerID: sender, Time: q.Time}
				notice.Elements = append(notice.Elements, q.First...)
				notice.Elements = append(notice.Elements, q.Second...)
				match.applyBoxDestroy(notice)
			}
		}
		return nil, true
	case game.NotifyPlayerKnocked:
		q, err := game.ParsePlayerKnockedEvent(event)
		if err != nil || (sender != q.PlayerID && sender != match.arbitrator) {
			return nil, true
		}
		return match.receiveBoxHarm(game.EntityHarmedEvent{ObjectID: q.PlayerID, Time: q.Time, PosX: q.PosX, PosY: q.PosY, IsAvatar: q.IsAvatar}, event.Schema), true
	case game.PlayerBeHarmed:
		q, err := game.ParseEntityHarmedEvent(event)
		if err != nil || (sender != q.ObjectID && sender != match.arbitrator) {
			return nil, true
		}
		return match.receiveBoxHarm(q, event.Schema), true
	case game.NotifyPlayerTrapped:
		if len(event.Body) == 6 {
			id := binary.BigEndian.Uint16(event.Body)
			if sender == id || sender == match.arbitrator {
				e, ok := match.engine.ApplyNativeBoxIce(id, binary.BigEndian.Uint32(event.Body[2:]) != 0)
				if ok {
					return []battleengine.Event{e}, true
				}
			}
		}
		return nil, true
	case game.NotifyFire:
		if sender == match.arbitrator {
			if q, err := game.ParsePlayerTimedStateEvent(event); err == nil {
				return match.applyBoxFire(q), true
			}
		}
		return nil, true
	}
	return nil, false
}

func (match *onlineMatch) boxFinalDeath(death game.PlayerDeathEvent) {
	if match.box != nil {
		match.finishBoxIfDecided()
	}
}
func (match *onlineMatch) finishBoxIfDecided() {
	teams := map[byte]bool{}
	for _, a := range match.engine.Actors() {
		if a.State != battleengine.ActorEliminated && a.DurabilityLeft > 0 {
			teams[a.TeamID] = true
		}
	}
	if len(teams) <= 1 {
		match.box.finishing = true
	}
}
func (match *onlineMatch) boxFinishing() bool { return match.box != nil && match.box.finishing }

func (match *onlineMatch) applyBoxItem(use game.WorldUseItemEvent) {
	if use.ItemID != uint32(boxBombItem) {
		return
	}
	if !match.engine.ConsumeHeldAction(use.PlayerID, boxBombItem) {
		return
	}
	at := battleengine.Cell{Row: int16(use.PosY / 40), Col: int16(use.PosX / 40)}
	e, err := match.runtime.AcceptHumanBombPlacement(use.PlayerID, at, 5, 0, max(use.ClientTime, battleengine.NativeRoundStartClockMS))
	if err != nil {
		match.logf("box item49 rejected: %v", err)
		return
	}
	match.bombs[e.BombID] = wireBomb{owner: use.PlayerID, placedAt: use.ClientTime, cell: at}
	match.engine.ConsumeBoxBombPower(use.PlayerID)
}

func (match *onlineMatch) boxDebugError(err error) {
	if err != nil {
		match.logf("box: %v", err)
	}
}

func (match *onlineMatch) takeBoxEvents() []battleengine.Event {
	if match.box == nil {
		return nil
	}
	events := match.box.events
	match.box.events = nil
	return events
}
