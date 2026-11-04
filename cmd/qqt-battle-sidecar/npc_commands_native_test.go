package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNPCMovementCommandMatchesNative(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_commands_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row struct {
			Rule          byte
			Case          string
			Frame         int
			Elapsed       uint32
			ActorMode     byte `json:"actor_mode"`
			MovementMode  byte `json:"movement_mode"`
			Direction     byte
			Before, After byte
			Decisions     int
			Actions       [][]any
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		command := npcMoveCommand{counter: int8(row.Before)}
		calls := 0
		direction, emitted := command.tick(row.Rule, row.Elapsed, row.ActorMode, row.MovementMode, func() byte {
			calls++
			return row.Direction
		})
		actions := make([][]any, 0)
		if emitted {
			if direction == 9 {
				actions = append(actions, []any{"stop"})
			} else {
				actions = append(actions, []any{"move", float64(direction)})
			}
		}
		if byte(command.counter) != row.After || calls != row.Decisions || !reflect.DeepEqual(actions, row.Actions) {
			t.Fatalf("rule%d %s frame%d counter%d calls%d actions%v; native counter%d calls%d actions%v", row.Rule, row.Case, row.Frame, byte(command.counter), calls, actions, row.After, row.Decisions, row.Actions)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 416 {
		t.Fatalf("native command cases%d", count)
	}
}
