package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func TestPVEPerceptionMatchesOriginal(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "client", "native", "tests", "fixtures", "pve_perception_v848.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	counts := map[string]int{}
	for scanner.Scan() {
		var row struct {
			Kind, Name string
			Position   [2]float32
			Target     [2]int32
			View       int
			Result     bool
			Roles      []struct {
				ID       uint16
				Mode     int
				Position [2]int32
			}
			Enmity, After         map[string]int
			Strategy, Now         uint32
			Chosen                uint16
			Before, Battle, Saved int
			NativeElapsed         int `json:"elapsed"`
			NativeRouteSize       int `json:"route_size"`
			NativeTotal           int `json:"total"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		counts[row.Kind]++
		if row.Kind == "init" {
			continue
		}
		t.Run(row.Kind+"-"+strconv.Itoa(counts[row.Kind]), func(t *testing.T) {
			n := &advNPC{bounds: mapdata.AdventureNPCBounds{EndCol: 14, EndRow: 12}, enmity: map[uint16]int{}}
			if row.Kind == "view" {
				n.view = row.View
				n.motion, n.motionKnown = battleengine.NativeNPCPosition{X: row.Position[0], Y: row.Position[1]}, true
				n.pos = n.motion.Pixels()
				if got := n.inView(battleengine.Position{X: row.Target[0], Y: row.Target[1]}); got != row.Result {
					t.Fatalf("view%d from%v target%v got%v native%v", row.View, row.Position, row.Target, got, row.Result)
				}
				return
			}
			for key, value := range row.Enmity {
				seat, err := strconv.Atoi(key)
				if err != nil {
					t.Fatal(err)
				}
				n.enmity[uint16(seat+1)] = value
			}
			var players []battleengine.Actor
			for _, role := range row.Roles {
				if role.ID >= 30300 || role.Mode == 7 || role.Mode == 12 {
					continue
				}
				players = append(players, battleengine.Actor{Participant: battleengine.Participant{PlayerID: role.ID}, Position: battleengine.Position{X: role.Position[0], Y: role.Position[1]}})
			}
			switch row.Kind {
			case "target":
				if got := adventureSelectTarget(n, players, row.Strategy, row.Now); got != row.Chosen {
					t.Fatalf("strategy%d clock%d target%d native%d", row.Strategy, row.Now, got, row.Chosen)
				}
			case "battle":
				n.battle, n.strategy, n.savedStrategy, n.battleSince = row.Before != 0, 3, 5, 4000
				if n.battle {
					n.strategy = 4
				}
				n.route = pveRouteFromCells([]battleengine.Cell{{Col: 7, Row: 6}, {Col: 8, Row: 6}})
				updateAdventureBattle(n, players, 5000)
				elapsed := 0
				if n.battleSince != 0 {
					elapsed = int(5000 - n.battleSince)
				}
				if n.battle != (row.Battle != 0) || n.strategy != int(row.Strategy) || n.savedStrategy != row.Saved || elapsed != row.NativeElapsed || len(n.route) != row.NativeRouteSize {
					t.Fatalf("battle transition differs: %+v native%+v", n, row)
				}
				total := 0
				for _, player := range players {
					if n.boundsContain(player.Position.Cell()) {
						total += n.enmity[player.PlayerID]
					}
				}
				if total != row.NativeTotal {
					t.Fatalf("total enmity%d native%d", total, row.NativeTotal)
				}
			}
			after := map[string]int{}
			for id, value := range n.enmity {
				after[strconv.Itoa(int(id)-1)] = value
			}
			if !reflect.DeepEqual(after, row.After) {
				t.Fatalf("enmity tree changed differently: got%v native%v", after, row.After)
			}
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counts, map[string]int{"init": 1, "view": 432, "target": 528, "battle": 24}) {
		t.Fatalf("incomplete original cases: %v", counts)
	}
}

func TestPVEViewAndRandomTargetUseProductionEntrypoints(t *testing.T) {
	m := pveMotionMatch(t, nil)
	m.adventure.players = map[uint16]*advPlayer{1: {hp: 100, maxHP: 100}}
	n := &advNPC{view: 0, bounds: mapdata.AdventureNPCBounds{EndCol: 14, EndRow: 12}, enmity: map[uint16]int{1: 0}, info: game.PVENPC{BattleStrategy: 2}}
	n.id = 30300
	n.motion, n.motionKnown = battleengine.NativeNPCPosition{X: 319.75, Y: 260}, true
	n.pos = n.motion.Pixels()
	m.adventure.npcs[n.id] = n
	if err := m.engine.ApplyVerifiedMovementCheckpoint(1, battleengine.Position{X: 359, Y: 260}); err != nil {
		t.Fatal(err)
	}
	m.adventureEnmity()
	if n.enmity[1] != 20 {
		t.Fatalf("view0 did not acquire target in next cell: %v", n.enmity)
	}
	if direction := m.adventureChase(n, n.pos.Cell(), m.livingAdventurePlayers(), 20); n.target != 1 || direction != 0 || len(n.route) != 2 {
		t.Fatalf("strategy2 did not chase its sole candidate: target%d direction%d route%v", n.target, direction, n.route)
	}
}
