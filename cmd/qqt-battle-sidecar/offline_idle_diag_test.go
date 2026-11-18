package main

import (
	"fmt"
	"os"
	"testing"

	"qqtang/internal/game/battleengine"
)

// TestOfflineIdleDiag (QQT_OFFLINE_DIAG_IDLE=map) plays 60 s and draws the
// field around player 1: what the bot sees when it stands still.
func TestOfflineIdleDiag(t *testing.T) {
	name := os.Getenv("QQT_OFFLINE_DIAG_IDLE")
	if name == "" {
		t.Skip()
	}
	var mapID uint32
	fmt.Sscan(name, &mapID)
	root := offlineClientRoot(t)
	players := []player{{ID: 1, Role: 1, Team: 1, AI: true, ExtPoint: 3000}, {ID: 2, Role: 2, Team: 1, AI: true, ExtPoint: 3000}}
	hub, err := newOfflineHub(root, offlineInit{MapID: mapID, Players: players, SpawnSeed: 5, ItemSeed: 6, Policy: offlinePolicyBot, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.close()
	for hub.over == nil && hub.link.clock < 60_000 {
		if _, err := hub.step(command{Cmd: "step", Ticks: 10}); err != nil {
			t.Fatal(err)
		}
	}
	seat := hub.seats[1]
	seat.bot.trace = true
	act := seat.bot.act(seat.engine)
	actor, _ := actorByID(seat.engine.Actors(), 1)
	here := actor.Position.Cell()
	t.Logf("player 1 at %+v cell %+v act %+v follow line %d path %v", actor.Position, here, act, seat.bot.traceLine, seat.bot.tracePath)
	grid := seat.engine.Grid()
	npcs := map[battleengine.Cell]bool{}
	if seat.adventure != nil {
		for _, n := range seat.adventure.npcs {
			if !n.dead {
				npcs[n.pos.Cell()] = true
				t.Logf("npc %d at %+v", n.id, n.pos.Cell())
			}
		}
	}
	for row := max(0, here.Row-8); row < min(int16(grid.Height), here.Row+8); row++ {
		line := ""
		for col := int16(0); col < int16(grid.Width); col++ {
			cell := battleengine.Cell{Row: row, Col: col}
			tile, _ := grid.Cell(cell)
			switch {
			case cell == here:
				line += "@"
			case npcs[cell]:
				line += "N"
			case tile.Kind == battleengine.CellOpen:
				line += "."
			case tile.Kind == battleengine.CellBreakable:
				if tile.NormalPushable {
					line += "p"
				} else {
					line += "#"
				}
			default:
				line += "X"
			}
		}
		t.Logf("%3d %s", row, line)
	}
}
