package battleenv

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

func workerTestMaps(t *testing.T) []mapdata.CompetitiveMap {
	t.Helper()
	if root := os.Getenv("QQT_TEST_CLIENT_ROOT"); root != "" {
		maps, err := LoadRuleMaps(root, OrdinaryRule{}, 2, 13, 15)
		if err != nil {
			t.Fatal(err)
		}
		return maps
	}
	return []mapdata.CompetitiveMap{benchmarkTrainingMap()}
}

func workerTestConfig(maps []mapdata.CompetitiveMap, envs int) Config {
	return Config{
		Maps: maps, EnvCount: envs, ParticipantCount: 4, ParticipantCounts: []int{2, 4},
		TickMS: 20, DecisionMS: 100, TrapDurationMS: 6_000, DangerHorizonMS: 3_500, BaseSeed: 99,
		TensorHeight: 13, TensorWidth: 15, ReuseTensorBuffers: true, AutoReset: true, DiscardEvents: true,
	}
}

// randomLegal picks a uniformly random legal action for every deciding slot.
func randomLegal(random *rand.Rand, tensors TensorBatch, actions []int16) {
	count := int(battleengine.DiscreteActionCount)
	for slot := range actions {
		actions[slot] = -1
		if tensors.DecisionReady[slot] == 0 {
			continue
		}
		var legal []int16
		for id, allowed := range tensors.Legal[slot*count : (slot+1)*count] {
			if allowed != 0 {
				legal = append(legal, int16(id))
			}
		}
		actions[slot] = legal[random.IntN(len(legal))]
	}
}

// observationBytes serializes everything a trainer sees after one step.
func observationBytes(result StepResult, tensors TensorBatch) []byte {
	var buffer bytes.Buffer
	_ = binary.Write(&buffer, binary.LittleEndian, tensors.Spatial)
	_ = binary.Write(&buffer, binary.LittleEndian, tensors.SpatialHalf)
	_ = binary.Write(&buffer, binary.LittleEndian, tensors.Scalars)
	buffer.Write(tensors.Legal)
	buffer.Write(tensors.DecisionReady)
	buffer.Write(tensors.TeamIDs)
	_ = binary.Write(&buffer, binary.LittleEndian, result.Rewards)
	buffer.Write(result.Dones)
	buffer.Write(result.Resets)
	buffer.Write(result.Ticks)
	for _, completed := range result.Completed {
		_ = binary.Write(&buffer, binary.LittleEndian, int32(completed.EnvIndex))
		_ = binary.Write(&buffer, binary.LittleEndian, completed.Returns)
	}
	return buffer.Bytes()
}

// TestBatchIsDeterministic: the same seed and actions give the same
// observations, rewards and resets whatever the goroutine count, with every
// curriculum, both cadences and half-precision output enabled.
func TestBatchIsDeterministic(t *testing.T) {
	maps := workerTestMaps(t)
	for _, server := range []bool{false, true} {
		run := func(workers int) [][]byte {
			config := workerTestConfig(maps, 12)
			config.WorkerCount = workers
			config.ServerCadence, config.RandomDecisionPhase = server, server
			config.HalfSpatial = server
			config.DuelCurriculumPermille, config.BlockadeCurriculumPermille = 100, 100
			config.ChainFinisherCurriculumPermille, config.BombEscapeCurriculumPermille = 100, 100
			config.DevelopmentCurriculumPermille, config.LateDuelReplayPermille = 100, 300
			batch, err := NewBatch(config)
			if err != nil {
				t.Fatal(err)
			}
			random := rand.New(rand.NewPCG(3, 4))
			tensors, err := batch.Observe()
			if err != nil {
				t.Fatal(err)
			}
			actions := make([]int16, 12*4)
			var frames [][]byte
			for step := 0; step < 400; step++ {
				randomLegal(random, tensors, actions)
				result, err := batch.StepFlat(actions)
				if err != nil {
					t.Fatal(err)
				}
				tensors = result.Observation
				frames = append(frames, observationBytes(result, tensors))
			}
			return frames
		}
		serial, parallel := run(1), run(6)
		for index := range serial {
			if !bytes.Equal(serial[index], parallel[index]) {
				t.Fatalf("server cadence %t: step %d differs between 1 and 6 goroutines", server, index)
			}
		}
	}
}

func TestAutoResetReportsEpisodesAndScalesRewards(t *testing.T) {
	maps := workerTestMaps(t)
	weights := DefaultRewardWeights()
	weights.ShapingScale = 0
	weights.TerminalScale = 2
	config := workerTestConfig(maps, 8)
	config.Rewards = &weights
	batch, err := NewBatch(config)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(8, 9))
	tensors, err := batch.Observe()
	if err != nil {
		t.Fatal(err)
	}
	actions := make([]int16, 8*4)
	returns := make([]float64, 8*4)
	episodes := 0
	for step := 0; step < 600; step++ {
		randomLegal(random, tensors, actions)
		result, err := batch.StepFlat(actions)
		if err != nil {
			t.Fatal(err)
		}
		tensors = result.Observation
		for slot, reward := range result.Rewards {
			if reward != 0 && result.Dones[slot/4] == 0 {
				t.Fatalf("step %d slot %d: shaping reward %g with ShapingScale 0", step, slot, reward)
			}
			returns[slot] += float64(reward)
		}
		for _, completed := range result.Completed {
			episodes++
			if result.Dones[completed.EnvIndex] != 1 || completed.Rule != 1 || completed.Metrics.Decisions == 0 {
				t.Fatalf("completed episode %+v", completed)
			}
			for actorIndex := range completed.PlayerIDs {
				slot := completed.EnvIndex*4 + actorIndex
				if math.Abs(returns[slot]-float64(completed.Returns[actorIndex])) > 1e-4 {
					t.Fatalf("slot %d summed %g, reported return %g", slot, returns[slot], completed.Returns[actorIndex])
				}
				if result.Resets[slot] != 1 && actorIndex < len(batch.PlayerIDs(completed.EnvIndex)) {
					t.Fatalf("slot %d of a restarted environment is not flagged", slot)
				}
				returns[slot] = 0
			}
			if completed.Outcome.Draw && completed.Returns[0] != 2*weights.Draw && len(completed.PlayerIDs) == 2 {
				t.Fatalf("1v1 draw return %g, want %g", completed.Returns[0], 2*weights.Draw)
			}
		}
		for envIndex, done := range result.Dones {
			if done == 1 && batch.Engine(envIndex).Terminal().Ended {
				t.Fatalf("environment %d was not reset", envIndex)
			}
		}
	}
	if episodes == 0 {
		t.Fatal("no episode ended")
	}
}

// cadenceReference is a verbatim port of the server's per-actor
// competitiveAIDecisionCadencePolicy.choose (internal/server/probe), driven
// on an independent engine clone.
type cadenceReference struct {
	cached      battleengine.Action
	initialized bool
	remaining   uint32
}

func actionAllowed(legal []battleengine.Action, action battleengine.Action) bool {
	for _, candidate := range legal {
		if candidate == action {
			return true
		}
	}
	return false
}

// TestServerCadenceMatchesLiveDecisionCadence replays the batch's decisions
// on a clone stepped tick by tick with the server's cadence logic and checks
// that both agree on which actors decide when, and on the resulting state.
func TestServerCadenceMatchesLiveDecisionCadence(t *testing.T) {
	maps := workerTestMaps(t)
	if len(maps) > 12 {
		maps = maps[:12]
	}
	early := 0
	for mapIndex, entry := range maps {
		config := workerTestConfig([]mapdata.CompetitiveMap{entry}, 1)
		config.AutoReset = false
		config.ServerCadence = true
		config.ParticipantCounts = []int{min(4, int(entry.PlayerLimit))}
		config.ParticipantCount = config.ParticipantCounts[0]
		config.BaseSeed = uint64(mapIndex) + 5
		batch, err := NewBatch(config)
		if err != nil {
			t.Fatal(err)
		}
		reference := batch.Engine(0).Clone()
		playerIDs := batch.PlayerIDs(0)
		states := make([]cadenceReference, len(playerIDs))
		random := rand.New(rand.NewPCG(uint64(entry.ID), 1))
		tensors, err := batch.Observe()
		if err != nil {
			t.Fatal(err)
		}
		actions := make([]int16, config.ParticipantCount)
		lastTicks, ticksSince := -1, 0
		for tick := 0; tick < 1500 && !reference.Terminal().Ended; tick++ {
			tickActions := make([]battleengine.Action, len(playerIDs))
			var need []int
			for index, playerID := range playerIDs {
				state := &states[index]
				legal, err := reference.LegalActions(playerID)
				if err != nil {
					t.Fatal(err)
				}
				held := state.cached
				held.PlaceBomb = false
				held.UseActionID = 0
				snapshot, err := reference.PolicySnapshot(playerID)
				if err != nil {
					t.Fatal(err)
				}
				canReuse := state.initialized && state.remaining > 0 && actionAllowed(legal, held) &&
					!snapshot.NeedsImmediatePolicyDecision(playerID)
				actor := reference.Actors()[index]
				if canReuse || actor.State == battleengine.ActorEliminated {
					if canReuse {
						state.remaining--
					}
					tickActions[index] = held
					if actor.State == battleengine.ActorEliminated {
						tickActions[index] = battleengine.Action{PlayerID: playerID}
					}
					continue
				}
				need = append(need, index)
			}
			if len(need) != 0 {
				if lastTicks >= 0 && lastTicks != ticksSince {
					t.Fatalf("map %d tick %d: batch advanced %d ticks, server cadence %d", entry.ID, tick, lastTicks, ticksSince)
				}
				if lastTicks >= 0 && lastTicks < 5 {
					early++
				}
				due := map[int]bool{}
				for _, index := range need {
					due[index] = true
				}
				for index := range playerIDs {
					if (tensors.DecisionReady[index] != 0) != due[index] {
						t.Fatalf("map %d tick %d: slot %d batch due=%d, server cadence due=%t", entry.ID, tick, index, tensors.DecisionReady[index], due[index])
					}
				}
				randomLegal(random, tensors, actions)
				for _, index := range need {
					chosen, ok := battleengine.ActionFromID(playerIDs[index], battleengine.ActionID(actions[index]))
					if !ok {
						t.Fatal("bad action")
					}
					states[index] = cadenceReference{cached: chosen, initialized: true, remaining: 4}
					tickActions[index] = chosen
				}
				result, err := batch.StepFlat(actions)
				if err != nil {
					t.Fatal(err)
				}
				tensors = result.Observation
				lastTicks, ticksSince = int(result.Ticks[0]), 0
			}
			if _, err := reference.Step(tickActions); err != nil {
				t.Fatal(err)
			}
			ticksSince++
			if ticksSince == lastTicks {
				left, right := reference.Actors(), batch.Engine(0).Actors()
				for index := range left {
					if left[index].Position != right[index].Position || left[index].State != right[index].State {
						t.Fatalf("map %d tick %d: actor %d diverged", entry.ID, tick, index)
					}
				}
			}
		}
	}
	if early == 0 {
		t.Fatal("no early re-decision occurred; the test did not exercise the cadence")
	}
	t.Logf("%d early (<5 tick) re-decisions matched the server cadence", early)
}
