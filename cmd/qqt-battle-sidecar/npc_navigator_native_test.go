package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qqtang/internal/game/battleengine"
)

func TestNPCNavigatorMatchesNativeControllerAndTimeGate(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_follow_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	controllers, gates := 0, 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row struct {
			Kind, Case    string
			Before        [][5]int
			Entries       [][5]int
			Position      [2]float32
			Counter       uint32
			Allowed       bool
			Standing      bool
			Facing        byte
			Level         int
			Dt            uint32
			Result        byte
			Now, Last     uint32
			AfterLast     uint32     `json:"after_last"`
			AfterCounter  uint32     `json:"after_counter"`
			AfterPosition [2]float32 `json:"after_position"`
			Queries       [][6]int32
			AI            struct {
				Cache                 [2]int16
				CachedDirection       byte `json:"cached_direction"`
				Blocked, Gate, Danger int
				Mask, Random          uint32
			}
			AfterCache           [2]int16 `json:"after_cache"`
			AfterCachedDirection byte     `json:"after_cached_direction"`
			RandomCalls          int      `json:"random_calls"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		switch row.Kind {
		case "gate":
			navigator := npcNavigator{lastThink: row.Last}
			pos := npcFollowPosition{220, 260}
			calls := 0
			navigator.choose(npcNavigationFrame{position: &pos, now: row.Now, mask: 15,
				random: func() uint32 { calls++; return 0 }})
			if calls != int(row.Result) || navigator.lastThink != row.AfterLast {
				t.Fatalf("gate now%d last%d: calls%d timestamp%d; native result%d timestamp%d", row.Now, row.Last, calls, navigator.lastThink, row.Result, row.AfterLast)
			}
			gates++
		case "wander_follow":
			t.Run(row.Case, func(t *testing.T) {
				navigator := npcNavigator{path: npcPathFollower{nodes: nativePathNodes(row.Before), mismatches: row.Counter},
					wander: npcWanderCache{cell: battleengine.Cell{Row: row.AI.Cache[0], Col: row.AI.Cache[1]}, direction: row.AI.CachedDirection, valid: true}}
				pos := npcFollowPosition{row.Position[0], row.Position[1]}
				calls := 0
				queries := make([][6]int32, 0)
				now := uint32(499)
				if row.AI.Gate != 0 {
					now = 500
				}
				direction := navigator.choose(npcNavigationFrame{position: &pos, facing: row.Facing, standing: row.Standing,
					blocked: row.AI.Blocked != 0, danger: row.AI.Danger != 0, mask: row.AI.Mask,
					now: now, dt: row.Dt, speed: row.Level, flee: func() byte { return 3 },
					random: func() uint32 { calls++; return row.AI.Random },
					canPass: func(x, y int32, facing byte) bool {
						queries = append(queries, [6]int32{x, y, int32(facing), 0, 0, 1})
						return row.Allowed
					}})
				if direction != row.Result || calls != row.RandomCalls || navigator.path.mismatches != row.AfterCounter || pos != (npcFollowPosition{row.AfterPosition[0], row.AfterPosition[1]}) || !reflect.DeepEqual(queries, row.Queries) || navigator.wander.cell != (battleengine.Cell{Row: row.AfterCache[0], Col: row.AfterCache[1]}) || navigator.wander.direction != row.AfterCachedDirection {
					t.Fatalf("direction%d rng%d pos%v path%+v cache%+v queries%v; native%+v", direction, calls, pos, navigator.path, navigator.wander, queries, row)
				}
				assertNativeNPCPath(t, navigator.path.nodes, row.Entries)
			})
			controllers++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if controllers != 11 || gates != 9 {
		t.Fatalf("native cases controllers%d gates%d", controllers, gates)
	}
}

func TestNPCFleeFallbackMatchesNativeInvertedBranches(t *testing.T) {
	checked := 0
	for _, row := range npcNativeCases(t) {
		if row.Kind != "flee_fallback" {
			continue
		}
		var checks []byte
		var points [][2]int32
		direction := npcFleeFallback(npcFollowPosition{row.Position[0], row.Position[1]}, func(x, y int32, direction byte) bool {
			checks = append(checks, direction)
			points = append(points, [2]int32{x, y})
			return row.Mask&(1<<direction) != 0
		})
		if direction != row.Direction || !reflect.DeepEqual(checks, row.Checks) || !reflect.DeepEqual(points, row.Points) {
			t.Fatalf("mask%d direction%d checks%v; native%d %v", row.Mask, direction, checks, row.Direction, row.Checks)
		}
		checked++
	}
	if checked != 32 {
		t.Fatalf("fallback cases%d", checked)
	}
}

func TestNPCBubbleEligibilityMatchesNativeKindByteAndLazyLineCheck(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_bubble_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	checked := 0
	for scanner.Scan() {
		var row struct {
			Present      bool
			Kind, Marker byte
			Near         bool
			LineCalls    int `json:"line_calls"`
			Searches     int
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		var path []pathNode
		if row.Present {
			path = []pathNode{{kind: row.Kind, padding: row.Marker}}
		}
		calls := 0
		eligible := npcBubbleEligible(path, func() bool { calls++; return row.Near })
		if eligible != (row.Searches == 1) || calls != row.LineCalls {
			t.Fatalf("bubble %+v eligible%v lineCalls%d", row, eligible, calls)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 30 {
		t.Fatalf("bubble cases%d", checked)
	}
}
