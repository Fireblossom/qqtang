package main

import (
	"encoding/binary"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

// 005DB43B/005DB487: the effect overrides rate to 2, including a rate-1
// NPC that consequently moves faster. Zero remaining time is still active.
type npcIceState struct {
	active bool
	left   int32
}

func (ice *npcIceState) refresh() { ice.active, ice.left = true, 10000 }
func (ice *npcIceState) frame(dt uint32) {
	if ice.active {
		ice.left -= int32(dt)
		if ice.left < 0 {
			ice.active, ice.left = false, 0
		}
	}
}
func (n *boxNPCActor) effectiveNPCSpeed() byte {
	if n.ice.active {
		return 2
	}
	return n.speed
}

func (match *onlineMatch) iceBoxNPC(n *boxNPCActor, now uint32) {
	if n.dead || n.trapped {
		return
	}
	n.ice.refresh()
	body := make([]byte, 6)
	binary.BigEndian.PutUint16(body, n.id)
	// 006034B4 observes the local human then NPCs without changing the UDP
	// package author. This is an arbitrator observation, not NPC input.
	match.queueBoxNPC(match.local, game.NotifyPlayerTrapped, now, body)
	match.mirror(game.NotifyPlayerTrapped, body)
}

// 006034B4 applies the initial fire/ice burst to arbitrator-owned NPCs too.
func (match *onlineMatch) boxSpecialNPCHit(cell battleengine.Cell, style uint32, now uint32) {
	if !match.isArbitrator() {
		return
	}
	for _, n := range match.boxNPCState().ordered() {
		if n.dead || n.trapped || !boxAreaContains(cell, n.pos) {
			continue
		}
		if style == 10000 {
			match.iceBoxNPC(n, now)
		} else {
			match.trapBoxNPC(n, now)
		}
	}
}
