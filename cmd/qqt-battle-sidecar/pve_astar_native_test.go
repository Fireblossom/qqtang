package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

func TestPVEAStarMatchesCompleteNativeSearch(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "pve_astar_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 2<<20)
	rows, searches, cutoff := 0, 0, 0
	for scanner.Scan() {
		var row struct {
			Kind, Name   string
			FinderVtable string `json:"finder_vtable"`
			Bounds       [4]int
			Start, Goal  [2]int16
			Elements     [][3]int16
			Bombs        [][2]int16
			Result       int
			Route        [][3]byte
			Expanded     [][2]int16
			Passages     [][4]int16
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows++
		if row.Kind == "init" {
			if row.FinderVtable != "0x7a374c" {
				t.Fatalf("wrong original finder: %+v", row)
			}
			continue
		}
		t.Run(row.Name+"-"+strconv.Itoa(searches), func(t *testing.T) {
			cell := func(pair [2]int16) battleengine.Cell { return battleengine.Cell{Col: pair[0], Row: pair[1]} }
			width, height := row.Bounds[2]+1, row.Bounds[3]+1
			grid := battleengine.Grid{Width: uint16(width), Height: uint16(height)}
			grid.Cells = make([]battleengine.Tile, grid.Width*grid.Height)
			for _, element := range row.Elements {
				mask := uint32(element[2])
				// Probe masks use (1<<direction); engine metadata stores
				// right/up/left/down in bits 8/4/2/1 respectively.
				attr := mask&1<<3 | mask&2<<1 | mask&4>>1 | mask&8>>3
				kind := battleengine.CellOpen
				if mask == 0 {
					kind = battleengine.CellSolid
				}
				grid.Cells[int(element[1])*width+int(element[0])] = battleengine.Tile{
					Kind: kind, MapElementOccupied: true, NativeGridAttrSet: true, NativeGridAttr: attr,
				}
			}
			var spawn battleengine.Cell
			for index, tile := range grid.Cells {
				if tile.Kind == battleengine.CellOpen && !tile.MapElementOccupied {
					spawn = battleengine.Cell{Col: int16(index % width), Row: int16(index / width)}
					break
				}
			}
			rules := onlineRules(1, 1)
			rules.NativeAdventure, rules.ExternalConclusion = true, true
			rules.ActorHalfSizePixels = 19
			rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS = 600000, 3000, 500
			engine, err := battleengine.New(battleengine.Config{Grid: grid, Rules: rules, Participants: []battleengine.Participant{{
				PlayerID: 1, TeamID: 1, Source: battleengine.ParticipantHuman, Spawn: spawn,
				SpeedPixelsPerSecond: 80, BombCapacity: 1, BombPower: 1,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			bombs := map[battleengine.Cell]bool{}
			for _, pair := range row.Bombs {
				bombs[cell(pair)] = true
				if _, err := engine.ApplyVerifiedBombPlacement(1, cell(pair), 1, 0); err != nil {
					t.Fatal(err)
				}
			}
			m := &onlineMatch{engine: engine, log: io.Discard}
			n := &advNPC{bounds: mapdata.AdventureNPCBounds{BeginCol: row.Bounds[0], BeginRow: row.Bounds[1], EndCol: row.Bounds[2], EndRow: row.Bounds[3]}}
			expanded, passages := make([][2]int16, 0), make([][4]int16, 0)
			path := pveFindRoute(cell(row.Start), cell(row.Goal), func(c battleengine.Cell, direction byte, check bool) bool {
				checkByte := int16(0)
				if check {
					checkByte = 1
				}
				passages = append(passages, [4]int16{c.Col, c.Row, int16(direction), checkByte})
				return m.adventurePathPassable(n, c, direction, check, bombs)
			}, func(c battleengine.Cell) { expanded = append(expanded, [2]int16{c.Col, c.Row}) })
			actual := make([][3]byte, 0, len(path))
			for _, node := range path {
				actual = append(actual, [3]byte{byte(node.cell.Col), byte(node.cell.Row), node.direction})
			}
			if (len(path) != 0) != (row.Result != 0) || !reflect.DeepEqual(actual, row.Route) {
				t.Fatalf("route mismatch: got %v native %v result%d", actual, row.Route, row.Result)
			}
			if !reflect.DeepEqual(expanded, row.Expanded) {
				t.Fatalf("expansion order differs: got%v native%v", expanded, row.Expanded)
			}
			if !reflect.DeepEqual(passages, row.Passages) {
				t.Fatalf("passage queries differ: got%v native%v", passages, row.Passages)
			}
			if got := m.adventureFindPath(n, cell(row.Start), cell(row.Goal)); !reflect.DeepEqual(got, path) {
				t.Fatalf("production search differs: %v vs %v", got, path)
			}
			wantGoal := cell(row.Goal)
			if row.Result == 0 {
				wantGoal = battleengine.Cell{Col: -1, Row: -1}
			}
			if n.routeGoal != wantGoal {
				t.Fatalf("stored target %v want %v", n.routeGoal, wantGoal)
			}
		})
		if len(row.Expanded) == 500 && row.Result == 0 {
			cutoff++
		}
		searches++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 106 || searches != 105 || cutoff != 5 {
		t.Fatalf("incomplete original samples: rows%d searches%d cutoffs%d", rows, searches, cutoff)
	}
}
