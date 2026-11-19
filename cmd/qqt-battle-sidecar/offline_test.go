package main

import (
	"os"
	"strconv"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

func offlineClientRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	return root
}

// offlineRun plays one headless offline match (every seat an AI) until its
// GAME_OVER, or fails after limitMS of scene time.
func offlineRun(t *testing.T, root string, options offlineInit, limitMS uint32) (*offlineHub, game.GameOverData) {
	t.Helper()
	options.Quiet = !testing.Verbose()
	hub, err := newOfflineHub(root, options)
	if err != nil {
		t.Fatalf("map %d: %v", options.MapID, err)
	}
	t.Cleanup(hub.close)
	for hub.over == nil {
		if hub.link.clock > limitMS {
			for _, playerID := range hub.order {
				seat := hub.seats[playerID]
				for _, actor := range seat.engine.Actors() {
					t.Logf("seat %d at %d ms: actor %d state %d at %+v (spawn %+v)", playerID, seat.engine.ElapsedMS(), actor.PlayerID, actor.State, actor.Position, actor.Spawn)
				}
			}
			t.Fatalf("map %d: no GAME_OVER after %d ms (round %d ms)", options.MapID, hub.link.clock, hub.referee.roundMS)
		}
		if _, err := hub.step(command{Cmd: "step", Ticks: 10}); err != nil {
			t.Fatalf("map %d at %d ms: %v", options.MapID, hub.link.clock, err)
		}
	}
	return hub, *hub.over
}

func offlineTeams(perTeam int, teams ...byte) []player {
	var players []player
	for _, team := range teams {
		for index := 0; index < perTeam; index++ {
			id := uint16(len(players) + 1)
			players = append(players, player{ID: id, Role: uint16(len(players)%11 + 1), Team: team, AI: true})
		}
	}
	return players
}

func TestOfflineRuleOneConcludes(t *testing.T) {
	root := offlineClientRoot(t)
	hub, over := offlineRun(t, root, offlineInit{MapID: 311, Players: offlineTeams(1, 1, 2), SpawnSeed: 7, ItemSeed: 9, Policy: offlinePolicyBot}, 200_000)
	if len(over.Results) != 2 {
		t.Fatalf("results = %+v", over.Results)
	}
	t.Logf("map 311 ended at %d ms: %+v; stats %+v %+v", hub.link.clock, over.Results, *hub.referee.stats[1], *hub.referee.stats[2])
}

// TestOfflineEveryRule plays one map of each rule to its end.
func TestOfflineEveryRule(t *testing.T) {
	root := offlineClientRoot(t)
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []mapdata.CompetitiveRuleKind{
		mapdata.CompetitiveRuleOrdinary, mapdata.CompetitiveRuleKickBomb, mapdata.CompetitiveRuleBun, mapdata.CompetitiveRuleWrestle,
		mapdata.CompetitiveRuleTreasure, mapdata.CompetitiveRuleSculpture, mapdata.CompetitiveRuleMachine, mapdata.CompetitiveRuleBox,
		mapdata.CompetitiveRuleTank,
	} {
		entry, ok := offlineMapForRule(catalog, rule, 4)
		if !ok {
			t.Errorf("no map for rule %s", rule)
			continue
		}
		t.Run(string(rule), func(t *testing.T) {
			hub, over := offlineRun(t, root, offlineInit{MapID: entry.ID, Players: offlineTeams(2, 1, 2), SpawnSeed: 3, ItemSeed: 5, Policy: offlinePolicyBot}, 400_000)
			t.Logf("map %d (%s) ended at %d ms: %+v", entry.ID, rule, hub.link.clock, over.Results)
		})
	}
}

// TestOfflineBossOverlay: map 11 always brings its sailor Boss; the AI
// players fight it together.
func TestOfflineBossOverlay(t *testing.T) {
	root := offlineClientRoot(t)
	for _, mapID := range []uint32{11, 12} {
		t.Run(strconv.Itoa(int(mapID)), func(t *testing.T) {
			hub, over := offlineRun(t, root, offlineInit{MapID: mapID, Players: offlineTeams(1, 1, 2, 3), SpawnSeed: 11, ItemSeed: 13, Boss: true, Policy: offlinePolicyBot}, 400_000)
			if hub.boss == nil || !hub.bossSent {
				t.Fatal("no Boss was announced")
			}
			t.Logf("map %d ended at %d ms: %+v", mapID, hub.link.clock, over.Results)
		})
	}
}

// TestOfflineAdventure plays an adventure route with two AI players: the
// stages must chain (or the route end) without errors.
func TestOfflineAdventure(t *testing.T) {
	root := offlineClientRoot(t)
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	ids := catalog.SelectableIDs()
	if len(ids) == 0 {
		t.Skip("no adventure stages")
	}
	players := []player{{ID: 1, Role: 1, Team: 1, AI: true, ExtPoint: 3000}, {ID: 2, Role: 2, Team: 1, AI: true, ExtPoint: 3000}}
	hub, over := offlineRun(t, root, offlineInit{MapID: ids[0], Players: players, SpawnSeed: 5, ItemSeed: 6, Policy: offlinePolicyBot}, 3_000_000)
	t.Logf("route from %d ended on stage %d (%d stages passed) at %d ms: %+v", ids[0], hub.begin.MapID, hub.stageNumber, hub.link.clock, over.Results)
}

func offlineMapForRule(catalog *mapdata.Catalog, rule mapdata.CompetitiveRuleKind, players int) (mapdata.CompetitiveMap, bool) {
	for _, id := range catalog.CompetitiveIDs() {
		entry, _ := catalog.CompetitiveMap(id)
		if entry.Rule == rule && len(entry.BossCandidates) == 0 && int(entry.PlayerLimit) >= players {
			return entry, true
		}
	}
	return mapdata.CompetitiveMap{}, false
}

var _ = battleengine.NativeRoundStartClockMS

// TestOfflineNPCMessagesReachEverySeat: the arbitrator's NPC messages go
// through the hub, so every seat sees the adventure NPCs and the outbox
// drains (flushNPC once returned early without a connection).
func TestOfflineNPCMessagesReachEverySeat(t *testing.T) {
	root := offlineClientRoot(t)
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	ids := catalog.SelectableIDs()
	if len(ids) == 0 {
		t.Skip("no adventure stages")
	}
	players := []player{{ID: 1, Role: 1, Team: 1, AI: true, ExtPoint: 3000}, {ID: 2, Role: 2, Team: 1, AI: true, ExtPoint: 3000}}
	hub, err := newOfflineHub(root, offlineInit{MapID: ids[0], Players: players, SpawnSeed: 5, ItemSeed: 6, Policy: offlinePolicyBot, Quiet: !testing.Verbose()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.close)
	for hub.over == nil && hub.link.clock < 30_000 {
		if _, err := hub.step(command{Cmd: "step", Ticks: 10}); err != nil {
			t.Fatal(err)
		}
	}
	arbitrator := hub.seats[hub.order[0]]
	if arbitrator.adventure == nil || len(arbitrator.adventure.npcs) == 0 {
		t.Skip("the stage has no NPCs by now")
	}
	if n := len(arbitrator.adventure.outbox); n > 64 {
		t.Errorf("arbitrator NPC outbox holds %d messages", n)
	}
	for _, id := range hub.order[1:] {
		if seat := hub.seats[id]; seat.adventure == nil || len(seat.adventure.npcs) == 0 {
			t.Errorf("seat %d sees no NPCs (arbitrator has %d)", id, len(arbitrator.adventure.npcs))
		}
	}
}

// TestOfflineAISeatsDropSkillNotices: only the local view shows charged-skill
// notices; an AI seat's are dropped each step instead of piling up.
func TestOfflineAISeatsDropSkillNotices(t *testing.T) {
	root := offlineClientRoot(t)
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := offlineMapForRule(catalog, mapdata.CompetitiveRuleMachine, 2)
	if !ok {
		t.Skip("no machine map")
	}
	hub, err := newOfflineHub(root, offlineInit{MapID: entry.ID, Players: offlineTeams(1, 1, 2), SpawnSeed: 3, ItemSeed: 5, Policy: offlinePolicyBot, Quiet: !testing.Verbose()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.close)
	var ai *onlineMatch
	for id, seat := range hub.seats {
		if id != hub.local {
			ai = seat
		}
	}
	if ai == nil || ai.machine == nil {
		t.Fatal("no machine state on the AI seat")
	}
	ai.machine.skill.notices = append(ai.machine.skill.notices, machineSkillNotice{Player: hub.local, Type: 1})
	if _, err := hub.step(command{Cmd: "step", Ticks: 1}); err != nil {
		t.Fatal(err)
	}
	if n := len(ai.machine.skill.notices); n != 0 {
		t.Errorf("AI seat keeps %d skill notices", n)
	}
}
