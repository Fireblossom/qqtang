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
)

func TestPVERouteElementAdapterMatchesNativePredicate(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "pve_route_elements_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	rows, checks := 0, 0
	for scanner.Scan() {
		var row struct {
			Kind          string
			Predicate     string
			ElementVtable string `json:"element_vtable"`
			Direction     byte
			Nodes, After  [][3]byte
			ElementIndex  int `json:"element_index"`
			Mask          uint32
			Valid         bool
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows++
		if row.Kind == "init" {
			if row.ElementVtable != "0x7a369c" || row.Predicate != "0x5ef1aa" {
				t.Fatalf("wrong original predicate: %+v", row)
			}
			continue
		}
		t.Run(strconv.Itoa(checks), func(t *testing.T) {
			grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 195)}
			if row.ElementIndex >= 0 {
				cell := row.Nodes[row.ElementIndex]
				kind := battleengine.CellOpen
				if row.Mask == 0 {
					kind = battleengine.CellSolid
				}
				grid.Cells[int(cell[1])*15+int(cell[0])] = battleengine.Tile{
					Kind: kind, MapElementOccupied: true, NativeGridAttrSet: true, NativeGridAttr: row.Mask,
				}
			}
			rules := onlineRules(1, 1)
			rules.NativeAdventure, rules.ExternalConclusion = true, true
			rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS, rules.ActorHalfSizePixels = 600000, 3000, 500, 19
			engine, err := battleengine.New(battleengine.Config{Grid: grid, Rules: rules, Participants: []battleengine.Participant{{
				PlayerID: 1, TeamID: 1, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: 1},
				SpeedPixelsPerSecond: 80, BombCapacity: 1, BombPower: 1,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			m := &onlineMatch{engine: engine, log: io.Discard}
			n := &advNPC{}
			n.pos = battleengine.PositionAtCellCenter(battleengine.Cell{Col: 7, Row: 6})
			for _, node := range row.Nodes {
				n.route = append(n.route, pveRouteNode{cell: battleengine.Cell{Col: int16(node[0]), Row: int16(node[1])}, direction: node[2]})
			}
			direction, valid := m.adventureReuseRoute(n)
			if valid != row.Valid || valid && direction != row.Direction || !valid && direction != bossDirStop {
				t.Fatalf("element%d mask%x: reused%d/%v native%d/%v", row.ElementIndex, row.Mask, direction, valid, row.Direction, row.Valid)
			}
			after := make([][3]byte, 0, len(n.route))
			for _, node := range n.route {
				after = append(after, [3]byte{byte(node.cell.Col), byte(node.cell.Row), node.direction})
			}
			if !reflect.DeepEqual(after, row.After) {
				t.Fatalf("route changed: got%v native%v", after, row.After)
			}
		})
		checks++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 197 || checks != 196 {
		t.Fatalf("incomplete original records: rows%d checks%d", rows, checks)
	}
}
