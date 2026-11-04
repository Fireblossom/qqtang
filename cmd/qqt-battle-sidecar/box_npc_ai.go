package main

import "qqtang/internal/game/battleengine"

// npcWanderCache is the fallback branch of 005DEF29. A changed mask alone
// does not invalidate the direction: the cell or movement-blocked flag must
// change. Keep the RNG lazy so a cache hit does not consume scene randomness.
type npcWanderCache struct {
	cell      battleengine.Cell
	direction byte
	valid     bool
}

func (cache *npcWanderCache) choose(cell battleengine.Cell, blocked bool, mask uint32, facing byte, random func() uint32) byte {
	if cache.valid && cache.cell == cell && !blocked {
		return cache.direction
	}
	value := uint32(0)
	if mask&15 != 0 {
		value = random()
	}
	cache.cell, cache.direction, cache.valid = cell, npcWanderDirection(mask, facing, value), true
	return cache.direction
}

// npcWanderDirection is 005B0ACA. Its reverse-direction comparison uses
// the candidate INDEX, not choices[index]; preserve that native quirk.
func npcWanderDirection(mask uint32, facing byte, random uint32) byte {
	var choices []byte
	for direction := byte(0); direction < 4; direction++ {
		if mask&(1<<(3-direction)) != 0 {
			choices = append(choices, direction)
		}
	}
	if len(choices) == 0 {
		return 0
	}
	index := random % uint32(len(choices))
	if (index+2)%4 == uint32(facing) {
		index = (index + 1) % uint32(len(choices))
	}
	return choices[index]
}

func npcPathCells(path []pathNode) []battleengine.Cell {
	var cells []battleengine.Cell
	for index, node := range path {
		if index > 0 {
			cells = append(cells, node.cell)
		}
	}
	return cells
}
