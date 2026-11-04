package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNPCMoveSchedulingMatchesCompleteNativeRuleDispatch(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "rule_dispatch_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	checked := 0
	for scanner.Scan() {
		var row struct {
			Rule             byte
			Role, Frames, Dt int
			Events           []struct {
				Entry, Phase, Caller string
				Frame                int
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		var native, actual []int
		bubbles := 0
		for _, event := range row.Events {
			if event.Phase != "command" {
				t.Fatalf("unexpected default actor AI dispatch: %+v", event)
			}
			switch event.Entry {
			case "direction":
				if event.Caller != "0x6113a3" {
					t.Fatalf("direction caller %s", event.Caller)
				}
				native = append(native, event.Frame)
			case "bubble":
				if row.Rule != 1 || event.Caller != "0x610c2c" {
					t.Fatalf("unexpected bubble: %+v", event)
				}
				bubbles++
			default:
				t.Fatalf("unexpected AI entry: %+v", event)
			}
		}
		if row.Rule == 1 && bubbles != row.Frames {
			t.Fatal("normal-rule positive control lost its per-frame bubble dispatch")
		}
		command := npcMoveCommand{}
		for frame := 0; frame < row.Frames; frame++ {
			if _, emitted := command.tick(row.Rule, uint32(61000+frame*row.Dt), 1, 1, func() byte { return 3 }); emitted {
				actual = append(actual, frame)
			}
		}
		if !reflect.DeepEqual(actual, native) {
			t.Fatalf("rule%d role%d frames%d: move frames %v; native %v", row.Rule, row.Role, row.Frames, actual, native)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 12 {
		t.Fatalf("native dispatch cases %d", checked)
	}
}
