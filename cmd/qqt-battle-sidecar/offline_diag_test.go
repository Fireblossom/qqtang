package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"qqtang/internal/game/battleengine"
)

// TestOfflineSelfTrapDiagnosis (QQT_OFFLINE_DIAG=map) prints the moments
// before every self-trap of a bot seat on one map.
func TestOfflineSelfTrapDiagnosis(t *testing.T) {
	mapName := os.Getenv("QQT_OFFLINE_DIAG")
	if mapName == "" {
		t.Skip("QQT_OFFLINE_DIAG is not set")
	}
	root := offlineClientRoot(t)
	var mapID uint32
	fmt.Sscan(mapName, &mapID)
	hub, err := newOfflineHub(root, offlineInit{MapID: mapID, Players: offlineTeams(2, 1, 2), SpawnSeed: 77, ItemSeed: 78, Policy: offlinePolicyBot, Quiet: true})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.close()
	type sample struct {
		at     uint32
		pos    battleengine.Position
		action battleengine.Action
		bombs  string
	}
	history := map[uint16][]sample{}
	reported := 0
	for hub.over == nil && hub.link.clock < 200_000 && reported < 6 {
		if _, err := hub.step(command{Cmd: "step", Ticks: 1}); err != nil {
			t.Fatal(err)
		}
		for _, playerID := range hub.order {
			seat := hub.seats[playerID]
			actor, _ := actorByID(seat.engine.Actors(), playerID)
			var bombs []string
			for _, bomb := range seat.engine.Bombs() {
				bombs = append(bombs, fmt.Sprintf("%d@%d,%d p%d t%d", bomb.OwnerID, bomb.Cell.Row, bomb.Cell.Col, bomb.Power, bomb.ExplodeAtMS))
			}
			if seat.bot != nil {
				cell := actor.Position.Cell()
				cached, cachedOK := seat.bot.timeline.ImpactAt(cell)
				fresh, _ := seat.engine.DangerTimeline(autoplayHorizonMS)
				now, nowOK := fresh.ImpactAt(cell)
				bombs = append(bombs, fmt.Sprintf("| cell %d,%d cached %v/%d fresh %v/%d planned %d", cell.Row, cell.Col, cachedOK, cached, nowOK, now, seat.bot.plannedAt))
			}
			h := append(history[playerID], sample{at: seat.engine.ElapsedMS(), pos: actor.Position, action: seat.lastAction, bombs: strings.Join(bombs, " ")})
			if len(h) > 120 {
				h = h[len(h)-120:]
			}
			history[playerID] = h
			if seat.lastSelfTrap != 0 {
				reported++
				t.Logf("player %d self-trapped at %d (speed %d px/s):", playerID, seat.lastSelfTrap, actor.SpeedPixelsPerSecond)
				for index, entry := range h {
					if index%5 == 0 || index > len(h)-10 {
						t.Logf("  %d pos %d,%d cell %+v act %+v bombs [%s]", entry.at, entry.pos.X, entry.pos.Y, entry.pos.Cell(), entry.action, entry.bombs)
					}
				}
				seat.lastSelfTrap = 0
			}
		}
	}
}
