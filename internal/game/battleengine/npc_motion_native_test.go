package battleengine

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestNPCFloatMotionMatchesNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "client", "native", "tests", "fixtures", "npc_motion_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	checked, failures := 0, 0
	for scanner.Scan() {
		var row struct {
			Kind      string
			Position  [2]float32
			Direction int
			Dt        uint32
			Speed     float32
			Blocked   [][2]int16
			Result    []float64
			Moved     int
			Results   [][2]float32
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		blocked := map[Cell]bool{}
		for _, cell := range row.Blocked {
			blocked[Cell{cell[0], cell[1]}] = true
		}
		can := func(p Position, d Direction) bool {
			points, ok := nativeLeadingEdgePoints(p, 19, d)
			if !ok {
				return false
			}
			for _, point := range points {
				if point.X < 0 || point.Y < 0 || point.X >= 600 || point.Y >= 520 || blocked[point.Cell()] {
					return false
				}
			}
			return true
		}
		position := NativeNPCPosition{row.Position[0], row.Position[1]}
		direction := [4]Direction{DirectionRight, DirectionUp, DirectionLeft, DirectionDown}[row.Direction]
		step := func(p NativeNPCPosition, dt uint32) NativeNPCPosition {
			return nativeNPCDisplacement(p, direction, row.Speed, dt, 15, 13, can, func(Cell, Direction) bool { return true }, 10)
		}
		if row.Kind == "sequence" {
			for frame, want := range row.Results {
				position = step(position, row.Dt)
				if position != (NativeNPCPosition{want[0], want[1]}) {
					t.Errorf("line%d frame%d sequence got%v want%v", checked+1, frame, position, want)
					failures++
					break
				}
			}
		} else if row.Kind == "move" {
			got := step(position, row.Dt)
			want := NativeNPCPosition{float32(row.Result[0]), float32(row.Result[1])}
			if math.Float32bits(got.X) != math.Float32bits(want.X) || math.Float32bits(got.Y) != math.Float32bits(want.Y) || (got != position) != (row.Moved != 0) {
				if failures < 12 {
					t.Errorf("line%d move %+v d%d dt%d walls%v got%v want%v", checked+1, position, row.Direction, row.Dt, row.Blocked, got, want)
				}
				failures++
			}
		} else {
			got := nativeNPCForecast(position, direction, func(p NativeNPCPosition) NativeNPCPosition { return step(p, 25) }, 1121)
			wantCorner, wantEnd := Position{int32(row.Result[0]), int32(row.Result[1])}, Position{int32(row.Result[2]), int32(row.Result[3])}
			if got.End != wantEnd || got.Corner != wantCorner {
				if failures < 12 {
					t.Errorf("line%d forecast %+v d%d walls%v got%+v want corner%v end%v", checked+1, position, row.Direction, row.Blocked, got, wantCorner, wantEnd)
				}
				failures++
			}
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("%d / %d native cases failed", failures, checked)
	}
	if checked != 3648 {
		t.Fatalf("native cases%d", checked)
	}
}
