package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBossGeneralCommandOrderMatchesNativeRegistrationAndDispatch(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "rule_dispatch_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	type event struct {
		Entry string
		Frame int
		Phase string
	}
	checked := 0
	for scanner.Scan() {
		var row struct {
			Rule, Role, Frames int
			Dt                 uint32
			Events             []event
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Rule != 1 {
			continue
		}
		command := npcMoveCommand{}
		actual := make([]event, 0)
		for frame := 0; frame < row.Frames; frame++ {
			command.generalFrame(61000+uint32(frame)*row.Dt, 1, 1, func() byte {
				actual = append(actual, event{"direction", frame, "command"})
				return 3
			}, func(byte) {}, func() { actual = append(actual, event{"bubble", frame, "command"}) })
		}
		expected := make([]event, 0)
		for _, e := range row.Events {
			if e.Phase == "command" {
				expected = append(expected, e)
			}
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("role%d frames%d: command order differs from native", row.Role, row.Frames)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 6 {
		t.Fatalf("native traces%d", checked)
	}
}

func TestBossBubbleKindAndPlannerPreserveSceneCommandCounter(t *testing.T) {
	for _, row := range []struct {
		kind, marker byte
		placed       bool
	}{{3, 4, false}, {4, 0, true}} {
		m := bossMatch(t, 1)
		b := m.boss.actors[30001]
		ai := b.ai.(*bossGeneral)
		ai.path = []pathNode{{cell: b.pos.Cell(), kind: row.kind, padding: row.marker}}
		m.boss.moveCommand.counter = 4
		ai.bombTick(m.bossWorld(m.engine.ElapsedMS()), b)
		if (len(m.engine.Bombs()) > 0) != row.placed || m.boss.moveCommand.counter != 4 {
			t.Fatalf("kind%d pad%d: bombs%d counter%d", row.kind, row.marker, len(m.engine.Bombs()), m.boss.moveCommand.counter)
		}
	}
}
