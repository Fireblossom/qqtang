package main

import (
	"os"
	"testing"

	"qqtang/internal/game/battleengine"
)

// TestOfflineDitherDiag (QQT_OFFLINE_DIAG_DITHER=map) looks for a bot seat
// oscillating between two positions while it keeps moving.
func TestOfflineDitherDiag(t *testing.T) {
	if os.Getenv("QQT_OFFLINE_DIAG_DITHER") == "" {
		t.Skip()
	}
	root := offlineClientRoot(t)
	hub, err := newOfflineHub(root, offlineInit{MapID: 1701, Players: offlineTeams(2, 1, 2), SpawnSeed: 3, ItemSeed: 4, Policy: offlinePolicyBot, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.close()
	for _, seat := range hub.seats {
		if seat.bot != nil {
			seat.bot.trace = true
		}
	}
	type sample struct {
		pos    battleengine.Position
		action battleengine.Action
		line   int
		path   []battleengine.Cell
	}
	history := map[uint16][]sample{}
	for hub.over == nil && hub.link.clock < 250_000 {
		if _, err := hub.step(command{Cmd: "step", Ticks: 1}); err != nil {
			t.Fatal(err)
		}
		for _, playerID := range hub.order {
			seat := hub.seats[playerID]
			own, _ := actorByID(seat.engine.Actors(), playerID)
			h := append(history[playerID], sample{own.Position, seat.lastAction, seat.bot.traceLine, seat.bot.tracePath})
			if len(h) > 100 {
				h = h[1:]
			}
			history[playerID] = h
			if len(h) < 100 || seat.lastAction.Move == 0 || own.State != battleengine.ActorActive {
				continue
			}
			distinct := map[battleengine.Position]bool{}
			dither := true
			for _, entry := range h {
				distinct[entry.pos] = true
				if entry.action.Move == 0 {
					dither = false
				}
			}
			dither = dither && len(distinct) <= 6
			if dither {
				t.Logf("player %d dithers at %d around %+v cell %+v", playerID, hub.link.clock, own.Position, own.Position.Cell())
				for _, entry := range h[len(h)-14:] {
					t.Logf("  %+v %+v line %d path %v", entry.pos, entry.action, entry.line, entry.path)
				}
				world := newAutoplayWorld(seat.engine)
				for _, cell := range []battleengine.Cell{{Row: 8, Col: 12}, {Row: 8, Col: 13}, {Row: 9, Col: 12}, {Row: 9, Col: 14}} {
					tile, _ := world.grid.Cell(cell)
					_, threat := seat.bot.timeline.ImpactAt(cell)
					t.Logf("  cell %+v open %v threat %v worth %v reach %v tank %v mate %v tile %+v", cell, world.open(cell), threat, world.worthBombing(cell, own),
						seat.bot.opponentInReach(seat.engine, world, own, cell), seat.bot.tank != nil && seat.bot.tank.worthBombing(world, cell, own), seat.bot.hitsTeammate(seat.engine, world, own, cell), tile)
				}
				for _, other := range seat.engine.Actors() {
					t.Logf("  actor %d team %d cell %+v state %d", other.PlayerID, other.TeamID, other.Position.Cell(), other.State)
				}
				return
			}
		}
	}
}
