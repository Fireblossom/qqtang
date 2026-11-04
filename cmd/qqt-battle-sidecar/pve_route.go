package main

import "qqtang/internal/game/battleengine"

// PVE +900 stores three-byte nodes {col,row,direction}, unlike the ordinary
// Boss's six-byte WF nodes. A goal node retains direction9 after prefix removal.
type pveRouteNode struct {
	cell      battleengine.Cell
	direction byte
}
type pveRoute []pveRouteNode

func pveRouteFromCells(cells []battleengine.Cell) pveRoute {
	var route pveRoute
	for i, cell := range cells {
		direction := byte(bossDirStop)
		if i+1 < len(cells) {
			direction = byte(directionBetween(cell, cells[i+1]))
		}
		route = append(route, pveRouteNode{cell: cell, direction: direction})
	}
	return route
}

// 005BB9F1 removes the prefix before the first current-cell match. A miss
// leaves the vector intact; finding the goal can leave a single dir9 node.
func (route *pveRoute) findCurrent(cell battleengine.Cell) bool {
	for i, node := range *route {
		if node.cell == cell {
			*route = (*route)[i:]
			return true
		}
	}
	return false
}

func pveRouteCenter(position battleengine.NativeNPCPosition) bool {
	cell := position.Pixels().Cell()
	x, y := float32(cell.Col)*40, float32(cell.Row)*40
	return position.X >= x+10 && position.X <= x+30 && position.Y >= y+10 && position.Y <= y+30
}

// 005BB7F6 checks all old nodes before trimming, including the current node
// and the goal. The original repeats its map-element lookup/predicate.
func (route *pveRoute) valid(cell battleengine.Cell, targetMatches bool, bomb func(battleengine.Cell) bool, element func(pveRouteNode) bool) bool {
	if !targetMatches {
		return false
	}
	for _, node := range *route {
		if bomb(node.cell) || element(node) || element(node) {
			return false
		}
	}
	return route.findCurrent(cell)
}

func (route *pveRoute) reuse(position battleengine.NativeNPCPosition, targetMatches bool, bomb func(battleengine.Cell) bool, element func(pveRouteNode) bool) (byte, bool) {
	if len(*route) <= 1 || !route.valid(position.Pixels().Cell(), targetMatches, bomb, element) {
		return bossDirStop, false
	}
	if pveRouteCenter(position) && (*route)[0].cell != position.Pixels().Cell() {
		*route = (*route)[1:]
	}
	return (*route)[0].direction, true
}
