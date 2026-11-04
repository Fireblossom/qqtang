package main

import "qqtang/internal/game/battleengine"

type pveSearchNode struct {
	parent battleengine.Cell
	g, f   int
	closed bool
}

// 005F10D3 uses a map keyed by (col<<8)|row. 005F1383 scans it in
// ascending order and replaces on equal f, so the largest key wins ties.
// The goal being open is enough to finish, before expanding the next node.
func pveFindRoute(from, goal battleengine.Cell, passable func(battleengine.Cell, byte, bool) bool, onExpand func(battleengine.Cell)) pveRoute {
	nodes := map[battleengine.Cell]*pveSearchNode{}
	step := func(cell battleengine.Cell, direction byte) battleengine.Cell {
		next := bossStep(cell, int(direction))
		return battleengine.Cell{Col: int16(byte(next.Col)), Row: int16(byte(next.Row))}
	}
	canStep := func(cell battleengine.Cell, direction byte) bool {
		next := step(cell, direction)
		if !passable(cell, direction, false) || !passable(next, (direction+2)%4, true) {
			return false
		}
		return nodes[next] == nil || !nodes[next].closed
	}
	// 005F0B70 deliberately evaluates all four directions, in this order.
	hasNeighbor := false
	for _, direction := range [...]byte{2, 0, 1, 3} {
		if canStep(goal, direction) {
			hasNeighbor = true
		}
	}
	if !hasNeighbor {
		return nil
	}
	heuristic := func(cell battleengine.Cell) int {
		return int(max(abs32(int32(cell.Col-goal.Col)), abs32(int32(cell.Row-goal.Row))))
	}
	key := func(cell battleengine.Cell) int { return int(cell.Col)<<8 | int(cell.Row) }
	nodes[from] = &pveSearchNode{parent: from, g: 1, f: 1 + heuristic(from)}
	for expansion := 0; expansion < 500; expansion++ {
		var current battleengine.Cell
		var best *pveSearchNode
		for cell, node := range nodes {
			if node.closed {
				continue
			}
			if best == nil || node.f < best.f || node.f == best.f && key(cell) > key(current) {
				current, best = cell, node
			}
		}
		if best == nil {
			return nil
		}
		if target := nodes[goal]; target != nil && !target.closed {
			var cells []battleengine.Cell
			for cell := goal; ; cell = nodes[cell].parent {
				cells = append(cells, cell)
				if cell == from {
					break
				}
			}
			for i, j := 0, len(cells)-1; i < j; i, j = i+1, j-1 {
				cells[i], cells[j] = cells[j], cells[i]
			}
			return pveRouteFromCells(cells)
		}
		for _, direction := range [...]byte{1, 3, 2, 0} {
			if !canStep(current, direction) {
				continue
			}
			next := step(current, direction)
			g := best.g + 1
			f := g + heuristic(next)
			if old := nodes[next]; old == nil || f < old.f {
				nodes[next] = &pveSearchNode{parent: current, g: g, f: f}
			}
		}
		best.closed = true
		if onExpand != nil {
			onExpand(current)
		}
	}
	return nil
}

// 005F0FC1: inclusive NPC polygon, two element predicates, then optional
// bubble occupancy. The source uses the outgoing direction without bit 0x80;
// the destination uses its reverse. Actors never enter this obstacle query.
func (match *onlineMatch) adventurePathPassable(n *advNPC, cell battleengine.Cell, direction byte, checkBombs bool, bombs map[battleengine.Cell]bool) bool {
	if !n.boundsContain(cell) {
		return false
	}
	for range 2 {
		tile, ok := match.engine.TileAt(cell)
		if !ok {
			return false
		}
		if tile.MapElementOccupied || tile.Kind != battleengine.CellOpen {
			d := [...]battleengine.Direction{battleengine.DirectionRight, battleengine.DirectionUp, battleengine.DirectionLeft, battleengine.DirectionDown}[direction]
			if !tile.PlayerPassable(d) {
				return false
			}
		}
	}
	return !checkBombs || !bombs[cell]
}
