package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleengine"
)

func TestDefaultBossMovementAndForecastMatchNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "boss_motion_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	rows, frames := 0, 0
	for scanner.Scan() {
		var row struct {
			Kind, Vtable, Getter       string
			Tolerance, Rate, Direction byte
			Walking                    bool
			Start                      [2]float32
			Blocked                    [][2]int16
			Dt                         uint32
			Samples                    []struct {
				Position    [2]float32
				Moved       int
				Corner, End [2]int32
			}
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row.Kind == "rule" {
			if row.Vtable != "0x7a37f0" || row.Getter != "0x5f49b5" || row.Tolerance != 6 {
				t.Fatalf("invalid rule witness %+v", row)
			}
			rows++
			continue
		}
		grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 195)}
		for _, cell := range row.Blocked {
			grid.Cells[int(cell[0])*15+int(cell[1])] = battleengine.Tile{Kind: battleengine.CellSolid, MapElementOccupied: true}
		}
		rules := onlineRules(1, 1)
		rules.NativeBossNPC, rules.ExternalConclusion = true, true
		rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS = 240000, 3000, 500
		rules.ActorHalfSizePixels = 19
		engine, err := battleengine.New(battleengine.Config{Grid: grid, Rules: rules, Participants: []battleengine.Participant{{PlayerID: 1, TeamID: 1, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: 1}, SpeedPixelsPerSecond: 80, BombCapacity: 1, BombPower: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = engine.RegisterRuleNPCOwner(30001); err != nil {
			t.Fatal(err)
		}
		m := &onlineMatch{engine: engine}
		n := &boxNPCActor{id: 30001, role: 32, speed: row.Rate, motion: battleengine.NativeNPCPosition{X: row.Start[0], Y: row.Start[1]}, motionKnown: true}
		n.pos = n.motion.Pixels()
		n.navigator.walking, n.navigator.direction = row.Walking, row.Direction
		for index, sample := range row.Samples {
			before := n.motion
			m.moveDefaultNPC(n, row.Dt)
			if math.Float32bits(n.motion.X) != math.Float32bits(sample.Position[0]) || math.Float32bits(n.motion.Y) != math.Float32bits(sample.Position[1]) || n.moveCorner != (battleengine.Position{X: sample.Corner[0], Y: sample.Corner[1]}) || n.moveEnd != (battleengine.Position{X: sample.End[0], Y: sample.End[1]}) || (n.motion != before) != (sample.Moved != 0) {
				t.Fatalf("row%d rate%d dir%d frame%d: motion%v corner%v end%v; native%+v", rows, row.Rate, row.Direction, index, n.motion, n.moveCorner, n.moveEnd, sample)
			}
			frames++
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 349 || frames != 5568 {
		t.Fatalf("native rows%d frames%d", rows, frames)
	}
}

func TestGeneralBossRetainsCommandPastCentreBetweenDecisions(t *testing.T) {
	m := bossMatch(t, 1)
	b := m.boss.actors[30001]
	b.pos = battleengine.Position{X: 259, Y: 260}
	b.motion, b.motionKnown = battleengine.NativeNPCPosition{X: 259.75, Y: 260}, true
	b.speed = 3
	b.lastTick = 3020
	b.navigator.walking, b.navigator.direction = true, 0
	b.path = []battleengine.Cell{{Row: 6, Col: 6}}
	b.ai = &bossGeneral{inited: true, path: []pathNode{{cell: b.pos.Cell(), kind: 3}}}
	for now := uint32(3040); now <= 3140; now += 20 {
		m.bossTick(now, nil)
	}
	// Six real 005CF8D9 steps from boss_motion_v848, rather than a stop at260.
	if math.Float32bits(b.motion.X) != math.Float32bits(float32(274.5196533203125)) || !b.moving || b.moveEnd != (battleengine.Position{X: 580, Y: 260}) || len(b.path) != 0 {
		t.Fatalf("held command stopped at its retired centre: motion%v moving%v end%v path%v", b.motion, b.moving, b.moveEnd, b.path)
	}
}
