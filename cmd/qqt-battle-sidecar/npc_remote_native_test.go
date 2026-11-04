package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

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
