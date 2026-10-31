package main

import (
	"encoding/binary"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

// movingBoxContact is 005F542F: the player's centre lies in the moving
// box's half-open 40x40 rectangle. Box speed is 400 px/s (8 px per tick).
func (rule *boxRule) movingBoxContact(pos battleengine.Position) *boxObject {
	for _, b := range rule.ordered() {
		if b.Moving && b.DestroyAt == 0 && pos.X >= b.Position.X-20 && pos.X < b.Position.X+20 && pos.Y >= b.Position.Y-20 && pos.Y < b.Position.Y+20 {
			return b
		}
	}
	return nil
}
func (match *onlineMatch) advanceBox(b *boxObject, now uint32) bool {
	if !b.Moving || b.DestroyAt != 0 || now < b.started {
		return false
	}
	b.previous = b.Position
	distance := int32(now-b.started) * 400 / 1000
	dest := battleengine.PositionAtCellCenter(b.Destination)
	b.Position = b.origin
	switch b.Direction {
	case 0:
		b.Position.X = min(dest.X, b.origin.X+distance)
	case 1:
		b.Position.Y = max(dest.Y, b.origin.Y-distance)
	case 2:
		b.Position.X = max(dest.X, b.origin.X-distance)
	case 3:
		b.Position.Y = min(dest.Y, b.origin.Y+distance)
	}
	if b.Remaining > 0 {
		old, current := b.previous.Cell(), b.Position.Cell()
		b.Remaining -= int(abs32(int32(old.Row-current.Row)) + abs32(int32(old.Col-current.Col)))
	}
	return b.Position == dest
}
func (match *onlineMatch) bounceBox(b *boxObject, now uint32) {
	if b.Remaining <= 0 {
		if match.isArbitrator() {
			match.stopBox(b, now)
		}
		return
	}
	dir := (b.Direction + 2) & 3
	from := b.Position.Cell()
	dest := from
	switch dir {
	case 0:
		dest.Col = min(13, from.Col+int16(b.Remaining))
	case 1:
		dest.Row = max(1, from.Row-int16(b.Remaining))
	case 2:
		dest.Col = max(1, from.Col-int16(b.Remaining))
	case 3:
		dest.Row = min(11, from.Row+int16(b.Remaining))
	}
	b.Direction = dir
	b.origin = b.Position
	b.started = now
	b.Destination = dest
	b.Moving = true
}
func boxBoundary(pos battleengine.Position) bool {
	return pos.X < 64 || pos.X > 536 || pos.Y < 66 || pos.Y > 416
}
func boxOverlap(a, b battleengine.Position) bool { return abs32(a.X-b.X) < 38 && abs32(a.Y-b.Y) < 38 }

// 005E4A3F / 005E4BA3 / 005E4CD9 also test a forward 50px strip.
func boxContact(a *boxObject, b battleengine.Position) bool {
	if boxOverlap(a.Position, b) {
		return true
	}
	cross, gap := a.Position.Y-b.Y, b.X-a.Position.X
	switch a.Direction {
	case 1:
		cross, gap = a.Position.X-b.X, a.Position.Y-b.Y
	case 2:
		gap = a.Position.X - b.X
	case 3:
		cross, gap = a.Position.X-b.X, b.Y-a.Position.Y
	}
	return cross > -6 && cross < 10 && gap > 0 && gap < 50
}

func (match *onlineMatch) boxCollisions(now uint32) {
	if !match.isArbitrator() {
		return
	}
	objects := match.box.ordered()
	for i, b := range objects {
		if !b.Moving || b.DestroyAt != 0 {
			continue
		}
		collided := false
		for _, other := range objects[i+1:] {
			if other.Moving && other.DestroyAt == 0 && boxContact(b, other.Position) {
				ids := []uint32{}
				if b.breakable(now) {
					ids = append(ids, b.ID)
				} else {
					match.stopBox(b, now)
				}
				if other.breakable(now) {
					ids = append(ids, other.ID)
				} else {
					match.stopBox(other, now)
				}
				match.destroyBoxes(match.arbitrator, ids, now)
				collided = true
				break
			}
		}
		if collided || !b.Moving || b.DestroyAt != 0 {
			continue
		}
		// The native broad phase tests nearby registered map cells and bubbles,
		// with the same 38-pixel overlap threshold for single-cell objects.
		cell := b.Position.Cell()
		blocked := false
		for dr := int16(-1); dr <= 1; dr++ {
			for dc := int16(-1); dc <= 1; dc++ {
				at := battleengine.Cell{Row: cell.Row + dr, Col: cell.Col + dc}
				if at == b.Cell {
					continue
				}
				if other := match.box.at(at); other != nil && other.DestroyAt != 0 {
					continue
				}
				tile, inside := match.engine.Grid().Cell(at)
				if inside && tile.MapElementOccupied && !tile.PlayerPassable(nativeDirection(b.Direction)) && boxContact(b, battleengine.PositionAtCellCenter(at)) {
					blocked = true
				}
			}
		}
		for _, bomb := range match.engine.Bombs() {
			if boxContact(b, battleengine.PositionAtCellCenter(bomb.Cell)) {
				blocked = true
			}
		}
		if blocked {
			if b.Flags&12 != 0 {
				match.destroyBoxes(match.arbitrator, []uint32{b.ID}, now)
			} else if b.Flags&16 != 0 {
				match.bounceBox(b, now)
			} else {
				match.stopBox(b, now)
			}
		}
	}
}
func nativeDirection(dir byte) battleengine.Direction {
	switch dir {
	case 0:
		return battleengine.DirectionRight
	case 1:
		return battleengine.DirectionUp
	case 2:
		return battleengine.DirectionLeft
	default:
		return battleengine.DirectionDown
	}
}

func (match *onlineMatch) boxTick() []battleengine.Event {
	rule := match.box
	if rule == nil || rule.finishing {
		return match.takeBoxEvents()
	}
	now := match.engine.ElapsedMS()
	var events []battleengine.Event
	for _, b := range rule.ordered() {
		if b.DestroyAt != 0 {
			if b.DestroyAt <= now {
				match.engine.ClearNativeBox(b.Cell, b.Element)
				for _, scene := range b.items {
					if e, err := match.engine.RevealNativeBoxPickup(scene, b.Position.Cell()); err == nil {
						events = append(events, e)
					}
				}
				delete(rule.objects, b.ID)
			}
			continue
		}
		if b.FrozenUntil != 0 && b.FrozenUntil <= now {
			b.FrozenUntil = 0
			if tile, ok := rule.tile(b.Element); ok {
				match.engine.SetNativeBoxCell(b.Cell, tile)
			}
		}
		arrived := match.advanceBox(b, now)
		if b.Moving && b.Position.Cell() != b.previous.Cell() && match.isArbitrator() && b.Owner != 0 {
			match.boxCollect(b, now)
		}
		if arrived {
			if b.Remaining > 0 && boxBoundary(b.Position) {
				match.bounceBox(b, now)
			} else if match.isArbitrator() {
				match.stopBox(b, now)
			}
		}
	}
	match.boxCollisions(now)
	if actor, ok := actorByID(match.engine.Actors(), match.local); ok && actor.State == battleengine.ActorActive {
		if b := rule.movingBoxContact(actor.Position); b != nil {
			if b.breakable(now) {
				match.destroyBoxes(match.local, []uint32{b.ID}, now)
			}
			if b.Flags&4 == 0 {
				match.boxHitLocal(game.NotifyPlayerKnocked, 0)
			}
		}
	}
	effects := rule.effects[:0]
	for _, effect := range rule.effects {
		if now < effect.Until {
			effects = append(effects, effect)
			if now >= effect.Start {
				match.boxSpecialHit(effect.Cell, effect.Style, now, false)
			}
		}
	}
	rule.effects = effects
	events = append(events, match.boxLauncherTick(now)...)
	match.boxGenerate(now)
	match.boxNPCTick(now)
	match.forgetPickups(events)
	events = append(events, match.takeBoxEvents()...)
	return events
}
func (match *onlineMatch) boxCollect(b *boxObject, now uint32) {
	for _, pickup := range match.engine.Pickups() {
		if pickup.State != battleengine.PickupAvailable || pickup.Cell != b.Position.Cell() || pickup.SceneID == 44 || pickup.SceneID == 201 {
			continue
		}
		pos := battleengine.PositionAtCellCenter(pickup.Cell)
		event, err := match.engine.ApplyExistingPickupAt(b.Owner, pickup.SceneID, pos)
		if err != nil {
			return
		}
		match.collected[pickupKey{scene: event.SceneID, cell: pickup.Cell}] = b.Owner
		match.publishItem(game.PlayerItemEvent{PlayerID: b.Owner, ClientTime: now, ItemID: pickup.SceneID, PosX: uint16(pos.X), PosY: uint16(pos.Y)})
		return
	}
}
func boxAreaContains(centre battleengine.Cell, pos battleengine.Position) bool {
	hits := 0
	for _, delta := range [][2]int32{{0, 0}, {-3, -3}, {3, -3}, {-3, 3}, {3, 3}} {
		cell := (battleengine.Position{X: pos.X + delta[0], Y: pos.Y + delta[1]}).Cell()
		if abs32(int32(cell.Row-centre.Row)) < 2 && abs32(int32(cell.Col-centre.Col)) < 2 {
			hits++
		}
	}
	return hits >= 4
}
func (match *onlineMatch) boxSpecialHit(cell battleengine.Cell, style uint32, now uint32, immediate bool) {
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorActive || actor.HarmProtectionExpiresAt > now || !boxAreaContains(cell, actor.Position) {
		return
	}
	if style == 10000 {
		body := make([]byte, 6)
		binary.BigEndian.PutUint16(body, match.local)
		if actor.TransformationSceneID != 0 {
			binary.BigEndian.PutUint32(body[2:], 1)
		}
		match.engine.ApplyNativeBoxIce(match.local, actor.TransformationSceneID != 0)
		match.queue(game.NotifyPlayerTrapped, now, now, body)
		match.mirror(game.NotifyPlayerTrapped, body)
	} else {
		loss := uint16(0)
		if immediate {
			loss = 500
		}
		match.boxHitLocal(game.PlayerBeHarmed, loss)
	}
}
func (match *onlineMatch) boxHitLocal(schema uint16, loss uint16) []battleengine.Event {
	actor, ok := actorByID(match.engine.Actors(), match.local)
	now := match.engine.ElapsedMS()
	if !ok || actor.State != battleengine.ActorActive || actor.HarmProtectionExpiresAt > now {
		return nil
	}
	var events []battleengine.Event
	avatar := actor.TransformationSceneID != 0
	if avatar {
		e, changed, err := match.engine.ApplyVerifiedActorHit(match.local, actor.Position, true)
		if err == nil && changed {
			events = append(events, e)
		}
	} else {
		events, _ = match.engine.ApplyNativeDurabilityHit(match.local, 0, actor.Position, now)
	}
	if len(events) == 0 {
		return nil
	}
	harm := game.EntityHarmedEvent{ObjectID: match.local, Time: now, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y), IsAvatar: avatar, LossHP: loss}
	match.publishBoxHarm(harm, schema)
	match.box.events = append(match.box.events, events...)
	return events
}
func (match *onlineMatch) boxHarm(event battleengine.Event, avatar bool) {
	id := event.TargetID
	if avatar {
		id = event.PlayerID
	}
	if id != match.local {
		return
	}
	match.publishBoxHarm(game.EntityHarmedEvent{ObjectID: id, Time: event.TimeMS, PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y), IsAvatar: avatar}, game.PlayerBeHarmed)
}
func (match *onlineMatch) publishBoxHarm(harm game.EntityHarmedEvent, schema uint16) {
	key := [3]uint32{uint32(schema), uint32(harm.ObjectID), harm.Time}
	if match.box.seen[key] {
		return
	}
	match.box.seen[key] = true
	var body []byte
	if schema == game.NotifyPlayerKnocked {
		body, _ = (game.PlayerKnockedEvent{PlayerID: harm.ObjectID, Time: harm.Time, PosX: harm.PosX, PosY: harm.PosY, IsAvatar: harm.IsAvatar}).MarshalNetworkBinary()
	} else {
		body = make([]byte, 13)
		binary.BigEndian.PutUint16(body, harm.ObjectID)
		binary.BigEndian.PutUint32(body[2:], harm.Time)
		binary.BigEndian.PutUint16(body[6:], harm.PosX)
		binary.BigEndian.PutUint16(body[8:], harm.PosY)
		if harm.IsAvatar {
			body[10] = 1
		}
		binary.BigEndian.PutUint16(body[11:], harm.LossHP)
	}
	match.queue(schema, harm.Time, harm.Time, body)
	match.mirror(schema, body)
	match.boxHarmReport(harm, schema)
}
func (match *onlineMatch) receiveBoxHarm(harm game.EntityHarmedEvent, schema uint16) []battleengine.Event {
	key := [3]uint32{uint32(schema), uint32(harm.ObjectID), harm.Time}
	if match.box.seen[key] {
		return nil
	}
	pos := battleengine.Position{X: int32(harm.PosX), Y: int32(harm.PosY)}
	var events []battleengine.Event
	if harm.IsAvatar {
		e, changed, err := match.engine.ApplyVerifiedActorHit(harm.ObjectID, pos, true)
		if err == nil && changed {
			events = append(events, e)
		}
	} else {
		events, _ = match.engine.ApplyNativeDurabilityHit(harm.ObjectID, 0, pos, harm.Time)
	}
	if len(events) == 0 {
		return nil
	}
	match.box.seen[key] = true
	match.boxHarmReport(harm, schema)
	return events
}
func (match *onlineMatch) boxHarmReport(harm game.EntityHarmedEvent, schema uint16) {
	actor, ok := actorByID(match.engine.Actors(), harm.ObjectID)
	if !ok || harm.IsAvatar {
		return
	}
	match.logf("box player %d durability %d at %d (0x%04X)", harm.ObjectID, actor.DurabilityLeft, harm.Time, schema)
	body, _ := (game.PlayerDeathEvent{PlayerID: harm.ObjectID, ClientTime: harm.Time, PosX: harm.PosX, PosY: harm.PosY, Items: []game.GameItem{}}).MarshalNetworkBinary()
	if schema == game.NotifyPlayerKnocked && harm.ObjectID == match.local || schema == game.PlayerBeHarmed && match.isArbitrator() {
		match.mirror(game.NotifyPlayerDieEvent, body)
	}
	if actor.DurabilityLeft == 0 {
		match.finishBoxIfDecided()
		if match.isArbitrator() {
			match.queue(game.NotifyPlayerDieEvent, harm.Time, harm.Time, body)
		}
	}
}

// Native 0xFA4 handles ordinary bubble damage to stationary map objects.
// Retain their seeded item until the box's own 500ms disappearance.
func (match *onlineMatch) boxObserve(events []battleengine.Event) {
	if match.box == nil {
		return
	}
	for _, event := range events {
		if event.Kind != battleengine.EventCellDestroyed {
			continue
		}
		b := match.box.at(event.Cell)
		if b == nil || b.DestroyAt != 0 {
			continue
		}
		notice := game.MapElementsExplodedEvent{Time: event.TimeMS, Elements: []game.MapElementWire{b.wire()}}
		match.applyBoxDestroy(notice)
	}
}
