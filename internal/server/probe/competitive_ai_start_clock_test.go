package probe

import (
	"reflect"
	"testing"
	"time"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/match"
	"qqtang/internal/protocol/game"
)

func TestCompetitiveAIWaitsForPlayableAuthorityClockWithoutReplayingIdleInputs(t *testing.T) {
	calls := 0
	live, err := newLiveCompetitiveAIRuntime(7, testCompetitiveAIMap(2),
		game.GameBeginData{GameID: 112, SpawnSeed: 11, ItemSeed: 22},
		[]match.CompetitiveParticipant{
			{PlayerID: 1, RoleID: 1, TeamID: 1, Source: match.CompetitiveParticipantHuman},
			{PlayerID: 20001, RoleID: 2, TeamID: 2, Source: match.CompetitiveParticipantVirtualAI},
		}, false, battleengine.PolicyFunc(func(observation battleengine.Observation, _ []battleengine.Action) (battleengine.Action, error) {
			calls++
			return battleengine.Action{PlayerID: observation.PlayerID, Move: battleengine.DirectionRight, PlaceBomb: true}, nil
		}), 100)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(100, 0)
	live.nativeGameStartedAt = started
	before := live.runtime.EngineSnapshot()
	// The native client can spend seconds loading after GAME_BEGIN and send
	// no gameplay at all while idle. Wall time cannot unlock AI control.
	for _, delay := range []time.Duration{3 * time.Second, 6 * time.Second, 45 * time.Second} {
		now := started.Add(delay)
		live.observeAuthorityClockLocked(2999, now)
		if target := live.targetElapsedMSLocked(now); target != 3000 {
			t.Fatalf("AI unlocked during loading/ReadyGo: target %d", target)
		}
		if err := live.advanceToLocked(&Server{}, live.targetElapsedMSLocked(now), 5); err != nil {
			t.Fatal(err)
		}
	}
	// The first human input may arrive well after Go. Seed the scene clock,
	// retaining spawn state and untouched recurrent policies, rather than
	// emitting a burst of obsolete moves and bubbles to catch up.
	observed := started.Add(45 * time.Second)
	live.observeAuthorityClockLocked(12_345, observed)
	after := live.runtime.EngineSnapshot()
	if calls != 0 || after.ElapsedMS() != 12_340 || !reflect.DeepEqual(before.Actors(), after.Actors()) || len(after.Bombs()) != 0 {
		t.Fatalf("initial synchronization changed gameplay: calls=%d clock=%d bombs=%v", calls, after.ElapsedMS(), after.Bombs())
	}
	if target := live.targetElapsedMSLocked(observed.Add(100 * time.Millisecond)); target != 12_440 {
		t.Fatalf("synchronized target = %d", target)
	}
	live.observeAuthorityClockLocked(12_345, observed.Add(time.Second))
	if live.authorityClockAt != observed {
		t.Fatal("duplicate clock moved the authority anchor")
	}
	for step := 0; step < 6; step++ {
		if _, err := live.runtime.StepWithTrace(nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls == 0 {
		t.Fatal("AI policy did not resume after clock synchronization")
	}
	if err := live.runtime.SynchronizeInitialClock(20_000); err == nil {
		t.Fatal("an active world's clock was reset")
	}
}
