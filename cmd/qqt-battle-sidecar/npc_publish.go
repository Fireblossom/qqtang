package main

import "qqtang/internal/protocol/game"

// 005F3D0A shares one 005F33FF history across the NPC iteration. It does
// not call the player's 005F3791 heartbeat accumulator. 005F3695 emits
// only the latest history entry for each NPC at that point in the loop.
type nativeNPCMovePublisher struct {
	sequence uint32
	history  []game.PlayerMoveSequence
}

func (p *nativeNPCMovePublisher) update(m game.PlayerMoveSequence, forcePosition bool) (game.PlayerMoveSequence, bool) {
	m.WalkAndDirection &= 0x1f
	changed := len(p.history) == 0
	if !changed {
		last := &p.history[len(p.history)-1]
		changed = last.WalkAndDirection != m.WalkAndDirection || last.Speed != m.Speed
		if !changed && m.WalkAndDirection&0x10 == 0 {
			changed = last.CurrentPosX != m.CurrentPosX || last.CurrentPosY != m.CurrentPosY
		}
	}
	dirty := changed
	if changed {
		p.sequence++
		m.Sequence = uint16(p.sequence)
		p.history = append(p.history, m)
		if len(p.history) > 8 {
			p.history = p.history[len(p.history)-8:]
		}
	} else {
		last := &p.history[len(p.history)-1]
		if m.WalkAndDirection&0x10 != 0 {
			endChanged := last.EndPosX != m.EndPosX || last.EndPosY != m.EndPosY
			cornerChanged := last.CornerPosX != m.CornerPosX || last.CornerPosY != m.CornerPosY
			if endChanged || cornerChanged && m.CornerPosX != 0 && m.CornerPosY != 0 {
				last.CornerPosX, last.CornerPosY, last.EndPosX, last.EndPosY = m.CornerPosX, m.CornerPosY, m.EndPosX, m.EndPosY
				dirty = true
			}
		}
		last.CurrentPosX, last.CurrentPosY, last.TimeStamp = m.CurrentPosX, m.CurrentPosY, m.TimeStamp
	}
	result := p.history[len(p.history)-1]
	if forcePosition {
		result.WalkAndDirection |= 0x20
	}
	return result, dirty || forcePosition
}
