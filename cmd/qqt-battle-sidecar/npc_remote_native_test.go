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

func TestNPCRemoteScalarAndStraightHeldStepMatchNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_remote_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scalars, held := 0, 0
	for scanner.Scan() {
		var row struct {
			Kind                  string
			Role                  uint16
			Rate, Direction       byte
			Dt                    uint32
			Remote, Owner, Scalar float32
			Start, End            [2]uint16
			Position              [2]float32
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind == "scalar" {
			if math.Float32bits(nativeNPCRemoteScalar(row.Rate)) != math.Float32bits(row.Remote) || math.Float32bits(float32(bossSpeedTable[min(int(row.Rate), 10)])) != math.Float32bits(row.Owner) {
				t.Fatalf("rate%d: native remote%v owner%v", row.Rate, row.Remote, row.Owner)
			}
			scalars++
			continue
		}
		n := &boxNPCActor{role: row.Role, netMoveSet: true, netMove: game.PlayerMoveSequence{CurrentPosX: row.Start[0], CurrentPosY: row.Start[1], EndPosX: row.End[0], EndPosY: row.End[1], WalkAndDirection: 0x10 | row.Direction, Speed: row.Rate}}
		m := &onlineMatch{}
		m.interpolateBoxNPC(n, row.Dt)
		if math.Float32bits(n.motion.X) != math.Float32bits(row.Position[0]) || math.Float32bits(n.motion.Y) != math.Float32bits(row.Position[1]) {
			t.Fatalf("role%d rate%d dir%d dt%d: got%v native%v", row.Role, row.Rate, row.Direction, row.Dt, n.motion, row.Position)
		}
		held++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if scalars != 13 || held != 780 {
		t.Fatalf("native scalars%d held%d", scalars, held)
	}
}

func TestPVERemoteMatchesInitializedNativeController(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "pve_remote_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	initialized, held := false, 0
	for scanner.Scan() {
		var row struct {
			Kind                    string
			Result, Rate, Direction byte
			RemoteVtable            string `json:"remote_vtable"`
			ActorVtable             string `json:"actor_vtable"`
			AttributesVtable        string `json:"attributes_vtable"`
			CtorCalls               int    `json:"remote_ctor_calls"`
			OwnerMatches            bool   `json:"owner_matches"`
			WorldMatches            bool   `json:"world_matches"`
			Dt                      uint32
			Start, End              [2]uint16
			Position                [2]float32
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind == "init" {
			if row.Result != 1 || row.RemoteVtable != "0x7a32e8" || row.ActorVtable != "0x7a1f54" || row.AttributesVtable != "0x7a1b7c" || row.CtorCalls != 1 || !row.OwnerMatches || !row.WorldMatches {
				t.Fatalf("invalid native initialization witness: %+v", row)
			}
			initialized = true
			continue
		}
		if !initialized {
			t.Fatal("motion samples lack initialization witness")
		}
		m := &onlineMatch{adventure: &adventureRule{}}
		n := &boxNPCActor{netMoveSet: true, netMove: game.PlayerMoveSequence{CurrentPosX: row.Start[0], CurrentPosY: row.Start[1], EndPosX: row.End[0], EndPosY: row.End[1], WalkAndDirection: 0x10 | row.Direction, Speed: row.Rate}}
		m.interpolateBoxNPC(n, row.Dt)
		if math.Float32bits(n.motion.X) != math.Float32bits(row.Position[0]) || math.Float32bits(n.motion.Y) != math.Float32bits(row.Position[1]) {
			t.Fatalf("PVE rate%d dir%d dt%d: %v native%v", row.Rate, row.Direction, row.Dt, n.motion, row.Position)
		}
		held++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !initialized || held != 260 {
		t.Fatalf("native PVE init%v held%d", initialized, held)
	}
}

func TestNPCRemoteHeldPathsMatchNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_remote_path_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	paths, frames := 0, 0
	for scanner.Scan() {
		var row struct {
			Case               string
			Direction, Rate    byte
			Start, Corner, End [2]float32
			Phase              int32
			Walking            bool
			Frames             []struct {
				Dt       uint32
				Position [2]float32
				Phase    int32
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		pos := func(xy [2]float32) battleengine.NativeNPCPosition {
			return battleengine.NativeNPCPosition{X: xy[0], Y: xy[1]}
		}
		s := nativeNPCRemoteHeld{position: pos(row.Start), corner: pos(row.Corner), end: pos(row.End), direction: row.Direction, rate: row.Rate, walking: row.Walking, phase: row.Phase}
		// Integer starts and the normal -1 phase can also enter through the
		// production wire adapter. Fractional/retained-state probes test the
		// kernel above without inventing fractional wire coordinates.
		var actor *boxNPCActor
		if row.Phase == -1 && row.Start[0] == float32(uint16(row.Start[0])) && row.Start[1] == float32(uint16(row.Start[1])) {
			walk := row.Direction
			if row.Walking {
				walk |= 0x10
			}
			actor = &boxNPCActor{netMoveSet: true, netMove: game.PlayerMoveSequence{
				CurrentPosX: uint16(row.Start[0]), CurrentPosY: uint16(row.Start[1]),
				CornerPosX: uint16(row.Corner[0]), CornerPosY: uint16(row.Corner[1]),
				EndPosX: uint16(row.End[0]), EndPosY: uint16(row.End[1]), WalkAndDirection: walk, Speed: row.Rate,
			}}
		}
		var now uint32
		for index, frame := range row.Frames {
			s.frame(frame.Dt)
			if math.Float32bits(s.position.X) != math.Float32bits(frame.Position[0]) || math.Float32bits(s.position.Y) != math.Float32bits(frame.Position[1]) || s.phase != frame.Phase {
				t.Fatalf("%s dir%d rate%d frame%d dt%d: %v phase%d native%v phase%d", row.Case, row.Direction, row.Rate, index, frame.Dt, s.position, s.phase, frame.Position, frame.Phase)
			}
			if actor != nil {
				now += frame.Dt
				match := &onlineMatch{}
				match.interpolateBoxNPC(actor, now)
				// Repeated delivery/tick at the same time must not consume an
				// extra native phase transition or erase accumulated fractions.
				match.interpolateBoxNPC(actor, now)
				if actor.motion != s.position || actor.remote.held.phase != s.phase {
					t.Fatalf("wire %s dir%d rate%d frame%d: %v phase%d native%v phase%d", row.Case, row.Direction, row.Rate, index, actor.motion, actor.remote.held.phase, s.position, s.phase)
				}
			}
			frames++
		}
		paths++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if paths != 880 || frames != 23320 {
		t.Fatalf("native paths%d frames%d", paths, frames)
	}
}
