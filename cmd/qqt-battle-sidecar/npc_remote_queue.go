package main

import (
	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

type npcRemoteRecord struct {
	move   game.PlayerMoveSequence
	forced bool
}

func (r *npcRemoteRecord) sequence() int32 { return int32(int16(r.move.Sequence)) }
func (r *npcRemoteRecord) position() battleengine.NativeNPCPosition {
	return battleengine.NativeNPCPosition{X: float32(int16(r.move.CurrentPosX)), Y: float32(int16(r.move.CurrentPosY))}
}

// 005D0945/005D09D6: queued movement records and native correction modes.
// Timestamp is a receive-order guard; it is not a future execution deadline.
type nativeNPCRemoteQueue struct {
	held                nativeNPCRemoteHeld
	queue               []*npcRemoteRecord
	lastReceive, clock  uint32
	sequence            int32
	scalar              float32
	correction          uint32
	correctionDirection byte
	correctionTarget    battleengine.NativeNPCPosition
	correctionScalar    float32
}

func (q *nativeNPCRemoteQueue) receive(m game.PlayerMoveSequence) bool {
	if m.TimeStamp <= q.lastReceive {
		return false
	}
	q.lastReceive = m.TimeStamp
	r := &npcRemoteRecord{move: m, forced: m.WalkAndDirection&0x20 != 0}
	if r.sequence() == q.sequence {
		q.held.corner, q.held.end = npcRemotePoints(m)
	}
	if len(q.queue) > 0 && q.queue[len(q.queue)-1].sequence() == r.sequence() {
		*q.queue[len(q.queue)-1] = *r
	} else {
		q.queue = append(q.queue, r)
	}
	return true
}

func npcRemotePoints(m game.PlayerMoveSequence) (battleengine.NativeNPCPosition, battleengine.NativeNPCPosition) {
	return battleengine.NativeNPCPosition{X: float32(int16(m.CornerPosX)), Y: float32(int16(m.CornerPosY))},
		battleengine.NativeNPCPosition{X: float32(int16(m.EndPosX)), Y: float32(int16(m.EndPosY))}
}

func (q *nativeNPCRemoteQueue) consume(r *npcRemoteRecord) {
	m := r.move
	q.held.direction, q.held.rate, q.held.walking = m.WalkAndDirection&15, m.Speed, m.WalkAndDirection&0x10 != 0
	q.held.corner, q.held.end = npcRemotePoints(m)
	q.held.phase = -1
	if q.held.walking {
		q.scalar = nativeNPCRemoteScalar(m.Speed)
	}
	q.sequence, q.clock = r.sequence(), m.TimeStamp
	for i, p := range q.queue {
		if p == r {
			q.queue = q.queue[i:]
			break
		}
	}
}

func (q *nativeNPCRemoteQueue) jump(r *npcRemoteRecord) bool {
	q.consume(r)
	q.held.position, q.correction = r.position(), 0
	return true
}

func (q *nativeNPCRemoteQueue) heldFrame(dt uint32) {
	q.held.frame(dt)
	if q.held.walking {
		q.clock += dt
	}
}

func (q *nativeNPCRemoteQueue) frame(dt uint32) bool {
	if len(q.queue) > 0 {
		latest := q.queue[len(q.queue)-1]
		if uint32(latest.sequence()-q.sequence) > 4 || latest.sequence() == q.sequence && q.held.walking && q.forwardGap(latest) >= 80 {
			return q.jump(latest)
		}
		for _, r := range q.queue {
			if r.forced {
				r.forced = false
				return q.jump(r)
			}
		}
		for _, r := range q.queue {
			if r.sequence() == q.sequence {
				continue
			}
			if q.sequence+1 != r.sequence() {
				return q.jump(r)
			}
			if r.move.WalkAndDirection&0x10 == 0 {
				return q.stopRecord(r, dt)
			}
			return q.walkRecord(r, dt)
		}
	}
	if q.correction != 0 {
		q.correctFrame(dt)
	} else {
		q.heldFrame(dt)
	}
	return false
}

func (q *nativeNPCRemoteQueue) forwardGap(r *npcRemoteRecord) float32 {
	p, t := q.held.position, r.position()
	switch q.held.direction {
	case 0:
		return t.X - p.X
	case 1:
		return p.Y - t.Y
	case 2:
		return p.X - t.X
	case 3:
		return t.Y - p.Y
	}
	return 0
}

func (q *nativeNPCRemoteQueue) compare(r *npcRemoteRecord, tolerance int32) (int32, int32, int32) {
	p, t := q.held.position, r.position()
	dx, dy := int32(t.X-p.X), int32(t.Y-p.Y)
	if !q.held.walking {
		if abs32(int32(p.X)-int32(t.X)) <= tolerance && abs32(int32(p.Y)-int32(t.Y)) <= tolerance {
			return 0, dx, dy
		}
		return -1, dx, dy
	}
	var delta int32
	switch q.held.direction {
	case 0:
		delta = int32(t.X) - int32(p.X)
	case 1:
		delta = int32(p.Y) - int32(t.Y)
	case 2:
		delta = int32(p.X) - int32(t.X)
	case 3:
		delta = int32(t.Y) - int32(p.Y)
	}
	if abs32(delta) <= tolerance {
		return 0, dx, dy
	}
	if delta > 0 {
		return -1, dx, dy
	}
	return 1, dx, dy
}

func (q *nativeNPCRemoteQueue) reachPrimary(r *npcRemoteRecord, dt uint32) bool {
	q.heldFrame(dt)
	tolerance := int32(1)
	if r.move.WalkAndDirection&0x10 != 0 {
		tolerance = 8
	}
	p, t := &q.held.position, r.position()
	if q.held.direction&1 == 0 {
		if abs32(int32(p.X-t.X)) > tolerance {
			return false
		}
		p.X = t.X
	} else {
		if abs32(int32(p.Y-t.Y)) > tolerance {
			return false
		}
		p.Y = t.Y
	}
	return true
}

func (q *nativeNPCRemoteQueue) beginCorrection(r *npcRemoteRecord, mode uint32, direction byte, dt uint32) bool {
	q.correction, q.correctionDirection, q.correctionTarget = mode, direction, r.position()
	q.correctionScalar = nativeNPCRemoteScalar(r.move.Speed)
	q.consume(r)
	q.correctFrame(dt)
	return true
}

func (q *nativeNPCRemoteQueue) walkRecord(r *npcRemoteRecord, dt uint32) bool {
	previous := q.correction
	q.correction = 0
	if !q.held.walking {
		if previous != 0 {
			p, t, dir := q.held.position, r.position(), r.move.WalkAndDirection&15
			if dir&1 == 0 {
				if abs32(int32(p.Y)-int32(t.Y)) > 8 {
					correctionDir := byte(1)
					if p.Y < t.Y {
						correctionDir = 3
					}
					return q.beginCorrection(r, 2, correctionDir, dt)
				}
			} else if abs32(int32(p.X)-int32(t.X)) > 8 {
				// 005D0C7E always selects +X in this vertical-walk branch.
				return q.beginCorrection(r, 2, 0, dt)
			}
		}
	} else if previous == 0 {
		comparison, dx, dy := q.compare(r, 8)
		if abs32(dx) < 40 || abs32(dy) < 40 {
			if comparison == -1 {
				q.reachPrimary(r, dt)
			} else if comparison == 1 {
				return q.beginCorrection(r, 2, q.held.direction^2, dt)
			}
		}
	}
	return q.jump(r)
}

func (q *nativeNPCRemoteQueue) stopRecord(r *npcRemoteRecord, dt uint32) bool {
	if !q.held.walking {
		return q.jump(r)
	}
	comparison, dx, dy := q.compare(r, 1)
	if comparison == -1 && !q.reachPrimary(r, dt) {
		return false
	}
	if comparison == 1 {
		delta := dx
		if q.held.direction&1 != 0 {
			delta = dy
		}
		if abs32(delta) <= 40 {
			return q.beginCorrection(r, 1, q.held.direction^2, dt)
		}
	}
	return q.jump(r)
}

func (q *nativeNPCRemoteQueue) correctFrame(dt uint32) {
	travel := float32(float32(dt) * float32(.016))
	if q.correction == 2 {
		travel = float32(float32(float32(dt)*q.correctionScalar) * float32(.04))
	}
	p, t := &q.held.position, q.correctionTarget
	done := false
	switch q.correctionDirection {
	case 0:
		p.X += travel
		done = p.X >= t.X
	case 1:
		p.Y -= travel
		done = p.Y <= t.Y
	case 2:
		p.X -= travel
		done = p.X <= t.X
	case 3:
		p.Y += travel
		done = p.Y >= t.Y
	}
	if done {
		*p, q.correction = t, 0
	}
}
