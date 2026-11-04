package main

import "qqtang/internal/game/battleengine"

type npcNavigator struct {
	path      npcPathFollower
	wander    npcWanderCache
	lastThink uint32
	direction byte
	walking   bool
}

func newNPCNavigator(cell battleengine.Cell, facing byte, now uint32) npcNavigator {
	// 005AFDFA sets +77C to scene time and primes the cell/direction cache.
	return npcNavigator{lastThink: now, direction: facing,
		wander: npcWanderCache{cell: cell, direction: facing, valid: true}}
}

type npcNavigationFrame struct {
	position *npcFollowPosition
	facing   byte
	standing bool
	blocked  bool
	danger   bool
	mask     uint32
	now, dt  uint32
	speed    int
	random   func() uint32
	flee     func() byte
	canPass  func(int32, int32, byte) bool
}

// 005B214D intentionally inverts the last two collision tests.
func npcFleeFallback(position npcFollowPosition, canPass func(int32, int32, byte) bool) byte {
	can := func(direction byte) bool {
		x := int32(float32(position.x + 40*float32(bossDelta[direction][0])))
		y := int32(float32(position.y + 40*float32(bossDelta[direction][1])))
		return canPass(x, y, direction)
	}
	switch {
	case can(0):
		return 0
	case can(1):
		return 1
	case !can(2):
		return 2
	case can(3):
		return 9
	default:
		return 3
	}
}

func npcBubbleEligible(path []pathNode, playerInLine func() bool) bool {
	// The native flag is byte4 (kind), not byte5 (the node's pad/marker).
	return len(path) == 0 || path[0].kind == 4 || playerInLine()
}

// choose is 005DEF29. The frame command, safe-time gate and physics remain
// separate layers: danger bypasses the safe gate, not the frame command.
func (navigator *npcNavigator) choose(frame npcNavigationFrame) byte {
	if frame.danger {
		return frame.flee()
	}
	if abs32(int32(frame.now-navigator.lastThink)) <= 499 {
		return frame.facing
	}
	navigator.lastThink = frame.now
	if !navigator.path.follow(frame.position, frame.facing, frame.standing, frame.speed, frame.dt, frame.canPass) {
		return byte(navigator.path.nodes[0].dir)
	}
	here := battleengine.Cell{Row: int16(int32(frame.position.y) / 40), Col: int16(int32(frame.position.x) / 40)}
	return navigator.wander.choose(here, frame.blocked, frame.mask, frame.facing, frame.random)
}
