package main

// Rule 13 (糖客战). Client.exe 00608AD2/007A4250 supplies ordinary combat
// and five-second respawns. 005B748F builds two bases, in GAME_BEGIN team
// order. Each is element 19031: a 3x3 cross with solid corners. Only a blast
// through its centre adds the base's anchor to the explosion element list
// (005D4937). 005F6580 subtracts 10 HP, or 5 for ten seconds after 005F65B8;
// 005F65CA repairs 30 HP but never resurrects a destroyed base.
//
// The arbitrator reports base damage using the explosion packet's sender ID
// (006075D0). The original repair callback also sends from the arbitrator,
// naming the collector. To retain the server's session identity check, our
// collector sends its own +30 after the same authenticated pickup. Every
// client applies the same explosion/pickup locally: 0x15B3 has no downlink.

import (
	"encoding/binary"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	tankBaseElement = 19031
	tankRepairScene = 119 // factory 005CEC6B -> pickup 005CECAB
	tankArmorScene  = 120 // factory 005CED3A -> pickup 005CED7A
	tankArmorMS     = 10000
)

type tankBase struct {
	team       byte
	anchor     battleengine.Cell
	hp         int
	armorUntil uint32
}
type tankRule struct{ bases [2]tankBase }

func newTankRule(entry mapdata.CompetitiveMap, begin game.GameBeginData) *tankRule {
	rule := &tankRule{}
	teams := firstTwoTeams(begin)
	for i, cell := range entry.ObjectiveCells {
		if i == len(rule.bases) {
			break
		}
		rule.bases[i] = tankBase{team: teams[i], anchor: battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)}, hp: 100}
	}
	return rule
}
func (rule *tankRule) base(team byte) *tankBase {
	for i := range rule.bases {
		if rule.bases[i].team == team {
			return &rule.bases[i]
		}
	}
	return nil
}
func (base tankBase) centre() battleengine.Cell {
	return battleengine.Cell{Row: base.anchor.Row + 1, Col: base.anchor.Col + 1}
}
func (rule *tankRule) atAnchor(cell battleengine.Cell) bool {
	for _, base := range rule.bases {
		if base.anchor == cell {
			return true
		}
	}
	return false
}

// installTankBases reproduces the runtime element registration, which is not
// in the serialized .map. Hidden wall-item allocation has already happened.
func installTankBases(config *battleengine.Config, rule *tankRule) {
	for _, base := range rule.bases {
		for dr := int16(0); dr < 3; dr++ {
			for dc := int16(0); dc < 3; dc++ {
				cell := battleengine.Cell{Row: base.anchor.Row + dr, Col: base.anchor.Col + dc}
				if _, ok := config.Grid.Cell(cell); !ok {
					continue
				}
				tile := battleengine.Tile{Kind: battleengine.CellOpen, FlamePassable: true, MapElementOccupied: true, NativeGridAttrSet: true, NativePlayerExitAttrSet: true}
				if dr != 1 && dc != 1 {
					tile.Kind = battleengine.CellSolid
					tile.FlamePassable = false
				} else {
					tile.NativeGridAttr = 0x1f1f
					tile.NativePlayerExitAttr = 0x1f1f
				}
				config.Grid.Cells[int(cell.Row)*int(config.Grid.Width)+int(cell.Col)] = tile
			}
		}
	}
}

func tankBombHits(bomb game.ExplodedBomb, cell battleengine.Cell) bool {
	return int16(bomb.Row) == cell.Row && cell.Col >= int16(bomb.ColumnMin) && cell.Col <= int16(bomb.ColumnMax) ||
		int16(bomb.Column) == cell.Col && cell.Row >= int16(bomb.RowMin) && cell.Row <= int16(bomb.RowMax)
}

// tankExplosion is called once on the authority before publishing, or once on
// a follower after accepting the same explosion. A base appears once per batch.
func (match *onlineMatch) tankExplosion(explosion *game.BombExplodeEvent, author bool) {
	if match.tank == nil {
		return
	}
	for i := range match.tank.bases {
		base := &match.tank.bases[i]
		if base.hp == 0 {
			continue
		}
		if !author {
			present := false
			for _, hit := range explosion.MapElems {
				if hit.MapElementID == tankBaseElement && int16(hit.Row) == base.anchor.Row && int16(hit.Column) == base.anchor.Col {
					present = true
					break
				}
			}
			if !present {
				continue
			}
		}
		var owner uint16
		for _, bomb := range explosion.Bombs {
			if tankBombHits(bomb, base.centre()) {
				owner = match.arbitrator
				break
			}
		}
		if owner == 0 {
			continue
		}
		change := -10
		if base.armorUntil != 0 && explosion.ClientTime <= base.armorUntil {
			change = -5
		}
		base.hp = max(0, base.hp+change)
		if author {
			explosion.MapElems = append(explosion.MapElems, game.ExplodedMapElement{MapElementID: tankBaseElement, Row: byte(base.anchor.Row), Column: byte(base.anchor.Col)})
		}
		match.reportTankHP(owner, base.team, int16(change), explosion.ClientTime)
		match.logf("tank base team %d hit by %d: %+d HP -> %d", base.team, owner, change, base.hp)
	}
}

func (match *onlineMatch) tankPickup(event battleengine.Event) {
	if match.tank == nil || event.Kind != battleengine.EventPickupCollected {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), event.PlayerID)
	if !ok {
		return
	}
	base := match.tank.base(actor.TeamID)
	if base == nil || base.hp == 0 {
		return
	}
	switch event.SceneID {
	case tankRepairScene:
		if base.hp < 100 {
			base.hp = min(100, base.hp+30)
			match.reportTankHP(actor.PlayerID, base.team, 30, event.TimeMS)
			match.logf("tank base team %d repaired by %d: HP %d", base.team, actor.PlayerID, base.hp)
		}
	case tankArmorScene:
		base.armorUntil = event.TimeMS + tankArmorMS
		match.logf("tank base team %d armor until %d", base.team, base.armorUntil)
	}
}
func (match *onlineMatch) reportTankHP(player uint16, team byte, change int16, clock uint32) {
	if player != match.local {
		return
	}
	body := make([]byte, 10)
	binary.BigEndian.PutUint16(body, player)
	binary.BigEndian.PutUint32(body[2:], clock)
	binary.BigEndian.PutUint16(body[6:], uint16(team))
	binary.BigEndian.PutUint16(body[8:], uint16(change))
	match.mirror(game.RequestTankBaseHP, body)
}

type tankBaseView struct {
	Team       byte   `json:"team"`
	Row        int16  `json:"row"`
	Col        int16  `json:"col"`
	HP         int    `json:"hp"`
	ArmorUntil uint32 `json:"armor_until"`
}
type tankView struct {
	Bases []tankBaseView `json:"bases"`
}

func (rule *tankRule) view() *tankView {
	view := &tankView{}
	for _, base := range rule.bases {
		view.Bases = append(view.Bases, tankBaseView{base.team, base.anchor.Row, base.anchor.Col, base.hp, base.armorUntil})
	}
	return view
}

// worthBombing targets a clear line to the enemy base's centre. The normal
// bot still clears intervening walls and checks escape before placing a bomb.
func (rule *tankRule) worthBombing(world autoplayWorld, cell battleengine.Cell, self battleengine.Actor) bool {
	if world.bombs[cell] {
		return false
	}
	active := 0
	for _, bomb := range world.engine.Bombs() {
		if bomb.OwnerID == self.PlayerID {
			active++
		}
	}
	if active >= int(self.BombCapacity) {
		return false
	}
	for _, base := range rule.bases {
		if base.hp == 0 || base.team == self.TeamID {
			continue
		}
		target := base.centre()
		for _, direction := range autoplayDirections {
			next := cell
			for distance := 0; distance <= int(self.BombPower); distance++ {
				if next == target {
					return true
				}
				next = stepCell(next, direction)
				tile, ok := world.grid.Cell(next)
				if !ok || tile.Kind != battleengine.CellOpen {
					break
				}
			}
		}
	}
	return false
}
