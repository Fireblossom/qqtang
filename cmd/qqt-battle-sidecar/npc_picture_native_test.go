package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/protocol/game"
)

func TestNPCPictureActionsAndResetClockMatchNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_picture_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	states := map[int]*npcPictureState{}
	checked := 0
	for scanner.Scan() {
		var row struct {
			Kind, Operation           string
			Role                      int
			Dt, Updates, Clock        uint32
			Direction, Action, Facing byte
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind != "event" {
			continue
		}
		p := states[row.Role]
		if row.Operation == "init" {
			p = &npcPictureState{action: 1, facing: 3}
			states[row.Role] = p
		}
		switch row.Operation {
		case "walk", "walk_expiry":
			p.set(4, row.Direction)
		case "stop":
			p.set(1, row.Direction)
		case "ice":
			p.set(7, row.Direction)
		case "birth":
			p.set(14, 3)
		}
		for i := uint32(0); i < row.Updates; i++ {
			p.frame(row.Dt)
		}
		if p.action != row.Action || p.facing != row.Facing || p.clock != row.Clock {
			t.Fatalf("role%d %s: %+v, native action%d face%d clock%d", row.Role, row.Operation, p, row.Action, row.Facing, row.Clock)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 126 || len(states) != 9 {
		t.Fatalf("native events%d roles%d", checked, len(states))
	}
}

func TestNPCIcePictureEndsOnCommandWhileSpeedEffectRemains(t *testing.T) {
	m := npcTestMatch(t, 1, 1)
	n := addTestNPC(t, m, 20001, 25)
	n.lastTick, n.navigator.lastThink = 60000, 60000
	m.iceBoxNPC(n, 60000)
	m.advanceBoxNPC(n, 60020)
	if n.picture.action != 7 || n.picture.clock != 20 || !n.ice.active {
		t.Fatal("ice did not select its independent picture")
	}
	m.box.npc.moveCommand.counter = 11
	m.advanceBoxNPC(n, 60040)
	if n.picture.action != 4 || n.picture.clock != 40 || !n.ice.active {
		t.Fatalf("move command failed to restore picture: %+v", n.picture)
	}
}

func TestNPCRemoteQueueConsumesAfterPictureWithoutTimestampDeadline(t *testing.T) {
	m := npcTestMatch(t, 2, 1)
	n := addTestNPC(t, m, 20001, 27)
	n.picture.set(7, 3)
	body, err := (game.PlayerMove{PlayerID: n.id, Entries: []game.PlayerMoveEntry{{PlayerID: n.id, Move: game.PlayerMoveSequence{Sequence: 1, TimeStamp: 40, CurrentPosX: 220, CurrentPosY: 220, EndPosX: 220, EndPosY: 400, WalkAndDirection: 0x13, Speed: 3}}}}).MarshalNetworkBinary()
	if err != nil {
		t.Fatal(err)
	}
	m.boxNPCRemote(1, game.GameEvent{Schema: game.PlayerMoveSchema, Body: body})
	if n.picture.action != 7 || n.picture.clock != 0 {
		t.Fatal("receive consumed the command before the actor frame")
	}
	m.boxNPCTick(20)
	if n.picture.action != 4 || n.picture.clock != 0 {
		t.Fatal("timestamp was incorrectly used as a frame deadline")
	}
	m.boxNPCTick(40)
	if n.picture.action != 4 || n.picture.clock != 40 {
		t.Fatalf("walking picture did not retain its clock: %+v", n.picture)
	}
	m.boxNPCTick(60)
	if n.picture.clock != 80 {
		t.Fatal("same interpolation record reset the walking clock again")
	}
	// A replay of that record is not a new picture command and must not
	// overwrite an ice pose selected after the record was consumed.
	n.picture.set(7, 3)
	m.boxNPCRemote(1, game.GameEvent{Schema: game.PlayerMoveSchema, Body: body})
	m.boxNPCTick(80)
	if n.picture.action != 7 || n.picture.clock != 20 {
		t.Fatal("duplicate record overwrote the later picture command")
	}
}
