package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qqtang/internal/game/battleengine"
)

// TestReplayTutorials plays the client's three tutorial recordings through
// the spectator match: every recorded message must be consumed and the
// recorded deaths must happen.
func TestReplayTutorials(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	paths := []string{"Tutorial1.qbv", "Tutorial2.qbv", "Tutorial3.qbv"}
	// QQT_TEST_REPLAYS adds recordings this host saved (保存录像).
	if extra := os.Getenv("QQT_TEST_REPLAYS"); extra != "" {
		paths = append(paths, strings.Split(extra, string(os.PathListSeparator))...)
	}
	for _, name := range paths {
		t.Run(filepath.Base(name), func(t *testing.T) {
			path := name
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, name)
			}
			match, begin, err := newOnlineMatch(command{Cmd: "init", ClientRoot: root, Online: &onlineInit{Replay: path}})
			if err != nil {
				t.Fatal(err)
			}
			defer match.close()
			end := match.replay.events[len(match.replay.events)-1].TimeMS
			var deaths, bombs, explosions int
			for clock := uint32(battleengine.NativeRoundStartClockMS); clock < end+60; clock += 60 {
				events, err := match.step(command{Cmd: "step", ReplayMS: clock})
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range events {
					switch event.Kind {
					case battleengine.EventActorEliminated:
						deaths++
					case battleengine.EventBombPlaced:
						bombs++
					case battleengine.EventBombExploded:
						explosions++
					}
				}
			}
			if match.replay.next != len(match.replay.events) {
				t.Fatalf("applied %d of %d events", match.replay.next, len(match.replay.events))
			}
			states := map[uint16]battleengine.ActorState{}
			for _, actor := range match.engine.Actors() {
				states[actor.PlayerID] = actor.State
			}
			t.Logf("map %d rule %s: clock %d, bombs %d, explosions %d, deaths %d, actors %v", begin.MapID, begin.Rule, match.engine.ElapsedMS(), bombs, explosions, deaths, states)
		})
	}
}
