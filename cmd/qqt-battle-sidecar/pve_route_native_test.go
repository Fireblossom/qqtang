package main

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

type pveNativePose struct {
	Position      [2]float32
	Facing        byte
	WalkFlag      byte `json:"walk_flag"`
	MovementMode  int  `json:"movement_mode"`
	HeldWalk      int  `json:"held_walk"`
	AttributeRate byte `json:"attribute_rate"`
}

func pveMotionMatch(t *testing.T, blocked [][2]int16) *onlineMatch {
	t.Helper()
	grid := battleengine.Grid{Width: 15, Height: 13, Cells: make([]battleengine.Tile, 195)}
	for _, cell := range blocked {
		grid.Cells[int(cell[0])*15+int(cell[1])] = battleengine.Tile{Kind: battleengine.CellSolid, MapElementOccupied: true}
	}
	rules := onlineRules(1, 1)
	rules.NativeAdventure, rules.ExternalConclusion = true, true
	rules.ActorHalfSizePixels = 19
	rules.RoundDurationMS, rules.BombFuseMS, rules.FlameDurationMS = 600000, 3000, 500
	engine, err := battleengine.New(battleengine.Config{Grid: grid, Rules: rules, Participants: []battleengine.Participant{{PlayerID: 1, TeamID: 1, Source: battleengine.ParticipantHuman, Spawn: battleengine.Cell{Row: 1, Col: 1}, SpeedPixelsPerSecond: 80, BombCapacity: 1, BombPower: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return &onlineMatch{engine: engine, local: 1, arbitrator: 1, log: io.Discard, adventure: &adventureRule{npcs: map[uint16]*advNPC{}}}
}

func TestInitializedPVERouteAndOwnerMotionMatchNative(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "pve_route_motion_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	rows, routes, motions, poses, commands, gates, frames := 0, 0, 0, 0, 0, 0, 0
	for scanner.Scan() {
		var row struct {
			Kind, Method, Condition string
			Witness                 struct {
				Result          byte
				ActorVtable     string `json:"actor_vtable"`
				RemoteCtorCalls int    `json:"remote_ctor_calls"`
			}
			Mode, Rate, Tolerance, Direction byte
			RuleVtable                       string `json:"rule_vtable"`
			Commands                         []struct {
				Category int
				Execute  string
			}
			Position, Start          [2]float32
			Before, After            [][3]byte
			Goal                     [2]int16
			TargetCell               [2]int16 `json:"target_cell"`
			TargetPresent            bool     `json:"target_present"`
			Battle, Center, Fallback bool
			Bombs, Elements          [][2]int16
			Blocked                  json.RawMessage
			Result                   int
			CanMove                  int `json:"can_move"`
			CanSkill                 int `json:"can_skill"`
			Events                   []string
			Queries                  [][4]int32
			Dt                       uint32
			Samples                  []struct {
				Position    [2]float32
				Moved       int
				Corner, End [2]int32
			}
			SecondInitialized int `json:"second_initialized"`
			Steps             []struct {
				ID            uint16
				Direction     byte
				Before, After int
				Decisions     int
				WalkFlag      byte `json:"walk_flag"`
				Facing        byte
			}
		}
		// before/after have different schemas in the prepare snapshot.
		var tag struct{ Kind string }
		if err := json.Unmarshal(scanner.Bytes(), &tag); err != nil {
			t.Fatal(err)
		}
		if tag.Kind == "prepare_stop" {
			var prepare struct {
				Before, Stopped, After pveNativePose
				Dt                     uint32
			}
			if err := json.Unmarshal(scanner.Bytes(), &prepare); err != nil {
				t.Fatal(err)
			}
			testPVEPrepareHeldPose(t, prepare.Before, prepare.Stopped, prepare.After, prepare.Dt)
			rows++
			poses++
			continue
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		switch row.Kind {
		case "init":
			moveCommands := 0
			for _, c := range row.Commands {
				if c.Execute == "0x6112af" && c.Category == 1 {
					moveCommands++
				}
			}
			if row.Witness.Result != 1 || row.Witness.ActorVtable != "0x7a1f54" || row.Witness.RemoteCtorCalls != 1 || row.Mode != 1 || row.Rate != 5 || row.Tolerance != 6 || row.RuleVtable != "0x7a429c" || len(row.Commands) != 23 || moveCommands != 1 {
				t.Fatalf("invalid PVE initialization/registration: %+v", row)
			}
		case "route":
			var blocked [][2]int16
			if err := json.Unmarshal(row.Blocked, &blocked); err != nil {
				t.Fatal(err)
			}
			route := make(pveRoute, 0, len(row.Before))
			for _, node := range row.Before {
				route = append(route, pveRouteNode{cell: battleengine.Cell{Col: int16(node[0]), Row: int16(node[1])}, direction: node[2]})
			}
			position := battleengine.NativeNPCPosition{X: row.Position[0], Y: row.Position[1]}
			if pveRouteCenter(position) != row.Center {
				t.Fatalf("PVE centre at%v", position)
			}
			has := func(cells [][2]int16, cell battleengine.Cell) bool {
				for _, c := range cells {
					if c == ([2]int16{cell.Col, cell.Row}) {
						return true
					}
				}
				return false
			}
			queries := make([][4]int32, 0)
			bomb := func(cell battleengine.Cell) bool {
				queries = append(queries, [4]int32{0, int32(cell.Col), int32(cell.Row), 0})
				return has(row.Bombs, cell)
			}
			element := func(node pveRouteNode) bool {
				queries = append(queries, [4]int32{1, int32(node.cell.Col), int32(node.cell.Row), 0})
				if !has(row.Elements, node.cell) {
					return false
				}
				queries = append(queries, [4]int32{2, int32(node.cell.Col), int32(node.cell.Row), int32(node.direction)})
				return has(blocked, node.cell)
			}
			targetMatches := !row.Battle || row.TargetPresent && row.Goal == row.TargetCell
			result, fallback := 0, false
			switch row.Method {
			case "find":
				if route.findCurrent(position.Pixels().Cell()) {
					result = 1
				}
			case "valid":
				if route.valid(position.Pixels().Cell(), targetMatches, bomb, element) {
					result = 1
				}
			case "strategy3":
				direction, reused := route.reuse(position, targetMatches, bomb, element)
				result, fallback = int(direction), !reused
				if !reused {
					route = nil
				} // native fallback finder is controlled to fail
			}
			after := make([][3]byte, 0, len(route))
			for _, node := range route {
				after = append(after, [3]byte{byte(node.cell.Col), byte(node.cell.Row), node.direction})
			}
			if result != row.Result || fallback != row.Fallback || !reflect.DeepEqual(after, row.After) || !reflect.DeepEqual(queries, row.Queries) {
				t.Fatalf("%s %s pos%v: result%d fallback%v after%v queries%v; native%d/%v %v %v", row.Method, row.Condition, position, result, fallback, after, queries, row.Result, row.Fallback, row.After, row.Queries)
			}
			routes++
		case "motion":
			var blocked [][2]int16
			if err := json.Unmarshal(row.Blocked, &blocked); err != nil {
				t.Fatal(err)
			}
			m := pveMotionMatch(t, blocked)
			if err := m.engine.RegisterRuleNPCOwner(30300); err != nil {
				t.Fatal(err)
			}
			n := &boxNPCActor{id: 30300, speed: row.Rate, motion: battleengine.NativeNPCPosition{X: row.Start[0], Y: row.Start[1]}, motionKnown: true}
			n.pos = n.motion.Pixels()
			n.navigator.walking, n.navigator.direction = true, row.Direction
			for i, sample := range row.Samples {
				before := n.motion
				m.moveDefaultNPC(n, row.Dt)
				if math.Float32bits(n.motion.X) != math.Float32bits(sample.Position[0]) || math.Float32bits(n.motion.Y) != math.Float32bits(sample.Position[1]) || n.moveCorner != (battleengine.Position{X: sample.Corner[0], Y: sample.Corner[1]}) || n.moveEnd != (battleengine.Position{X: sample.End[0], Y: sample.End[1]}) || (n.motion != before) != (sample.Moved != 0) {
					t.Fatalf("PVE motion dir%d start%v walls%v frame%d: %v corner%v end%v native%+v", row.Direction, row.Start, blocked, i, n.motion, n.moveCorner, n.moveEnd, sample)
				}
				frames++
			}
			motions++
		case "shared_command":
			if row.SecondInitialized != 1 {
				t.Fatal("second native PVE NPC did not initialize")
			}
			m := pveMotionMatch(t, nil)
			m.adventure.moveCommand.counter = 5
			for _, id := range []uint16{30300, 30301} {
				if err := m.engine.RegisterRuleNPCOwner(id); err != nil {
					t.Fatal(err)
				}
				n := &advNPC{strategy: 3}
				n.id, n.pos, n.speed, n.lastTick = id, battleengine.Position{X: 300, Y: 260}, 5, 61000
				n.navigator.walking, n.navigator.direction, n.moving = true, 0, true
				n.route = pveRouteFromCells([]battleengine.Cell{{Row: 6, Col: 7}, {Row: 6, Col: 8}})
				m.adventure.npcs[id] = n
			}
			for _, step := range row.Steps {
				n := m.adventure.npcs[step.ID]
				n.moveLocks = 0
				if step.Direction == bossDirStop {
					n.moveLocks = 1
				}
				if int(m.adventure.moveCommand.counter) != step.Before {
					t.Fatal("PVE command entry counter differs")
				}
				m.adventureNPCTick(n, 61000)
				if int(m.adventure.moveCommand.counter) != step.After || n.moving != (step.WalkFlag == 2) || n.facing != step.Facing {
					t.Fatalf("PVE shared command NPC%d: counter%d moving%v face%d native%+v", n.id, m.adventure.moveCommand.counter, n.moving, n.facing, step)
				}
			}
			commands++
		case "move_gate":
			if row.CanMove != 0 || row.CanSkill != 1 || row.Result != 9 || !reflect.DeepEqual(row.Events, []string{"battle", "skill"}) {
				t.Fatal("native PVE move/skill gate order differs")
			}
			m, n := adventureSkillMatch(t, mapdata.AdventureProp{ID: 21001, PrepareTime: 500}, "1")
			m.adventure.moveCommand.counter = 6
			n.moveLocks = 1
			now := m.engine.ElapsedMS()
			m.adventureNPCTick(n, now)
			if len(m.adventure.casts) != 1 || n.preparingUntil != now+500 || n.moving {
				t.Fatal("movement lock incorrectly suppressed a ready PVE skill")
			}
			gates++
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != 1036 || routes != 960 || motions != 72 || frames != 1152 || poses != 1 || commands != 1 || gates != 1 {
		t.Fatalf("native rows%d routes%d motions%d frames%d poses%d commands%d gates%d", rows, routes, motions, frames, poses, commands, gates)
	}
}

func testPVEPrepareHeldPose(t *testing.T, before, stopped, after pveNativePose, dt uint32) {
	t.Helper()
	m, n := adventureSkillMatch(t, mapdata.AdventureProp{ID: 21001, PrepareTime: 500}, "1")
	n.pos, n.lastTick, n.speed = battleengine.Position{X: 300, Y: 260}, 3000, 5
	m.receiveNPCMove(&n.boxNPCActor, game.PlayerMoveSequence{Sequence: 1, TimeStamp: 1000, CurrentPosX: 300, CurrentPosY: 260, EndPosX: 580, EndPosY: 260, WalkAndDirection: 0x10, Speed: 2})
	check := func(want pveNativePose) {
		mode := 2
		if n.moving {
			mode = 3
		}
		if n.motion != (battleengine.NativeNPCPosition{X: want.Position[0], Y: want.Position[1]}) || n.facing != want.Facing || n.moving != (want.WalkFlag == 2) || mode != want.MovementMode || n.remote.queue.held.walking != (want.HeldWalk != 0) || n.speed != want.AttributeRate {
			t.Fatalf("prepare pose: motion%v moving%v face%d held%v native%+v", n.motion, n.moving, n.facing, n.remote.queue.held.walking, want)
		}
	}
	m.interpolateBoxNPC(&n.boxNPCActor, 3000)
	m.interpolateBoxNPC(&n.boxNPCActor, 3000+dt)
	check(before)
	m.startAdventureCast(n, game.WorldUseItemEvent{PlayerID: n.id, ClientTime: 3000 + dt, ItemID: 21001, PosX: 301, PosY: 260, Flag2: 3})
	check(stopped)
	m.interpolateBoxNPC(&n.boxNPCActor, 3000+2*dt)
	check(after)
}

func TestAdventurePrepareClearsSamePixelSubpixelAndCommand(t *testing.T) {
	m, n := adventureSkillMatch(t, mapdata.AdventureProp{ID: 21001, PrepareTime: 500}, "1")
	n.pos = battleengine.Position{X: 300, Y: 260}
	n.motion, n.motionKnown = battleengine.NativeNPCPosition{X: 300.75, Y: 260}, true
	n.moving, n.navigator.walking = true, true
	m.startAdventureCast(n, game.WorldUseItemEvent{PlayerID: n.id, ClientTime: 3000, ItemID: 21001, PosX: 300, PosY: 260, Flag2: 3})
	if n.motion != (battleengine.NativeNPCPosition{X: 300, Y: 260}) || n.moving || n.navigator.walking || n.facing != 3 {
		t.Fatalf("prepare retained a subpixel/command: motion%v moving%v navigator%+v face%d", n.motion, n.moving, n.navigator, n.facing)
	}
}

func TestAdventureGoalNodeStopsAtCellEntryWithoutNewSearch(t *testing.T) {
	m := pveMotionMatch(t, nil)
	if err := m.engine.RegisterRuleNPCOwner(30300); err != nil {
		t.Fatal(err)
	}
	n := &advNPC{strategy: 3}
	n.id, n.speed, n.lastTick = 30300, 5, 61000
	n.motion, n.motionKnown = battleengine.NativeNPCPosition{X: 320.25, Y: 260}, true
	n.pos = n.motion.Pixels()
	n.moving, n.navigator.walking, n.navigator.direction = true, true, 0
	n.route = pveRouteFromCells([]battleengine.Cell{{Row: 6, Col: 7}, {Row: 6, Col: 8}})
	n.routeGoal = battleengine.Cell{Row: 6, Col: 8}
	m.adventure.moveCommand.counter = 6
	// rng is deliberately absent: consuming the cached dir9 goal must not
	// begin another random search in this same command.
	m.adventureNPCTick(n, 61000)
	if n.moving || n.navigator.walking || len(n.route) != 1 || n.route[0].direction != 9 || n.motion.X != 320.25 || m.adventure.moveCommand.counter != 6 {
		t.Fatalf("PVE goal did not preserve its stop node: moving%v route%v motion%v counter%d", n.moving, n.route, n.motion, m.adventure.moveCommand.counter)
	}
}

func TestBossAndPVEWireRateLeavesAttributeSpeedUnchanged(t *testing.T) {
	boss := bossMatch(t, 2)
	adventure, npc := adventureSkillMatch(t, mapdata.AdventureProp{ID: 21001}, "1")
	adventure.local = 2
	for _, row := range []struct {
		match *onlineMatch
		npc   *boxNPCActor
		pve   bool
	}{{boss, &boss.boss.actors[30001].boxNPCActor, false}, {adventure, &npc.boxNPCActor, true}} {
		m, n := row.match, row.npc
		base := n.speed
		n.lastTick = m.engine.ElapsedMS()
		body, err := (game.PlayerMove{PlayerID: n.id, Entries: []game.PlayerMoveEntry{{PlayerID: n.id, Move: game.PlayerMoveSequence{Sequence: 1, TimeStamp: 61000, CurrentPosX: uint16(n.pos.X), CurrentPosY: uint16(n.pos.Y), EndPosX: 580, EndPosY: uint16(n.pos.Y), WalkAndDirection: 0x10, Speed: 2}}}}).MarshalNetworkBinary()
		if err != nil {
			t.Fatal(err)
		}
		if row.pve {
			m.adventureRemote(n.id, game.GameEvent{Schema: game.PlayerMoveSchema, Body: body})
		} else {
			m.bossRemote(n.id, game.GameEvent{Schema: game.PlayerMoveSchema, Body: body})
		}
		m.interpolateBoxNPC(n, m.engine.ElapsedMS())
		if n.speed != base || n.remote.queue.held.rate != 2 || n.remote.queue.scalar != nativeNPCRemoteScalar(2) {
			t.Fatalf("NPC%d wire rate overwrote attributes: speed%d base%d held%d scalar%v", n.id, n.speed, base, n.remote.queue.held.rate, n.remote.queue.scalar)
		}
	}
}
