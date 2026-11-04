package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

func TestNPCPublisherMatchesNativeSharedHistoryAndWorldLoop(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_publish_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	cases, frames := 0, 0
	for scanner.Scan() {
		var row struct {
			Case            string
			InitialSequence uint32 `json:"initial_sequence"`
			Frames          []struct {
				Now, Dt, Sequence, Counter uint32
				History                    int
				Actors                     []struct {
					ID               uint16
					Position         [2]float32
					Walking, Force   bool
					Direction, Speed byte
					Corner, End      [2]int32
				}
				Packet []struct {
					ID   uint16
					Move [10]uint32
				}
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		m := &onlineMatch{npcMovement: nativeNPCMovePublisher{sequence: row.InitialSequence}}
		for index, frame := range row.Frames {
			var out []boxNPCOutbound
			for _, a := range frame.Actors {
				n := &boxNPCActor{id: a.ID, pos: battleengine.Position{X: int32(a.Position[0]), Y: int32(a.Position[1])}, moving: a.Walking, facing: a.Direction, speed: a.Speed,
					moveEndValid: true, moveEnd: battleengine.Position{X: a.End[0], Y: a.End[1]}, moveCorner: battleengine.Position{X: a.Corner[0], Y: a.Corner[1]}}
				m.publishNPCMove(&out, n, frame.Now, a.Force)
			}
			if len(out) != len(frame.Packet) || m.npcMovement.sequence != frame.Sequence || len(m.npcMovement.history) != frame.History || frame.Counter != 0 {
				t.Fatalf("%s frame%d: packets%d sequence%d history%d native%+v", row.Case, index, len(out), m.npcMovement.sequence, len(m.npcMovement.history), frame)
			}
			for i, expected := range frame.Packet {
				actual, err := game.ParsePlayerMove(out[i].message.Data)
				if err != nil {
					t.Fatal(err)
				}
				if len(actual.Entries) != 1 || out[i].author != expected.ID || actual.Entries[0].PlayerID != expected.ID {
					t.Fatal("wrong NPC movement author")
				}
				v := actual.Entries[0].Move
				packed := [10]uint32{uint32(v.Sequence), v.TimeStamp, uint32(v.CurrentPosX), uint32(v.CurrentPosY), uint32(v.CornerPosX), uint32(v.CornerPosY), uint32(v.EndPosX), uint32(v.EndPosY), uint32(v.WalkAndDirection), uint32(v.Speed)}
				if packed != expected.Move {
					t.Fatalf("%s frame%d NPC%d: %v native%v", row.Case, index, expected.ID, packed, expected.Move)
				}
			}
			frames++
		}
		cases++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if cases != 32 || frames != 1215 {
		t.Fatalf("native cases%d frames%d", cases, frames)
	}
}
