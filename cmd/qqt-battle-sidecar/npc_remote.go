package main

import (
	"qqtang/internal/game/battleengine"
)

// 005D1348 stores 005C450F's player-table value in Actor+3B0 even for
// rule1/2/8 and PVE NPCs. Owner physics uses 005C452B's distinct NPC table.
func nativeNPCRemoteScalar(rate byte) float32 {
	rate = min(rate, byte(battleengine.MaxNativeSpeedRate))
	milli, _ := battleengine.NativeSpeedScalarMilli(rate)
	return float32(milli) / 1000
}

// nativeNPCRemoteHeld is 005D13C2's no-next-record path. Phase is the
// controller's retained +34 field: -1 approach, 0 cross-axis, 1 final leg.
type nativeNPCRemoteHeld struct {
	position        battleengine.NativeNPCPosition
	corner, end     battleengine.NativeNPCPosition
	direction, rate byte
	walking         bool
	phase           int32
}

func (s *nativeNPCRemoteHeld) frame(dt uint32) {
	if !s.walking {
		return
	}
	travel := float32(float32(nativeNPCRemoteScalar(s.rate)*float32(dt)) * float32(.04))
	primary, cross := &s.position.X, &s.position.Y
	corner, cornerCross := s.corner.X, s.corner.Y
	end, endCross := s.end.X, s.end.Y
	if s.direction&1 != 0 {
		primary, cross = cross, primary
		corner, cornerCross = cornerCross, corner
		end, endCross = endCross, end
	}
	sign := float32(1)
	if s.direction == 1 || s.direction == 2 {
		sign = -1
	}
	final := func() {
		*primary += sign * travel
		if sign*(*primary-end) >= 0 {
			*primary = end
		}
	}
	correct := func() {
		if endCross < cornerCross {
			*cross -= travel
			if int32(*cross) > int32(endCross) {
				return
			}
		} else {
			*cross += travel
			if int32(*cross) < int32(endCross) {
				return
			}
		}
		*cross, s.phase = endCross, 1
	}
	if s.corner.X == 0 && s.corner.Y == 0 {
		if s.phase == 0 || s.phase == 1 {
			s.phase, *cross = -1, endCross
		} else {
			final()
		}
		return
	}
	distance := abs32(int32(*primary) - int32(corner))
	switch s.phase {
	case -1:
		if distance < 2 {
			s.phase, *primary, *cross = 0, corner, cornerCross
			correct()
		} else if sign*float32(int32(*primary)-int32(corner)) < 0 {
			*primary += sign * travel
			if sign*(*primary-corner) >= -1 {
				*primary, *cross, s.phase = corner, cornerCross, 0
			}
		} else {
			s.phase, *cross = 1, endCross
		}
	case 0:
		if distance < 2 {
			correct()
		} else {
			s.phase, *cross = 1, endCross
		}
	default:
		if distance < 2 || sign*float32(int32(*primary)-int32(corner)) >= 0 {
			final()
		} else {
			s.phase, *cross = -1, cornerCross
		}
	}
}

// npcRemotePlayback connects the native queue to the host actor-frame clock.
// Receiving a record only queues it; consuming it happens after the picture frame.
type npcRemotePlayback struct {
	initialized bool
	frameSet    bool
	time        uint32
	queue       nativeNPCRemoteQueue
}

func (p *npcRemotePlayback) initialize(position battleengine.NativeNPCPosition, now uint32) {
	p.initialized, p.time = true, now
	p.queue.held = nativeNPCRemoteHeld{position: position, direction: 3}
}

func (p *npcRemotePlayback) frame(now uint32) bool {
	if now < p.time || p.frameSet && now == p.time {
		return false
	}
	consumed := p.queue.frame(now - p.time)
	p.time, p.frameSet = now, true
	return consumed
}
