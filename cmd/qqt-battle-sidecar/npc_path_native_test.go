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

func nativePathNodes(entries [][5]int) []pathNode {
	var nodes []pathNode
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		nodes = append(nodes, pathNode{cell: battleengine.Cell{Row: int16(entry[0]), Col: int16(entry[1])}, dir: entry[2], kind: byte(entry[3]), padding: byte(entry[4])})
	}
	return nodes
}

func TestNPCPathConsumptionMatchesNative(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "npc_follow_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	checked := 0
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
			Result        int
			AfterCounter  uint32     `json:"after_counter"`
			AfterPosition [2]float32 `json:"after_position"`
			Queries       [][6]int32
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind != "follow" {
			continue
		}
		t.Run(row.Case, func(t *testing.T) {
			path := npcPathFollower{nodes: nativePathNodes(row.Before), mismatches: row.Counter}
			pos := npcFollowPosition{row.Position[0], row.Position[1]}
			queries := make([][6]int32, 0)
			fallback := path.follow(&pos, row.Facing, row.Standing, row.Level, row.Dt, func(x, y int32, direction byte) bool {
				queries = append(queries, [6]int32{x, y, int32(direction), 0, 0, 1})
				return row.Allowed
			})
			if fallback != (row.Result == 1) || path.mismatches != row.AfterCounter || pos != (npcFollowPosition{row.AfterPosition[0], row.AfterPosition[1]}) || !reflect.DeepEqual(queries, row.Queries) {
				t.Fatalf("fallback%v counter%d pos%v queries%v; native result%d counter%d pos%v queries%v", fallback, path.mismatches, pos, queries, row.Result, row.AfterCounter, row.AfterPosition, row.Queries)
			}
			assertNativeNPCPath(t, path.nodes, row.Entries)
			// The Boss bridge must preserve the same float position, retained
			// mismatch counter, node consumption and collision query inputs.
			n := &boxNPCActor{id: 30001, role: 32, speed: byte(row.Level), facing: row.Facing,
				motion: battleengine.NativeNPCPosition{X: row.Position[0], Y: row.Position[1]}, motionKnown: true}
			n.pos = n.motion.Pixels()
			n.navigator.walking = !row.Standing
			boss := &bossGeneral{path: nativePathNodes(row.Before), pathMismatches: row.Counter}
			queries = make([][6]int32, 0)
			fallback = boss.followPath(n, row.Dt, func(x, y int32, direction byte) bool {
				queries = append(queries, [6]int32{x, y, int32(direction), 0, 0, 1})
				return row.Allowed
			})
			if fallback != (row.Result == 1) || boss.pathMismatches != row.AfterCounter || n.motion != (battleengine.NativeNPCPosition{X: row.AfterPosition[0], Y: row.AfterPosition[1]}) || !reflect.DeepEqual(queries, row.Queries) {
				t.Fatalf("Boss bridge differs: fallback%v counter%d motion%v queries%v", fallback, boss.pathMismatches, n.motion, queries)
			}
			assertNativeNPCPath(t, boss.path, row.Entries)
		})
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 47 {
		t.Fatalf("native follower cases%d", checked)
	}
}

func TestNPCEscapeAppendMatchesNativeNonemptyVector(t *testing.T) {
	for _, row := range npcNativeCases(t) {
		if row.Kind != "escape_append" {
			continue
		}
		match := npcTestMatch(t, 1, 1)
		path, ok := (&bossGeneral{}).escapePlan(match.bossWorld(0), battleengine.Cell{Row: 6, Col: 5})
		if !ok {
			t.Fatal("open native escape failed")
		}
		follower := npcPathFollower{nodes: nativePathNodes(row.Before)}
		follower.appendEscape(path)
		assertNativeNPCPath(t, follower.nodes, row.Entries)
		boss := bossGeneral{path: nativePathNodes(row.Before)}
		boss.appendEscape(path)
		assertNativeNPCPath(t, boss.path, row.Entries)
		return
	}
	t.Fatal("missing native append record")
}
