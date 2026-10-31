package main

import (
	"encoding/binary"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

var boxCorners = [4]battleengine.Cell{{Row: 0, Col: 0}, {Row: 0, Col: 14}, {Row: 12, Col: 0}, {Row: 12, Col: 14}}
var boxCornerElements = [4]uint32{14116, 14118, 14119, 14117}

func (match *onlineMatch) installBoxLaunchers() {
	for i, at := range boxCorners {
		if tile, ok := match.box.tile(boxCornerElements[i]); ok {
			match.engine.SetNativeBoxCell(at, tile)
		}
	}
}
func (rule *boxRule) launcherMask(now uint32) byte {
	if now < rule.lastFire || now-rule.lastFire < 1800 {
		return 0
	}
	k := 0
	if now-rule.lastFire > 3600 {
		k = 2
	}
	switch r := rule.rng.Intn(100); r {
	case 20, 30, 40, 50, 60:
		k++
	case 99:
		k += 2
	}
	switch k {
	case 1:
		return 1 << rule.rng.Intn(4)
	case 2:
		if rule.rng.Intn(2) == 0 {
			return 5
		}
		return 10
	case 3:
		return 15 ^ (1 << rule.rng.Intn(4))
	case 4:
		return 15
	}
	return 0
}
func (match *onlineMatch) boxLauncherTick(now uint32) []battleengine.Event {
	if !match.isArbitrator() {
		return nil
	}
	mask := match.box.launcherMask(now)
	if mask == 0 {
		return nil
	}
	// Native frames carry an unquantized scene millisecond clock; its low bit
	// selects the hidden side blocker. Preserve that bit rather than making
	// every 20ms simulation tick choose the same direction forever.
	wireTime := now
	if !match.started.IsZero() {
		wireTime = match.clientTag()
	}
	notice := game.PlayerTimedStateEvent{PlayerID: match.arbitrator, Time: wireTime, Value: mask}
	events := match.applyBoxFire(notice)
	body := make([]byte, 7)
	binary.BigEndian.PutUint16(body, notice.PlayerID)
	binary.BigEndian.PutUint32(body[2:], notice.Time)
	body[6] = mask
	match.queue(game.NotifyFire, wireTime, wireTime, body)
	match.mirror(game.NotifyFire, body)
	return events
}
func (match *onlineMatch) applyBoxFire(notice game.PlayerTimedStateEvent) []battleengine.Event {
	rule := match.box
	key := [3]uint32{game.NotifyFire, uint32(notice.PlayerID), notice.Time}
	if rule.seen[key] {
		return nil
	}
	rule.seen[key] = true
	rule.lastFire = notice.Time
	var events []battleengine.Event
	even := [4]battleengine.Cell{{Row: 0, Col: 1}, {Row: 1, Col: 14}, {Row: 11, Col: 0}, {Row: 12, Col: 13}}
	odd := [4]battleengine.Cell{{Row: 1, Col: 0}, {Row: 0, Col: 13}, {Row: 12, Col: 1}, {Row: 11, Col: 14}}
	for i, corner := range boxCorners {
		if notice.Value&(1<<i) == 0 {
			continue
		}
		if old := rule.objects[rule.blockers[i]]; old != nil && old.Hidden {
			match.engine.ClearNativeBox(old.Cell, old.Element)
			delete(rule.objects, old.ID)
		}
		blocker := even[i]
		if notice.Time&1 != 0 {
			blocker = odd[i]
		}
		uid := uint32(blocker.Row)*16 + uint32(blocker.Col)
		match.addBox(game.NewMapElementWire(8005, uid, byte(blocker.Row), byte(blocker.Col)), notice.Time, true)
		rule.blockers[i] = uid
		rule.fired[i] = notice.Time
		e, err := match.engine.PlaceBoxLauncherBomb(notice.PlayerID, corner, notice.Time)
		if err != nil {
			match.logf("box launcher: %v", err)
			continue
		}
		match.bombs[e.BombID] = wireBomb{owner: notice.PlayerID, placedAt: notice.Time, cell: corner}
		events = append(events, e)
	}
	match.logf("box launchers mask 0x%X at %d", notice.Value, notice.Time)
	return events
}
func (match *onlineMatch) boxGenerate(now uint32) {
	rule := match.box
	if !match.isArbitrator() || now <= 30000 || now-rule.lastGenerate <= 5000 {
		return
	}
	ordinary := 0
	special := [3]int{}
	for _, b := range rule.objects {
		if !b.breakable(now) || b.Cell.Row < 1 || b.Cell.Row > 11 || b.Cell.Col < 1 || b.Cell.Col > 13 {
			continue
		}
		if b.Flags&28 == 0 {
			ordinary++
		}
		for i, id := range []uint32{14100, 14105, 14115} {
			if b.Element == id {
				special[i]++
			}
		}
	}
	wanted := [4]int{}
	if rule.initialOrdinary > 0 && ordinary*2 < rule.initialOrdinary {
		wanted[0] = rule.rng.Intn(3) + 2
	}
	for i := range special {
		if special[i] == 0 && rule.initialSpecial[i] > 0 {
			wanted[i+1] = rule.rng.Intn(min(2, rule.initialSpecial[i]))
		}
	}
	occupied := map[battleengine.Cell]bool{}
	for _, b := range match.engine.Bombs() {
		occupied[b.Cell] = true
	}
	for _, p := range match.engine.Pickups() {
		if p.State == battleengine.PickupAvailable {
			occupied[p.Cell] = true
		}
	}
	for _, a := range match.engine.Actors() {
		if a.State != battleengine.ActorEliminated {
			occupied[a.Position.Cell()] = true
		}
	}
	if npc := match.boxNPCView(); npc != nil {
		for _, a := range npc.Actors {
			if !a.Dead {
				occupied[(battleengine.Position{X: a.X, Y: a.Y}).Cell()] = true
			}
		}
	}
	for _, f := range match.engine.FieldObjects() {
		occupied[f.Cell] = true
	}
	var free []battleengine.Cell
	grid := match.engine.Grid()
	for r := int16(1); r <= 11; r++ {
		for c := int16(1); c <= 13; c++ {
			at := battleengine.Cell{Row: r, Col: c}
			tile, _ := grid.Cell(at)
			if tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied && !occupied[at] {
				free = append(free, at)
			}
		}
	}
	rule.rng.Shuffle(len(free), func(i, j int) { free[i], free[j] = free[j], free[i] })
	normalIDs := []uint16{14112, 14110, 14111, 14113, 14114, 14120, 14121, 14122, 14126}
	notice := game.GenerateBoxesEvent{}
	add := func(id uint16, normal bool) {
		if len(free) == 0 || len(notice.Elements) >= 20 {
			return
		}
		at := free[0]
		free = free[1:]
		uid := uint32(at.Row)*16 + uint32(at.Col)
		if normal {
			rule.generations[at]++
			uid |= rule.generations[at] << 8
		}
		notice.Elements = append(notice.Elements, game.NewMapElementWire(id, uid, byte(at.Row), byte(at.Col)))
	}
	for n := 0; n < wanted[0]; n++ {
		add(normalIDs[rule.rng.Intn(len(normalIDs))], true)
	}
	// 00617B9C's final loop replenishes the first two special categories; the
	// third (rebound) is counted but is not visited by that loop.
	for i, id := range []uint16{14100, 14105} {
		for n := 0; n < wanted[i+1]; n++ {
			add(id, false)
		}
	}
	if len(notice.Elements) == 0 {
		return
	}
	rule.lastGenerate = now
	for _, w := range notice.Elements {
		match.addBox(w, now, false)
	}
	body, _ := notice.MarshalNetworkBinary()
	match.queue(game.NotifyGenerateBox, now, now, body)
	match.mirror(game.NotifyGenerateBox, body)
	match.logf("box regenerated %d objects at %d", len(notice.Elements), now)
}
