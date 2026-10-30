package main

// Rule 3, 抢包子 (bun). Client.exe keeps the whole rule in its scene; the
// server only mirrors the arbitrator's notifications to decide the round.
//
// Two houses (CMEBunShop, map element 8009, 3x3, set up by 0x005B720C) stand
// at the map's two objective cells (top-left anchors): the first belongs to
// the first player's team in GAME_BEGIN order, the second to the first
// player of another team. Each starts with (players / 2) buns of its own
// colour (+0x44) and stores captured buns of the other colour apart (+0x46);
// captured buns can never be taken back.
//
// A player standing in a house (its walkable cross; the corners are solid)
// asks the arbitrator (0x006153DB): in an opposing house with a bun left and
// nothing carried, to take one (0x0FB6, BunID 0, the house's colour); in its
// own house while carrying, to put it down (0x0FB8, the carried colour). A
// player who carries nothing and stands on a loose bun asks for that
// (0x006195B3: 0x0FB6, BunID 1). The arbitrator checks the request against
// its own scene (0x00603D53, 0x00603B0F) and answers with 0x0FB7 or 0x0FB9,
// which every client applies (0x006039D0, 0x0060386F). A refused request
// gets no answer.
//
// Deaths are applied from the death messages themselves (0x0060583E,
// 0x00606606): a carrier killed by a player who carries nothing hands the
// bun to the killer; otherwise (the killer carries one, or the carrier's
// bubble popped) it drops loose on the cell of the message's position.

import (
	"strconv"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	bunNone               = -1
	bunHouseSize          = 3
	bunHomeBun       byte = 0   // BunID: a bun taken from a house
	bunLooseBun      byte = 1   // BunID: a bun lying on the ground
	bunRequestPeriod      = 500 // ms between repeated requests for the same thing
)

type bunHouse struct {
	team     byte
	anchor   battleengine.Cell // top-left of the 3x3 house
	own      int16             // buns of the house's colour (+0x44)
	captured int16             // buns brought in from the other house (+0x46)
}

func (house bunHouse) contains(cell battleengine.Cell) bool {
	return cell.Row >= house.anchor.Row && cell.Row < house.anchor.Row+bunHouseSize &&
		cell.Col >= house.anchor.Col && cell.Col < house.anchor.Col+bunHouseSize
}

type looseBun struct {
	cell battleengine.Cell
	team byte
}

type bunRule struct {
	houses  [2]bunHouse
	carried map[uint16]int16 // by player: carried colour, bunNone if none
	loose   []looseBun
	// The local player's last request and when it was sent, so standing in a
	// house does not flood the arbitrator.
	lastRequest     game.BunActionEvent
	lastRequestSent uint32
	lastRequestKind uint16
}

// newBunRule sets the houses up as 0x005B720C does.
func newBunRule(entry mapdata.CompetitiveMap, begin game.GameBeginData) *bunRule {
	rule := &bunRule{carried: map[uint16]int16{}}
	if len(begin.Players) == 0 || len(entry.ObjectiveCells) < 2 {
		return rule
	}
	first := begin.Players[0].TeamID
	second := first
	for _, info := range begin.Players {
		if info.TeamID != first {
			second = info.TeamID
			break
		}
	}
	// (players - scene +0x3844) / 2; the subtrahend is taken to be 0
	// (inferred), which gives one bun per member of equal teams, as the
	// server's ledger assumes.
	count := int16(len(begin.Players) / 2)
	for index, team := range [2]byte{first, second} {
		cell := entry.ObjectiveCells[index]
		rule.houses[index] = bunHouse{team: team, anchor: battleengine.Cell{Row: int16(cell.Row), Col: int16(cell.Col)}, own: count}
	}
	for _, info := range begin.Players {
		rule.carried[info.PlayerID] = bunNone
	}
	return rule
}

func (rule *bunRule) houseAt(cell battleengine.Cell) *bunHouse {
	for index := range rule.houses {
		if rule.houses[index].contains(cell) {
			return &rule.houses[index]
		}
	}
	return nil
}

func (rule *bunRule) looseAt(cell battleengine.Cell, team byte, anyTeam bool) int {
	for index, bun := range rule.loose {
		if bun.cell == cell && (anyTeam || bun.team == team) {
			return index
		}
	}
	return -1
}

func (rule *bunRule) carrying(playerID uint16) int16 {
	if team, known := rule.carried[playerID]; known {
		return team
	}
	return bunNone
}

// wanted is the request the local player's position calls for, if any.
func (rule *bunRule) wanted(actor battleengine.Actor) (uint16, game.BunActionEvent, bool) {
	cell := actor.Position.Cell()
	carried := rule.carrying(actor.PlayerID)
	request := game.BunActionEvent{PlayerID: actor.PlayerID, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y)}
	if house := rule.houseAt(cell); house != nil {
		switch {
		case house.team == actor.TeamID && carried != bunNone:
			request.BunID, request.BunTeamID = bunHomeBun, byte(carried)
			return game.RequestPutBun, request, true
		case house.team != actor.TeamID && carried == bunNone && house.own >= 1:
			request.BunID, request.BunTeamID = bunHomeBun, house.team
			return game.RequestGetBun, request, true
		}
		return 0, request, false
	}
	if index := rule.looseAt(cell, 0, true); index >= 0 && carried == bunNone {
		request.BunID, request.BunTeamID = bunLooseBun, rule.loose[index].team
		return game.RequestGetBun, request, true
	}
	return 0, request, false
}

// allowTake is the arbitrator's check of a take request (0x00603D53).
func (rule *bunRule) allowTake(request game.BunActionEvent, taker battleengine.Actor) bool {
	if rule.carrying(request.PlayerID) != bunNone {
		return false
	}
	cell := battleengine.Position{X: int32(request.PosX), Y: int32(request.PosY)}.Cell()
	switch request.BunID {
	case bunHomeBun:
		house := rule.houseAt(cell)
		return house != nil && house.team != taker.TeamID && house.own >= 1
	case bunLooseBun:
		return rule.looseAt(cell, request.BunTeamID, false) >= 0
	}
	return false
}

// allowPut is the arbitrator's check of a put request (0x00603B0F).
func (rule *bunRule) allowPut(request game.BunActionEvent, putter battleengine.Actor) bool {
	if rule.carrying(request.PlayerID) == bunNone {
		return false
	}
	house := rule.houseAt(battleengine.Position{X: int32(request.PosX), Y: int32(request.PosY)}.Cell())
	return house != nil && house.team == putter.TeamID
}

// applyTake is 0x0FB7 (0x006039D0).
func (rule *bunRule) applyTake(event game.BunActionEvent) {
	cell := battleengine.Position{X: int32(event.PosX), Y: int32(event.PosY)}.Cell()
	switch event.BunID {
	case bunHomeBun:
		house := rule.houseAt(cell)
		if house == nil {
			return
		}
		// 0x0061555C takes only a bun of the house's own colour.
		if event.BunTeamID == house.team && house.own > 0 {
			house.own--
		}
	case bunLooseBun:
		index := rule.looseAt(cell, event.BunTeamID, false)
		if index < 0 {
			return
		}
		rule.loose = append(rule.loose[:index], rule.loose[index+1:]...)
	default:
		return
	}
	rule.carried[event.PlayerID] = int16(event.BunTeamID)
}

// applyPut is 0x0FB9 (0x0060386F, 0x00615546).
func (rule *bunRule) applyPut(event game.BunActionEvent) {
	house := rule.houseAt(battleengine.Position{X: int32(event.PosX), Y: int32(event.PosY)}.Cell())
	if house == nil {
		return
	}
	if event.BunTeamID == house.team {
		house.own++
	} else {
		house.captured++
	}
	rule.carried[event.PlayerID] = bunNone
}

// onDeath applies a death message: killed says it is a 0x0FA9 with killer.
func (rule *bunRule) onDeath(victim, killer uint16, killed bool, position battleengine.Position) {
	bun := rule.carrying(victim)
	if bun == bunNone {
		return
	}
	if killed && killer != 0 && killer != victim && rule.carrying(killer) == bunNone {
		if _, known := rule.carried[killer]; known {
			rule.carried[killer] = bun
			rule.carried[victim] = bunNone
			return
		}
	}
	rule.loose = append(rule.loose, looseBun{cell: position.Cell(), team: byte(bun)})
	rule.carried[victim] = bunNone
}

// bunView is the rule state the client draws.
type bunView struct {
	Houses  []bunHouseView   `json:"houses"`
	Loose   []bunLooseView   `json:"loose"`
	Carried map[string]int16 `json:"carried"` // by player ID; -1 none
	First   byte             `json:"first"`   // the first house's team: its buns are cap 22 / misc161
}

type bunHouseView struct {
	Row      int16 `json:"row"`
	Col      int16 `json:"col"`
	Team     byte  `json:"team"`
	Own      int16 `json:"own"`
	Captured int16 `json:"captured"`
}

type bunLooseView struct {
	Row  int16 `json:"row"`
	Col  int16 `json:"col"`
	Team byte  `json:"team"`
}

func (rule *bunRule) view() *bunView {
	view := &bunView{Carried: map[string]int16{}, First: rule.houses[0].team, Loose: []bunLooseView{}}
	for _, house := range rule.houses {
		view.Houses = append(view.Houses, bunHouseView{Row: house.anchor.Row, Col: house.anchor.Col, Team: house.team, Own: house.own, Captured: house.captured})
	}
	for _, bun := range rule.loose {
		view.Loose = append(view.Loose, bunLooseView{Row: bun.cell.Row, Col: bun.cell.Col, Team: bun.team})
	}
	for playerID, team := range rule.carried {
		view.Carried[strconv.Itoa(int(playerID))] = team
	}
	return view
}
