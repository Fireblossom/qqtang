package main

// Rule 6, 英雄传说 (sculpture). Like rule 3 the scene is Client.exe's own and
// the server only mirrors the arbitrator's notifications.
//
// Two statues (map element 12008, one walkable cell, built at the start by
// 0x005B7355 and registered over whatever the map file has there) stand at
// the map's objective cells: the first belongs to the first player's team in
// GAME_BEGIN order, the second to the first player of another team.
//
// Fragments are GAME_BEGIN wall items 161..165 (materials 12..16). They take
// their wall-placement slots like ordinary items, but 0x005D9815 makes them
// objects of the loose list at once; one is invisible and out of reach while
// a map element stands on its cell (0x005F75A5). Flames never touch them.
//
// A player who carries nothing and stands on a loose fragment asks for it
// (0x006195B3: 0x0FB6, BunID 2, the material); a carrier standing on its own
// team's statue asks to deposit (0x0061972F: 0x0FB8, BunID 0, the material).
// The arbitrator checks (0x00603D53, 0x00603B0F) and answers with 0x0FB7 or
// 0x0FB9. Taking a fragment grants held item 66, which puts it down again
// (0x0060471C, 0x005F6AB8); depositing uses that item up (0x0060386F).
// A statue keeps up to four materials (0x005F5EB8).
//
// Deaths (0x0060583E, 0x00606606): a carrier killed by a player who carries
// nothing hands the fragment and an item 66 to the killer; otherwise the
// fragment drops loose on the message's cell. Players relive after 5 s
// without waiting for the spawn cell (rule vtable 0x7A43CC slots 4 and 5).

import (
	"strconv"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	sculptureFirstScene         = 161 // material 12
	sculptureFirstMaterial      = 12
	sculptureMaterials          = 5
	sculpturePieces             = 4
	sculptureDepositBun    byte = 0
	sculptureFragmentBun   byte = 2
	sculpturePutDownItem        = 66
	sculptureBombItem           = 48 // a style-9 bubble, one per player (0x0060A772)
	sculptureBombPower          = 2  // wire power of that bubble (0x005CDD0A)
)

// sculptureDropOffsets is 0x00607C5A's neighbour table, entries 1..8: right,
// up, left, down, then the diagonals.
var sculptureDropOffsets = [8]battleengine.Cell{{Row: 0, Col: 1}, {Row: -1, Col: 0}, {Row: 0, Col: -1}, {Row: 1, Col: 0}, {Row: -1, Col: 1}, {Row: -1, Col: -1}, {Row: 1, Col: -1}, {Row: 1, Col: 1}}

// sculptureHitOffsets is the order 0x00607C5A looks for a statue around a
// style-9 bubble (table 0x7F7450): centre, right, up, left, down.
var sculptureHitOffsets = [5]battleengine.Cell{{}, {Row: 0, Col: 1}, {Row: -1, Col: 0}, {Row: 0, Col: -1}, {Row: 1, Col: 0}}

type sculptureStatue struct {
	team     byte
	cell     battleengine.Cell
	progress int
	slots    [sculpturePieces]byte // materials in deposit order
	// The player whose deposit completed the statue; Client.exe picks the
	// hero image from it when every piece is material 12 (0x005F5EB8).
	completedBy uint16
}

type looseFragment struct {
	cell     battleengine.Cell
	material byte
}

type sculptureRule struct {
	engine  *battleengine.Engine
	statues [2]sculptureStatue
	carried map[uint16]int16 // by player: carried material, bunNone if none
	loose   []looseFragment
	// Players who have not used their style-9 bubble yet; Client.exe gives
	// it back after a death (0x005A9EB3).
	bombLeft map[uint16]bool
}

func newSculptureRule(engine *battleengine.Engine, entry mapdata.CompetitiveMap, begin game.GameBeginData, fragments []battleengine.Pickup) *sculptureRule {
	rule := &sculptureRule{engine: engine, carried: map[uint16]int16{}, bombLeft: map[uint16]bool{}}
	for _, fragment := range fragments {
		rule.loose = append(rule.loose, looseFragment{cell: fragment.Cell, material: byte(fragment.SceneID - sculptureFirstScene + sculptureFirstMaterial)})
	}
	for _, info := range begin.Players {
		rule.carried[info.PlayerID] = bunNone
	}
	teams := firstTwoTeams(begin)
	for index := range rule.statues {
		if index >= len(entry.ObjectiveCells) {
			break
		}
		cell := entry.ObjectiveCells[index]
		rule.statues[index] = sculptureStatue{team: teams[index], cell: battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)}}
	}
	return rule
}

// firstTwoTeams is the team of GAME_BEGIN's first player and of the first
// player of another team, as 0x005B720C and 0x005B7355 pick them.
func firstTwoTeams(begin game.GameBeginData) [2]byte {
	if len(begin.Players) == 0 {
		return [2]byte{}
	}
	first := begin.Players[0].TeamID
	for _, info := range begin.Players {
		if info.TeamID != first {
			return [2]byte{first, info.TeamID}
		}
	}
	return [2]byte{first, first}
}

func (rule *sculptureRule) statueAt(cell battleengine.Cell) *sculptureStatue {
	for index := range rule.statues {
		if rule.statues[index].cell == cell {
			return &rule.statues[index]
		}
	}
	return nil
}

// covered reports whether a map element (a wall, a pushed box) hides cell.
func (rule *sculptureRule) covered(cell battleengine.Cell) bool {
	tile, ok := rule.engine.TileAt(cell)
	return !ok || tile.Kind != battleengine.CellOpen
}

func (rule *sculptureRule) looseAt(cell battleengine.Cell, material byte, anyMaterial bool) int {
	for index, fragment := range rule.loose {
		if fragment.cell == cell && (anyMaterial || fragment.material == material) && !rule.covered(cell) {
			return index
		}
	}
	return -1
}

func (rule *sculptureRule) carrying(playerID uint16) int16 {
	if material, known := rule.carried[playerID]; known {
		return material
	}
	return bunNone
}

func (rule *sculptureRule) wanted(actor battleengine.Actor) (uint16, game.BunActionEvent, bool) {
	cell := actor.Position.Cell()
	carried := rule.carrying(actor.PlayerID)
	request := game.BunActionEvent{PlayerID: actor.PlayerID, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y)}
	if statue := rule.statueAt(cell); statue != nil && statue.team == actor.TeamID && carried != bunNone {
		request.BunID, request.BunTeamID = sculptureDepositBun, byte(carried)
		return game.RequestPutBun, request, true
	}
	if index := rule.looseAt(cell, 0, true); index >= 0 && carried == bunNone {
		request.BunID, request.BunTeamID = sculptureFragmentBun, rule.loose[index].material
		return game.RequestGetBun, request, true
	}
	return 0, request, false
}

func eventCell(event game.BunActionEvent) battleengine.Cell {
	return battleengine.Position{X: int32(event.PosX), Y: int32(event.PosY)}.Cell()
}

// allowTake is 0x00603D53 under rule 6: the BunID is not checked.
func (rule *sculptureRule) allowTake(request game.BunActionEvent, _ battleengine.Actor) bool {
	return rule.carrying(request.PlayerID) == bunNone && rule.looseAt(eventCell(request), request.BunTeamID, false) >= 0
}

// allowPut is 0x00603B0F under rule 6; neither the material nor the
// statue's progress is checked.
func (rule *sculptureRule) allowPut(request game.BunActionEvent, putter battleengine.Actor) bool {
	statue := rule.statueAt(eventCell(request))
	return rule.carrying(request.PlayerID) != bunNone && statue != nil && statue.team == putter.TeamID
}

// applyTake is 0x0FB7 (0x006039D0): the fragment is carried and the taker
// gets the put-down item.
func (rule *sculptureRule) applyTake(event game.BunActionEvent) {
	index := rule.looseAt(eventCell(event), event.BunTeamID, false)
	if index < 0 {
		return
	}
	rule.loose = append(rule.loose[:index], rule.loose[index+1:]...)
	rule.carried[event.PlayerID] = int16(event.BunTeamID)
	rule.engine.GrantHeldAction(event.PlayerID, sculpturePutDownItem, 1)
}

// applyPut is 0x0FB9 (0x0060386F, 0x005F5EB8).
func (rule *sculptureRule) applyPut(event game.BunActionEvent) {
	statue := rule.statueAt(eventCell(event))
	if statue == nil {
		return
	}
	if statue.progress < sculpturePieces {
		statue.slots[statue.progress] = event.BunTeamID
		statue.progress++
		if statue.progress == sculpturePieces {
			statue.completedBy = event.PlayerID
		}
	}
	rule.carried[event.PlayerID] = bunNone
	rule.engine.ConsumeHeldAction(event.PlayerID, sculpturePutDownItem)
}

// putDown is held item 66 (0x0060471C, 0x005F6AB8): a carried fragment is
// left on the user's cell.
func (rule *sculptureRule) putDown(playerID uint16, position battleengine.Position) {
	if material := rule.carrying(playerID); material != bunNone {
		rule.loose = append(rule.loose, looseFragment{cell: position.Cell(), material: byte(material)})
		rule.carried[playerID] = bunNone
	}
	rule.engine.ConsumeHeldAction(playerID, sculpturePutDownItem)
}

func (rule *sculptureRule) onDeath(victim, killer uint16, killed bool, position battleengine.Position) {
	if rule.bombLeft[victim] {
		// The engine empties a dead player's slots; an unused bubble stays.
		rule.engine.GrantHeldAction(victim, sculptureBombItem, 1)
	}
	material := rule.carrying(victim)
	if material == bunNone {
		return
	}
	rule.carried[victim] = bunNone
	rule.engine.ConsumeHeldAction(victim, sculpturePutDownItem)
	if killed && killer != 0 && killer != victim && rule.carrying(killer) == bunNone {
		if _, known := rule.carried[killer]; known {
			rule.carried[killer] = material
			rule.engine.GrantHeldAction(killer, sculpturePutDownItem, 1)
			return
		}
	}
	rule.loose = append(rule.loose, looseFragment{cell: position.Cell(), material: byte(material)})
}

// onDeparture is slot 8 (0060ABBE): recycle the carried material into the
// next pickup-bird batch on every peer, including a prospective arbitrator.
// Native 0060ABBE uses 161 for material 12 but mistakenly 13..16 for the
// others. Those are not valid fragment scene IDs. This departure-only
// compatibility correction preserves the special pieces as scenes 162..165.
func (rule *sculptureRule) onDeparture(playerID uint16) {
	if material := rule.carrying(playerID); material != bunNone {
		_ = rule.engine.RecycleLooseObject(uint32(material - sculptureFirstMaterial + sculptureFirstScene))
	}
	rule.carried[playerID] = bunNone
	delete(rule.bombLeft, playerID)
}

// worthBombing says whether a style-9 bubble at cell would cost the
// opposing statue a piece (for the test bot).
func (rule *sculptureRule) worthBombing(cell battleengine.Cell, actor battleengine.Actor) bool {
	if !rule.bombLeft[actor.PlayerID] {
		return false
	}
	for _, offset := range sculptureHitOffsets {
		if statue := rule.statueAt(battleengine.Cell{Row: cell.Row + offset.Row, Col: cell.Col + offset.Col}); statue != nil {
			return statue.team != actor.TeamID && statue.progress > 0 && statue.progress < sculpturePieces
		}
	}
	return false
}

// damage is 0x00607C5A for a style-9 bubble that exploded at cell, placed
// at placedAt: the first statue at the centre or a side cell loses its
// latest piece unless it is empty or complete, and the piece drops on the
// first neighbour without a map element, starting from (placedAt & 7) + 1.
func (rule *sculptureRule) damage(cell battleengine.Cell, placedAt uint32) {
	for _, offset := range sculptureHitOffsets {
		statue := rule.statueAt(battleengine.Cell{Row: cell.Row + offset.Row, Col: cell.Col + offset.Col})
		if statue == nil {
			continue
		}
		if statue.progress <= 0 || statue.progress >= sculpturePieces {
			return
		}
		statue.progress--
		material := statue.slots[statue.progress]
		statue.slots[statue.progress] = 0
		start := int(placedAt & 7)
		for try := 0; try < len(sculptureDropOffsets); try++ {
			next := sculptureDropOffsets[(start+try)%len(sculptureDropOffsets)]
			drop := battleengine.Cell{Row: statue.cell.Row + next.Row, Col: statue.cell.Col + next.Col}
			if !rule.covered(drop) {
				rule.loose = append(rule.loose, looseFragment{cell: drop, material: material})
				return
			}
		}
		return
	}
}

// sculptureView is the rule state the client draws.
type sculptureView struct {
	Statues []sculptureStatueView `json:"statues"`
	Loose   []sculptureLooseView  `json:"loose"`
	Carried map[string]int16      `json:"carried"` // by player ID: material, -1 none
}

type sculptureStatueView struct {
	Row         int16  `json:"row"`
	Col         int16  `json:"col"`
	Team        byte   `json:"team"`
	First       bool   `json:"first"`
	Progress    int    `json:"progress"`
	Slots       []int  `json:"slots"` // materials in deposit order (not []byte: JSON would make it base64)
	CompletedBy uint16 `json:"completed_by"`
}

type sculptureLooseView struct {
	Row      int16 `json:"row"`
	Col      int16 `json:"col"`
	Material byte  `json:"material"`
	Hidden   bool  `json:"hidden"` // under a map element
}

func (rule *sculptureRule) view() *sculptureView {
	view := &sculptureView{Carried: map[string]int16{}, Loose: []sculptureLooseView{}}
	for index, statue := range rule.statues {
		view.Statues = append(view.Statues, sculptureStatueView{
			Row: statue.cell.Row, Col: statue.cell.Col, Team: statue.team, First: index == 0,
			Progress: statue.progress, Slots: materials(statue.slots[:min(statue.progress, sculpturePieces)]), CompletedBy: statue.completedBy,
		})
	}
	for _, fragment := range rule.loose {
		view.Loose = append(view.Loose, sculptureLooseView{Row: fragment.cell.Row, Col: fragment.cell.Col, Material: fragment.material, Hidden: rule.covered(fragment.cell)})
	}
	for playerID, material := range rule.carried {
		view.Carried[strconv.Itoa(int(playerID))] = material
	}
	return view
}

func materials(slots []byte) []int {
	result := make([]int, len(slots))
	for index, material := range slots {
		result[index] = int(material)
	}
	return result
}

// markStyleNine tags the style-9 bubbles for drawing (object/bomb/bomb9).
func (match *onlineMatch) markStyleNine(bombs []bombView) {
	for index := range bombs {
		if match.styleNine[bombs[index].ID] {
			bombs[index].Style = 9
		}
	}
}
