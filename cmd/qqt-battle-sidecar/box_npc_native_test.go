package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/protocol/game"
)

type npcNativeCase struct {
	Kind      string
	Case      string
	Mask      uint32
	Facing    byte
	Random    uint32
	Direction byte
	Level     int
	Value     float64
	Role      uint16
	Queries   [][2]int16
	Blocked   [][2]int16
	Entries   [][5]int
	Before    [][5]int
	Checks    []byte
	Position  [2]float32
	Points    [][2]int32
}

func npcNativeCases(t *testing.T) []npcNativeCase {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "box_ai_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var cases []npcNativeCase
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row npcNativeCase
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestNPCWanderAndSpeedMatchNative(t *testing.T) {
	wanders, speeds := 0, 0
	for _, row := range npcNativeCases(t) {
		switch row.Kind {
		case "wander":
			wanders++
			if got := npcWanderDirection(row.Mask, row.Facing, row.Random); got != row.Direction {
				t.Fatalf("mask%d facing%d random%d: got%d native%d", row.Mask, row.Facing, row.Random, got, row.Direction)
			}
		case "speed":
			speeds++
			if math.Float32bits(float32(bossSpeedTable[row.Level])) != math.Float32bits(float32(row.Value)) {
				t.Fatalf("NPC speed level%d: %v native%v", row.Level, bossSpeedTable[row.Level], row.Value)
			}
		}
	}
	if wanders != 320 || speeds != 11 {
		t.Fatalf("native records: wander%d speed%d", wanders, speeds)
	}
}

func TestNPCRingEnumerationMatchesNative(t *testing.T) {
	checked := 0
	for _, row := range npcNativeCases(t) {
		if row.Kind != "safe_scan" && row.Kind != "item_scan" {
			continue
		}
		radius := 10
		if row.Kind == "item_scan" {
			radius = 8
		} else if row.Role == 25 {
			radius = 3
		}
		var actual [][2]int16
		ring(battleengine.Cell{Row: 6, Col: 5}, radius, func(cell battleengine.Cell) bool {
			actual = append(actual, [2]int16{cell.Row, cell.Col})
			return false
		})
		if len(actual) != len(row.Queries) {
			t.Fatalf("%s role%d count%d native%d", row.Kind, row.Role, len(actual), len(row.Queries))
		}
		for index, cell := range actual {
			if cell != row.Queries[index] {
				t.Fatalf("%s role%d query%d: %v native%v", row.Kind, row.Role, index, cell, row.Queries[index])
			}
		}
		checked++
	}
	if checked != 3 {
		t.Fatalf("native ring cases%d", checked)
	}
}

func assertNativeNPCPath(t *testing.T, path []pathNode, entries [][5]int) {
	t.Helper()
	if len(path) != len(entries) {
		t.Fatalf("path%v native%v", path, entries)
	}
	for index, node := range path {
		native := entries[len(entries)-1-index]
		if int(node.cell.Row) != native[0] || int(node.cell.Col) != native[1] || node.dir != native[2] || int(node.kind) != native[3] {
			t.Fatalf("node%d: %+v native%v", index, node, native)
		}
	}
}

func TestNPCEscapeAndFleeMatchNativeWFAndChoice(t *testing.T) {
	escapes, flees := 0, 0
	for _, row := range npcNativeCases(t) {
		if row.Kind != "escape" && row.Kind != "flee" {
			continue
		}
		t.Run(fmt.Sprintf("%s_%s_role%d_rng%d", row.Kind, row.Case, row.Role, row.Random), func(t *testing.T) {
			match := npcTestMatch(t, 1, 1)
			for _, cell := range row.Blocked {
				if err := match.engine.SetNativeBoxCell(battleengine.Cell{Row: cell[0], Col: cell[1]}, battleengine.Tile{Kind: battleengine.CellSolid, MapElementOccupied: true}); err != nil {
					t.Fatal(err)
				}
			}
			here := battleengine.Cell{Row: 6, Col: 5}
			world := match.bossWorld(0)
			var path []pathNode
			var ok bool
			if row.Kind == "escape" {
				path, ok = (&bossGeneral{}).escapePlan(world, here)
			} else {
				world.burning[here] = true // controlled native grid: only the origin is unsafe
				radius := 10
				if row.Role == 25 {
					radius = 3
				}
				calls := 0
				path, ok = world.safePlan(here, radius, func(n int) int {
					calls++
					return int(row.Random % uint32(n))
				})
				if calls != 1 {
					t.Fatalf("random calls%d", calls)
				}
			}
			if !ok {
				t.Fatal("native path succeeded but port failed")
			}
			assertNativeNPCPath(t, path, row.Entries)
		})
		if row.Kind == "escape" {
			escapes++
		} else {
			flees++
		}
	}
	if escapes != 5 || flees != 34 {
		t.Fatalf("native path cases: escape%d flee%d", escapes, flees)
	}
}

func TestBoxNPCOwnerAndReceiverUseTheirDistinctNativeSpeedTables(t *testing.T) {
	for _, row := range []struct {
		role       byte
		dx, peerDX int32
	}{{25, 40, 40}, {26, 100, 72}, {27, 123, 140}} {
		match := npcTestMatch(t, 1, 1)
		npc := addTestNPC(t, match, 20001, row.role)
		start := npc.pos
		npc.navigator.walking, npc.navigator.direction = true, 0
		for now := uint32(20); now <= 1000; now += 20 {
			match.moveBoxNPC(npc, 20)
		}
		if npc.pos.X-start.X != row.dx {
			t.Fatalf("role%d arb travel%d want%d", row.role, npc.pos.X-start.X, row.dx)
		}
		peer := npcTestMatch(t, 2, 1)
		remote := addTestNPC(t, peer, 20001, row.role)
		remote.netMoveSet = true
		remote.netMove = game.PlayerMoveSequence{CurrentPosX: uint16(start.X), CurrentPosY: uint16(start.Y), EndPosX: 420, EndPosY: uint16(start.Y), Speed: remote.speed, WalkAndDirection: 0x10}
		peer.interpolateBoxNPC(remote, 1000)
		if remote.pos.X-start.X != row.peerDX {
			t.Fatalf("role%d peer travel%d want%d", row.role, remote.pos.X-start.X, row.peerDX)
		}
	}
}
