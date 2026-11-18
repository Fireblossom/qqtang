package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

// TestOfflineBench measures the AI seats with headless bot-vs-bot matches:
// QQT_OFFLINE_BENCH=N plays N matches (2 vs 2) on every map of each rule in
// turn and prints, per rule, how matches end, their length and how often a
// player was caught by its own or a teammate's bubble. QQT_OFFLINE_BENCH_RULES
// (comma list of rule kinds) narrows it; QQT_OFFLINE_BENCH_POLICY sets team
// 1's policy ("bot" by default; "onnx" needs -tags onnxruntime).
func TestOfflineBench(t *testing.T) {
	count, _ := strconv.Atoi(os.Getenv("QQT_OFFLINE_BENCH"))
	if count <= 0 {
		t.Skip("QQT_OFFLINE_BENCH is not set")
	}
	root := offlineClientRoot(t)
	catalog, err := mapdata.LoadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	rules := []mapdata.CompetitiveRuleKind{
		mapdata.CompetitiveRuleOrdinary, mapdata.CompetitiveRuleKickBomb, mapdata.CompetitiveRuleBun, mapdata.CompetitiveRuleWrestle,
		mapdata.CompetitiveRuleTreasure, mapdata.CompetitiveRuleSculpture, mapdata.CompetitiveRuleMachine, mapdata.CompetitiveRuleBox,
		mapdata.CompetitiveRuleTank,
	}
	if only := os.Getenv("QQT_OFFLINE_BENCH_RULES"); only != "" {
		rules = nil
		for _, name := range strings.Split(only, ",") {
			rules = append(rules, mapdata.CompetitiveRuleKind(strings.TrimSpace(name)))
		}
	}
	teamPolicy := os.Getenv("QQT_OFFLINE_BENCH_POLICY")
	var lines []string
	for _, rule := range rules {
		var maps []mapdata.CompetitiveMap
		for _, id := range catalog.CompetitiveIDs() {
			if entry, _ := catalog.CompetitiveMap(id); entry.Rule == rule && len(entry.BossCandidates) == 0 && entry.PlayerLimit >= 4 {
				maps = append(maps, entry)
			}
		}
		if len(maps) == 0 {
			continue
		}
		var wins [3]int
		var totalMS, timeouts int
		var self, team, deaths, objective int
		for index := 0; index < count; index++ {
			entry := maps[index%len(maps)]
			players := offlineTeams(2, 1, 2)
			if os.Getenv("QQT_OFFLINE_BENCH_SWAP") != "" {
				players = offlineTeams(2, 2, 1) // the arbitrator (player 1) on team 2
				if os.Getenv("QQT_OFFLINE_BENCH_SWAP") == "mix" {
					players = offlineTeams(1, 1, 2, 1, 2)
				}
			}
			for seat := range players {
				players[seat].AI = true
				if players[seat].Team == 1 && teamPolicy != "" {
					players[seat].Policy = teamPolicy
				} else {
					players[seat].Policy = offlinePolicyBot
				}
			}
			seed := uint32(1000 + index*7919)
			hub, over := offlineRun(t, root, offlineInit{MapID: entry.ID, Players: players, SpawnSeed: seed, ItemSeed: seed ^ 0x5A5A,
				Library: os.Getenv("QQTANG_TEST_ONNX_RUNTIME")}, 600_000)
			winner := 0
			for _, result := range over.Results {
				if result.Result == game.GameResultWin {
					for _, info := range hub.begin.Players {
						if info.PlayerID == result.PlayerID {
							winner = int(info.TeamID)
						}
					}
				}
			}
			wins[winner]++
			totalMS += int(hub.link.clock)
			if hub.referee.roundMS > 0 && hub.link.clock >= hub.referee.roundMS {
				timeouts++
			}
			for _, stats := range hub.referee.stats {
				self += stats.SelfTraps
				team += stats.TeamTraps
				deaths += stats.Deaths
				objective += stats.Objective
			}
		}
		line := fmt.Sprintf("%-10s %3d matches on %2d maps: team1 %2d team2 %2d draw %2d | avg %5.1f s, %2d timeouts | per match: deaths %4.1f self-traps %4.1f team-traps %4.1f objectives %4.1f",
			rule, count, len(maps), wins[1], wins[2], wins[0], float64(totalMS)/float64(count)/1000, timeouts,
			float64(deaths)/float64(count), float64(self)/float64(count), float64(team)/float64(count), float64(objective)/float64(count))
		t.Log(line)
		lines = append(lines, line)
	}
	sort.Strings(lines)
	if path := os.Getenv("QQT_OFFLINE_BENCH_OUT"); path != "" {
		_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
}
