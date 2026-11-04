package main

import "qqtang/internal/game/battleengine"

// npcPathFollower stores start..goal, the reverse of N+760's native vector.
// New escape vectors are appended to the native end, hence prepended here.
type npcPathFollower struct {
	nodes      []pathNode
	mismatches uint32
}

type npcFollowPosition struct{ x, y float32 }

func (path *npcPathFollower) appendEscape(nodes []pathNode) {
	path.nodes = append(append([]pathNode(nil), nodes...), path.nodes...)
}

var npcLookahead = [...]float32{0, 1, 2, 2, 2, 2, 2, 2, 2, 8, 13.333}

// follow is 005B1EA6. True means use the controller's fallback this time,
// even when a centre-node removal leaves more nodes in the vector.
func (path *npcPathFollower) follow(pos *npcFollowPosition, facing byte, standing bool, speed int, dt uint32, canPass func(int32, int32, byte) bool) bool {
	if len(path.nodes) < 2 {
		path.nodes, path.mismatches = nil, 0
		return true
	}
	here := battleengine.Cell{Row: int16(int32(pos.y) / 40), Col: int16(int32(pos.x) / 40)}
	centre := npcFollowPosition{float32(here.Col)*40 + 20, float32(here.Row)*40 + 20}
	for len(path.nodes) > 0 && path.nodes[0].cell != here {
		path.mismatches++
		if path.mismatches > 10 {
			path.nodes, path.mismatches, *pos = nil, 0, centre
			return true
		}
		path.nodes = path.nodes[1:]
	}
	if len(path.nodes) > 1 && abs32(int32(pos.x-centre.x)) < 10 && abs32(int32(pos.y-centre.y)) < 10 {
		path.mismatches = 0
		path.nodes = path.nodes[1:]
		return true
	}
	if len(path.nodes) > 0 {
		speed = min(max(speed, 0), 10)
		distance := float32(npcLookahead[speed] * float32(dt))
		distance = float32(distance * float32(0.04))
		direction := path.nodes[0].dir
		var dx, dy float32
		if direction == 9 {
			// Native 7F29F8[9] reads into the following pointer table.
			// This is observable after mismatch removes all but the goal;
			// movement command 9 still means stop, never walk 127 cells.
			dx = 127
		} else {
			dx, dy = float32(bossDelta[direction][0]), float32(bossDelta[direction][1])
		}
		x := int32(float32(pos.x + float32(distance*dx)))
		y := int32(float32(pos.y + float32(distance*dy)))
		if canPass(x, y, facing) || standing {
			return false
		}
		path.nodes, path.mismatches, *pos = nil, 0, centre
	}
	return true
}
