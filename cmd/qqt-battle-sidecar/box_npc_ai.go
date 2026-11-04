package main

import "qqtang/internal/game/battleengine"

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
