package main

// Adventure NPC skills (docs/adventure-npc-ai-re.md §6): a skill is a prop
// of config/errorPPP.ini. In battle, the first stage-XML skill whose
// triggers hold and whose Tier cooldown has passed is cast with 0x0FB0; the
// NPC stops for PrepareTime, then the prop becomes a trap on its cell, a
// projectile, or fires at once. When it fires the arbitrator picks the
// affected roles by shape and object and sends 0x10F7; each effect then
// lands AffectCount times. HP losses are reported by each victim's own
// client (0x10F4 with the HP left), as 0x005B57DB does.
//
// Played effects (with RestorePlayerAttr): HP, SetTotalPercentHP,
// SetCurPercentHP, Defence/Defense, TouchHarmValue, Enmity; SpeedDel,
// BombPowerDel, BombCountDel, CanUseMoveKey, CanUseBombKey, CanUseItemKey,
// IsReversalControl, IsSlip and the projectiles' IsIgniteBomb; SpecialAvatarType
// changes only the look. Not played yet: the avatars' own movement/bubble
// slots.

import (
	"strconv"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

type advCastStage byte

const (
	castPrepare advCastStage = iota
	castTrap
	castMove
	castTrigger
)

type advCast struct {
	npc        uint16
	prop       mapdata.AdventureProp
	guid       uint32 // the 0x0FB0 time, which names the prop in 0x10F7
	stage      advCastStage
	since      uint32
	cell       battleengine.Cell // the prop's cell (caster's, trap's or projectile's)
	pos        battleengine.Position
	dir        int
	hasTarget  bool
	target     battleengine.Cell
	hits       []uint16
	targets    []uint16
	applied    int
	restore    map[uint16]map[string]float64
	triggerSet bool
}

// advCastView is what the host draws: the prop's position by stage, when
// the stage began, and in Trigger the area cells and targets for GridsAnim
// and AffectAnim.
type advCastView struct {
	ID      uint32                     `json:"id"`
	Stage   byte                       `json:"stage"`
	Since   uint32                     `json:"since"`
	Caster  uint16                     `json:"caster"`
	Dir     int                        `json:"dir"` // 0 right, 1 up, 2 left, 3 down
	Row     int16                      `json:"row"`
	Col     int16                      `json:"col"`
	X       int32                      `json:"x"`
	Y       int32                      `json:"y"`
	Anims   mapdata.AdventurePropAnims `json:"anims"`
	Offsets [2][2]int                  `json:"offsets"` // prepare, affect
	Cells   []cell                     `json:"cells,omitempty"`
	Targets []uint16                   `json:"targets,omitempty"`
}

func skillTriggerHolds(skill mapdata.AdventureNPCSkill, kind, lower, upper string, value int) bool {
	low, high := atoiField(skill[lower]), atoiField(skill[upper])
	switch atoiField(skill[kind]) {
	case 0:
		return true
	case 1:
		if upper == "" {
			return value > low
		}
		return value >= low && value < high
	case 2:
		if upper == "" {
			return value == low
		}
		return value >= low && value < high
	case 3:
		return value < low
	}
	return false
}

func atoiField(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// adventureTrySkill is 0x005BBAFA: only in battle, the first skill whose
// time/HP triggers hold and whose prop can be used (0x005F20CF).
func (match *onlineMatch) adventureTrySkill(n *advNPC, now uint32) bool {
	rule := match.adventure
	index := int(n.id) - adventureFirstNPCID
	if !n.battle || index < 0 || index >= len(rule.stage.NPCs) || now < n.preparingUntil {
		return false
	}
	for _, skill := range rule.stage.NPCs[index].Skills {
		// TimeTrigger 1 is the game time, 2 the time in battle.
		timeOK := false
		switch atoiField(skill["TimeTrigger"]) {
		case 0:
			timeOK = true
		case 1:
			timeOK = uint32(atoiField(skill["TimeLower"])) <= now && now < uint32(atoiField(skill["TimeUpper"]))
		case 2:
			in := now - n.battleSince
			timeOK = uint32(atoiField(skill["TimeLower"])) <= in && in < uint32(atoiField(skill["TimeUpper"]))
		}
		// HPTrigger compares the current HP with Lower only.
		hpOK := skillTriggerHolds(skill, "HPTrigger", "HPLower", "", n.hp)
		genmityOK := atoiField(skill["GEnmityTrigger"]) == 0
		enmityOK := atoiField(skill["EnmityTrigger"]) == 0
		id := uint32(atoiField(skill["SkiLLID"]))
		prop, known := rule.props[id]
		if !timeOK || !hpOK || !genmityOK || !enmityOK || !known || id < 20001 || id > 22999 || now < prop.InitialInterval {
			continue
		}
		if last, fired := n.lastFire[prop.Tier]; fired && now-last < prop.FireInterval() {
			continue
		}
		var target battleengine.Cell
		hasTarget := false
		switch atoiField(skill["SkillTrigger"]) {
		case 1, 2:
			player := match.adventureEnmityTarget(n, atoiField(skill["SkillTrigger"]) == 1)
			if player == 0 {
				return false
			}
			actor, _ := actorByID(match.engine.Actors(), player)
			target, hasTarget = actor.Position.Cell(), true
		}
		use := game.WorldUseItemEvent{PlayerID: n.id, ClientTime: now, ItemID: id, PosX: uint16(n.pos.X), PosY: uint16(n.pos.Y),
			Flag2: uint32(n.facing), Flag3: uint32(target.Row), Flag4: uint32(target.Col)}
		if hasTarget {
			use.Flag1 = 1
		}
		if body, err := use.MarshalNetworkBinary(); err == nil {
			match.queueNPC(&rule.outbox, n.id, game.NotifyPlayerUseItem, now, body)
			match.mirror(game.NotifyPlayerUseItem, body)
		}
		match.startAdventureCast(n, use)
		return true
	}
	return false
}

// adventureEnmityTarget is 0x005BC25F (highest) / 0x005BC398 (lowest).
func (match *onlineMatch) adventureEnmityTarget(n *advNPC, highest bool) uint16 {
	var chosen uint16
	best := 0
	for _, a := range match.livingAdventurePlayers() {
		if !n.boundsContain(a.Position.Cell()) {
			continue
		}
		v := n.enmity[a.PlayerID]
		if chosen == 0 || highest && v > best || !highest && v < best {
			chosen, best = a.PlayerID, v
		}
	}
	return chosen
}

// startAdventureCast is the 0x0FB0 receiver 0x006048BF: the prop prepares
// on the caster's cell and the caster stops (0x005F8624).
func (match *onlineMatch) startAdventureCast(n *advNPC, use game.WorldUseItemEvent) {
	rule := match.adventure
	for _, c := range rule.casts {
		if c.npc == n.id && c.guid == use.ClientTime {
			return // the TCP mirror of a multicast cast
		}
	}
	prop, ok := rule.props[use.ItemID]
	if !ok {
		match.logf("adventure NPC %d skill %d is not in the prop table", n.id, use.ItemID)
		return
	}
	pos := battleengine.Position{X: int32(use.PosX), Y: int32(use.PosY)}
	cast := &advCast{npc: n.id, prop: prop, guid: use.ClientTime, since: use.ClientTime, cell: pos.Cell(), pos: battleengine.PositionAtCellCenter(pos.Cell()),
		dir: int(use.Flag2), hasTarget: use.Flag1 != 0, target: battleengine.Cell{Row: int16(use.Flag3), Col: int16(use.Flag4)}}
	n.pos, n.path, n.route = cast.pos, nil, nil
	n.motion, n.motionKnown = battleengine.NativeNPCPosition{X: float32(cast.pos.X), Y: float32(cast.pos.Y)}, true
	n.facing, n.moving, n.navigator.walking = byte(cast.dir), false, false
	n.preparingUntil = use.ClientTime + prop.PrepareTime
	rule.casts = append(rule.casts, cast)
	match.logf("adventure NPC %d casts %d %s", n.id, prop.ID, prop.Name)
}

func (match *onlineMatch) adventureCastTick(now uint32) {
	rule := match.adventure
	kept := rule.casts[:0]
	for _, c := range rule.casts {
		if match.advanceAdventureCast(c, now) {
			kept = append(kept, c)
		}
	}
	rule.casts = kept
}

// advanceAdventureCast runs the prop state machine; false when it is done.
func (match *onlineMatch) advanceAdventureCast(c *advCast, now uint32) bool {
	rule := match.adventure
	arbitrator := match.isArbitrator()
	switch c.stage {
	case castPrepare:
		caster := rule.npcs[c.npc]
		if caster == nil || caster.dead {
			// Still the caster's own prop: it goes with the NPC [inferred].
			return false
		}
		if now-c.since < c.prop.PrepareTime {
			return true
		}
		caster.lastFire[c.prop.Tier] = now
		c.since = now
		switch {
		case c.prop.HasTrap:
			c.stage = castTrap
		case c.prop.HasMove:
			c.stage = castMove
		default:
			c.stage = castTrigger
		}
		return true
	case castTrap:
		if now-c.since >= c.prop.TrapTime {
			return false // 0x005F8AC0: the trap is removed
		}
		if !arbitrator {
			return true
		}
		for _, a := range match.livingAdventurePlayers() {
			if a.Position.Cell() == c.cell {
				c.hits, c.stage, c.since = []uint16{a.PlayerID}, castTrigger, now
				break
			}
		}
		return true
	case castMove:
		return match.moveAdventureCast(c, now)
	}
	return match.triggerAdventureCast(c, now)
}

// moveAdventureCast flies the prop toward its target cell (or MaxMoveDistance
// cells ahead) at MoveSpeed px/s; a hit or the end makes it fire.
func (match *onlineMatch) moveAdventureCast(c *advCast, now uint32) bool {
	start := battleengine.PositionAtCellCenter(c.cell)
	end := c.target
	if !c.hasTarget {
		d := bossDelta[c.dir&3]
		end = battleengine.Cell{Row: c.cell.Row + d[1]*int16(c.prop.MoveDistance), Col: c.cell.Col + d[0]*int16(c.prop.MoveDistance)}
	}
	goal := battleengine.PositionAtCellCenter(end)
	dx, dy := goal.X-start.X, goal.Y-start.Y
	length := abs32(dx) + abs32(dy)
	travel := int32(uint64(now-c.since) * uint64(max(c.prop.MoveSpeed, 1)) / 1000)
	if travel >= length || length == 0 {
		c.pos, c.stage, c.since = goal, castTrigger, now
		return true
	}
	pos := battleengine.Position{X: start.X + dx*travel/length, Y: start.Y + dy*travel/length}
	if cell := pos.Cell(); cell != c.pos.Cell() && c.prop.IgniteBomb && match.engine.IgniteBombAt(cell) {
		// IsIgniteBomb: a bubble in the new cell goes off at once (its fuse
		// -10,000,000) and the prop fires there.
		c.pos, c.stage, c.since = pos, castTrigger, now
		return true
	}
	if cell := pos.Cell(); cell != c.pos.Cell() && match.isArbitrator() {
		for _, a := range match.livingAdventurePlayers() {
			if a.Position.Cell() == cell {
				c.hits = append(c.hits, a.PlayerID)
			}
		}
		if len(c.hits) >= max(c.prop.HitPlayerPower, 1) {
			c.pos, c.stage, c.since = pos, castTrigger, now
			return true
		}
	}
	if tile, ok := match.engine.TileAt(pos.Cell()); !ok || tile.Kind != battleengine.CellOpen {
		c.pos, c.stage, c.since = pos, castTrigger, now
		return true
	}
	c.pos = pos
	return true
}

// affectArea is 0x005FC5C7: the closed row/column ranges of a shape.
func affectArea(center battleengine.Cell, factor, dir, shape int) (r0, r1, c0, c1 int16) {
	f := int16(factor)
	d := bossDelta[dir&3]
	switch shape {
	case 0:
		return center.Row - f, center.Row + f, center.Col - f, center.Col + f
	case 1:
		a := battleengine.Cell{Row: center.Row + d[1], Col: center.Col + d[0]}
		b := battleengine.Cell{Row: center.Row + d[1]*f, Col: center.Col + d[0]*f}
		return min(a.Row, b.Row), max(a.Row, b.Row), min(a.Col, b.Col), max(a.Col, b.Col)
	case 2, 3:
		front := center
		if shape == 2 {
			front = battleengine.Cell{Row: center.Row + d[1], Col: center.Col + d[0]}
		}
		if d[0] != 0 { // facing left/right: a column across
			return front.Row - f, front.Row + f, front.Col, front.Col
		}
		return front.Row, front.Row, front.Col - f, front.Col + f
	}
	return 0, 0, 0, 0
}

// triggerAdventureCast fires the prop: the arbitrator picks and announces
// the targets (0x10F7), then every client lands the effects in turn.
func (match *onlineMatch) triggerAdventureCast(c *advCast, now uint32) bool {
	rule := match.adventure
	if !c.triggerSet && c.prop.AffectObject == 0 {
		// Every client targets the caster itself; no 0x10F7.
		c.targets, c.triggerSet, c.since = []uint16{c.npc}, true, now
	}
	if !c.triggerSet {
		if !match.isArbitrator() {
			return now-c.since < 5000 // waiting for the arbitrator's 0x10F7
		}
		c.targets = match.adventureCastTargets(c)
		c.triggerSet, c.since = true, now
		match.logf("adventure NPC %d skill %d fires on %v", c.npc, c.prop.ID, c.targets)
		caster := rule.npcs[c.npc]
		trigger := game.PropTriggerEvent{PropGUID: c.guid, GameTime: now, UserID: c.npc, Dir: byte(c.dir), TypeID: c.prop.ID}
		if caster != nil {
			trigger.PosX, trigger.PosY = uint32(caster.pos.X), uint32(caster.pos.Y)
		}
		for _, id := range c.targets {
			x, y := match.adventureRolePosition(id)
			trigger.AffectedPlayers = append(trigger.AffectedPlayers, game.PropTriggerAffectedPlayer{PlayerID: id, PosX: uint32(x), PosY: uint32(y)})
		}
		if body, err := trigger.MarshalNetworkBinary(); err == nil {
			match.queueNPC(&rule.outbox, c.npc, game.NotifyPropTrigger, now, body)
			match.mirror(game.NotifyPropTrigger, body)
		} else {
			match.logf("NOTIFY_PROP_TRIGGER: %v", err)
		}
	}
	count := max(c.prop.AffectCount, 1)
	for c.applied < count && now-c.since >= c.prop.Delay+uint32(c.applied)*c.prop.AffectTime/uint32(count) {
		match.applyAdventureEffects(c, now)
		c.applied++
	}
	if c.applied >= count && now-c.since > c.prop.AffectTime {
		match.restoreAdventureEffects(c)
		return false
	}
	return true
}

func (match *onlineMatch) adventureRolePosition(id uint16) (int32, int32) {
	if n := match.adventure.npcs[id]; n != nil {
		return n.pos.X, n.pos.Y
	}
	a, _ := actorByID(match.engine.Actors(), id)
	return a.Position.X, a.Position.Y
}

// adventureCastTargets is §6.5: friends are NPCs, enemies players, for an
// NPC caster; AffectState 1 skips the dead.
func (match *onlineMatch) adventureCastTargets(c *advCast) []uint16 {
	rule := match.adventure
	if len(c.hits) > 0 {
		return c.hits
	}
	center := c.pos.Cell()
	if c.hasTarget {
		center = c.target
	}
	r0, r1, c0, c1 := affectArea(center, c.prop.AffectFactor, c.dir, c.prop.AffectShape)
	inside := func(cell battleengine.Cell) bool {
		return cell.Row >= r0 && cell.Row <= r1 && cell.Col >= c0 && cell.Col <= c1
	}
	type role struct {
		id     uint16
		cell   battleengine.Cell
		friend bool
	}
	var roles []role
	for _, n := range rule.orderedNPCs() {
		if !n.dead {
			roles = append(roles, role{n.id, n.pos.Cell(), true})
		}
	}
	for _, a := range match.livingAdventurePlayers() {
		roles = append(roles, role{a.PlayerID, a.Position.Cell(), false})
	}
	var out []uint16
	switch c.prop.AffectObject {
	case 0:
		return []uint16{c.npc}
	case 1, 2:
		best, bestDistance := uint16(0), int32(1<<30)
		for _, r := range roles {
			if r.friend != (c.prop.AffectObject == 1) || r.id == c.npc || !inside(r.cell) {
				continue
			}
			if d := abs32(int32(r.cell.Row-center.Row)) + abs32(int32(r.cell.Col-center.Col)); d <= bestDistance {
				best, bestDistance = r.id, d
			}
		}
		if best != 0 {
			out = append(out, best)
		}
	case 3, 4, 5:
		for _, r := range roles {
			if inside(r.cell) && (c.prop.AffectObject == 5 || r.friend == (c.prop.AffectObject == 3)) {
				out = append(out, r.id)
			}
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// applyAdventureEffects is one landing of 0x005FA37A's effects.
func (match *onlineMatch) applyAdventureEffects(c *advCast, now uint32) {
	rule := match.adventure
	for _, id := range c.targets {
		n := rule.npcs[id]
		p := rule.players[id]
		if p != nil {
			match.logf("adventure skill %d lands on player %d: %v", c.prop.ID, id, c.prop.Effects)
		}
		for name, v := range c.prop.Effects {
			switch name {
			case "HP":
				switch {
				case v < 0 && id == match.local && p != nil && !p.dead && p.hp > 0 && now >= p.protectedUntil:
					// 0x005B57DB: only the victim's own client reports it.
					if actor, ok := actorByID(match.engine.Actors(), id); ok {
						match.adventureHarmPlayer(id, actor.Position, max(0, p.hp+int(v)), now)
					}
				case v > 0 && n != nil:
					n.hp = min(n.maxHP, n.hp+int(v))
				case v > 0 && p != nil && !p.dead:
					p.hp = min(p.maxHP, p.hp+int(v))
				}
			case "SetTotalPercentHP":
				if n != nil {
					n.hp = int(float64(n.maxHP) * v)
				}
			case "SetCurPercentHP":
				if n != nil {
					n.hp = max(1, int(float64(n.hp)*v))
				}
			case "Defence", "Defense":
				if n != nil {
					n.defense += int(v)
					c.remember(id, name, v)
				}
			case "TouchHarmValue":
				if n != nil {
					n.harm += int(v)
					c.remember(id, name, v)
				}
			case "CanUseMoveKey":
				if n != nil && v == 0 {
					n.moveLocks++
					n.path = nil
					n.navigator.walking, n.moving = false, false
					c.remember(id, name, 1)
				} else if p != nil && v == 0 {
					p.moveLocks++
					c.remember(id, name, 1)
				}
			case "CanUseBombKey":
				if p != nil && v == 0 {
					p.bombLocks++
					c.remember(id, name, 1)
				}
			case "CanUseItemKey":
				if p != nil && v == 0 {
					p.itemLocks++
					c.remember(id, name, 1)
				}
			case "IsSlip":
				if p != nil && v != 0 {
					match.engine.ApplyAdventureSlip(id) // only switched on; restore does nothing
				}
			case "IsReversalControl":
				if p != nil && v != 0 {
					p.reversed++
					c.remember(id, name, 1)
				}
			case "SpeedDel", "BombPowerDel", "BombCountDel":
				if p != nil {
					var d [3]int
					d[map[string]int{"SpeedDel": 0, "BombPowerDel": 1, "BombCountDel": 2}[name]] = int(v)
					if err := match.engine.AdjustAdventureAttributes(id, d[0], d[1], d[2]); err == nil {
						c.remember(id, name, v)
					}
				}
			case "SpecialAvatarType":
				// The common avatar path with the prop's AffectTime as its
				// lifetime; RestorePlayerAttr does not undo it (v < 1 returns).
				// Only the look is played [the avatars' own slots are not].
				if v >= 1 && p != nil {
					p.avatar, p.avatarUntil = uint16(v), now+c.prop.AffectTime
				} else if v >= 1 && n != nil {
					n.avatar, n.avatarUntil = uint16(v), now+c.prop.AffectTime
				}
			case "Enmity":
				if p != nil && match.isArbitrator() {
					for _, other := range rule.npcs {
						if !other.dead {
							other.addEnmity(id, int(v))
						}
					}
				}
			}
		}
	}
}

func (c *advCast) remember(id uint16, name string, v float64) {
	if !c.prop.RestoreAttr {
		return
	}
	if c.restore == nil {
		c.restore = map[uint16]map[string]float64{}
	}
	if c.restore[id] == nil {
		c.restore[id] = map[string]float64{}
	}
	c.restore[id][name] += v
}

func (match *onlineMatch) restoreAdventureEffects(c *advCast) {
	for id, effects := range c.restore {
		if p := match.adventure.players[id]; p != nil {
			for name, v := range effects {
				switch name {
				case "CanUseMoveKey":
					p.moveLocks -= int(v)
				case "CanUseBombKey":
					p.bombLocks -= int(v)
				case "CanUseItemKey":
					p.itemLocks -= int(v)
				case "IsReversalControl":
					p.reversed -= int(v)
				case "SpeedDel":
					_ = match.engine.AdjustAdventureAttributes(id, -int(v), 0, 0)
				case "BombPowerDel":
					_ = match.engine.AdjustAdventureAttributes(id, 0, -int(v), 0)
				case "BombCountDel":
					_ = match.engine.AdjustAdventureAttributes(id, 0, 0, -int(v))
				}
			}
			continue
		}
		n := match.adventure.npcs[id]
		if n == nil {
			continue
		}
		for name, v := range effects {
			switch name {
			case "Defence", "Defense":
				n.defense -= int(v)
			case "TouchHarmValue":
				n.harm -= int(v)
			case "CanUseMoveKey":
				n.moveLocks -= int(v)
			}
		}
	}
}

// adventureSkillRemote takes the arbitrator's 0x0FB0 and 0x10F7.
func (match *onlineMatch) adventureSkillRemote(event game.GameEvent) bool {
	rule := match.adventure
	switch event.Schema {
	case game.NotifyPlayerUseItem:
		use, err := game.ParseWorldUseItemEvent(event)
		if err != nil || !isAdventureNPC(use.PlayerID) {
			return false
		}
		if n := rule.npcs[use.PlayerID]; n != nil && !match.isArbitrator() {
			match.startAdventureCast(n, use)
		}
		return true
	case game.NotifyPropTrigger:
		trigger, err := game.ParsePropTriggerEvent(event)
		if err != nil || !isAdventureNPC(trigger.UserID) {
			return false
		}
		if match.isArbitrator() {
			return true
		}
		for _, c := range rule.casts {
			if c.npc == trigger.UserID && c.guid == trigger.PropGUID && !c.triggerSet {
				c.stage, c.triggerSet, c.since, c.targets = castTrigger, true, match.engine.ElapsedMS(), nil
				for _, a := range trigger.AffectedPlayers {
					c.targets = append(c.targets, a.PlayerID)
				}
			}
		}
		return true
	}
	return false
}

func (match *onlineMatch) adventureCastView() []advCastView {
	var out []advCastView
	for _, c := range match.adventure.casts {
		a := c.prop.Anims
		view := advCastView{ID: c.prop.ID, Stage: byte(c.stage), Since: c.since, Caster: c.npc, Dir: c.dir, Row: c.cell.Row, Col: c.cell.Col, X: c.pos.X, Y: c.pos.Y,
			Anims: a, Offsets: [2][2]int{{a.PrepareX, a.PrepareY}, {a.AffectX, a.AffectY}}}
		if c.stage == castTrigger && c.triggerSet {
			center := c.pos.Cell()
			if c.hasTarget {
				center = c.target
			}
			r0, r1, c0, c1 := affectArea(center, c.prop.AffectFactor, c.dir, c.prop.AffectShape)
			for row := r0; row <= r1; row++ {
				for col := c0; col <= c1; col++ {
					view.Cells = append(view.Cells, cell{Row: row, Col: col})
				}
			}
			view.Targets = c.targets
		}
		out = append(out, view)
	}
	return out
}

// adventureItemsLocked is CanUseItemKey=0 on the local player.
func (match *onlineMatch) adventureItemsLocked() bool {
	if match.adventure == nil {
		return false
	}
	p := match.adventure.players[match.local]
	return p != nil && p.itemLocks > 0
}

// adventureInput holds the local player's keys under skill control effects.
func (match *onlineMatch) adventureInput(action *battleengine.Action) {
	if match.adventure == nil {
		return
	}
	p := match.adventure.players[match.local]
	if p == nil {
		return
	}
	if p.bombLocks > 0 {
		action.PlaceBomb = false
	}
	if p.itemLocks > 0 {
		action.UseActionID = 0
	}
	if p.moveLocks > 0 {
		action.Move = battleengine.DirectionNone
	} else if p.reversed > 0 {
		action.Move = map[battleengine.Direction]battleengine.Direction{battleengine.DirectionUp: battleengine.DirectionDown, battleengine.DirectionDown: battleengine.DirectionUp,
			battleengine.DirectionLeft: battleengine.DirectionRight, battleengine.DirectionRight: battleengine.DirectionLeft}[action.Move]
	}
}
