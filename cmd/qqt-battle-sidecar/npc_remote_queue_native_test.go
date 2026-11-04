package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func TestNPCRemoteReceiveQueueAndCorrectionMatchNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_remote_queue_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	cases, events := 0, 0
	for scanner.Scan() {
		var row struct {
			Case   string
			Start  [2]float32
			Events []struct {
				Receive *struct {
					Sequence             uint16
					TimeStamp            uint32 `json:"time_stamp"`
					Current, Corner, End [2]uint16
					Flags, Speed         byte
				}
				Dt       uint32
				Consumed bool
				State    struct {
					Position        [2]float32
					Count           int
					LastReceive     uint32 `json:"last_receive"`
					Walking         bool
					Direction, Rate byte
					Scalar          float32
					Phase           int32
					Time            uint32
					Sequence        int32
					Correction      uint32
				}
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		q := nativeNPCRemoteQueue{held: nativeNPCRemoteHeld{position: battleengine.NativeNPCPosition{X: row.Start[0], Y: row.Start[1]}, direction: 3}}
		actor := &boxNPCActor{pos: battleengine.Position{X: int32(row.Start[0]), Y: int32(row.Start[1])}, facing: 3}
		match := &onlineMatch{}
		var now uint32
		for i, op := range row.Events {
			consumed := false
			if m := op.Receive; m != nil {
				movement := game.PlayerMoveSequence{Sequence: m.Sequence, TimeStamp: m.TimeStamp, CurrentPosX: m.Current[0], CurrentPosY: m.Current[1], CornerPosX: m.Corner[0], CornerPosY: m.Corner[1], EndPosX: m.End[0], EndPosY: m.End[1], WalkAndDirection: m.Flags, Speed: m.Speed}
				q.receive(movement)
				match.receiveNPCMove(actor, movement)
			} else {
				consumed = q.frame(op.Dt)
				now += op.Dt
				match.interpolateBoxNPC(actor, now)
				match.interpolateBoxNPC(actor, now)
			}
			s := op.State
			if math.Float32bits(q.held.position.X) != math.Float32bits(s.Position[0]) || math.Float32bits(q.held.position.Y) != math.Float32bits(s.Position[1]) ||
				len(q.queue) != s.Count || q.lastReceive != s.LastReceive || q.held.walking != s.Walking || q.held.direction != s.Direction || q.held.rate != s.Rate ||
				math.Float32bits(q.scalar) != math.Float32bits(s.Scalar) || q.held.phase != s.Phase || q.clock != s.Time || q.sequence != s.Sequence || q.correction != s.Correction || consumed != op.Consumed {
				t.Fatalf("%s event%d: pos%v count%d last%d walk%v dir%d rate%d scalar%v phase%d time%d seq%d corr%d consumed%v; native%+v consumed%v", row.Case, i, q.held.position, len(q.queue), q.lastReceive, q.held.walking, q.held.direction, q.held.rate, q.scalar, q.held.phase, q.clock, q.sequence, q.correction, consumed, s, op.Consumed)
			}
			a := &actor.remote.queue
			if a.held != q.held || len(a.queue) != len(q.queue) || a.lastReceive != q.lastReceive || a.clock != q.clock || a.sequence != q.sequence || a.scalar != q.scalar || a.correction != q.correction {
				t.Fatalf("host adapter %s event%d: %+v native queue%+v", row.Case, i, a, &q)
			}
			events++
		}
		cases++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if cases != 458 || events != 15594 {
		t.Fatalf("native cases%d events%d", cases, events)
	}
}

func TestNPCRemoteReadsSharedActorPositionAfterOwnershipMovement(t *testing.T) {
	m := &onlineMatch{}
	n := &boxNPCActor{pos: battleengine.Position{X: 300, Y: 260}}
	m.receiveNPCMove(n, game.PlayerMoveSequence{Sequence: 1, TimeStamp: 1000, CurrentPosX: 300, CurrentPosY: 260, EndPosX: 580, EndPosY: 260, WalkAndDirection: 0x10, Speed: 2})
	m.interpolateBoxNPC(n, 0)
	m.interpolateBoxNPC(n, 20)
	// Local float movement can change the fractional coordinate while the
	// retained receiver still points at this same actor.
	n.motion = battleengine.NativeNPCPosition{X: 500.25, Y: 260}
	n.pos = n.motion.Pixels()
	m.interpolateBoxNPC(n, 40)
	if n.motion.X != float32(float32(500.25)+float32(1.44)) {
		t.Fatalf("receiver used a stale private position: %v", n.motion)
	}
	// Legacy owner movement changes integer pixels without updating motion.
	n.pos.X = 530
	m.interpolateBoxNPC(n, 60)
	if n.motion.X != float32(float32(530)+float32(1.44)) {
		t.Fatalf("receiver ignored the updated actor pixels: %v", n.motion)
	}
}
