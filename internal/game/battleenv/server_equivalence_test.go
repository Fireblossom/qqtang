package battleenv_test

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"qqtang/internal/game/battleai"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/battleenv"
	"qqtang/internal/game/mapdata"
)

// recordingRunner captures exactly the tensors that the server's learned
// actor hands to ONNX Runtime.
type recordingRunner struct {
	spatial, scalars []float32
	legal            []uint8
	memory           int
}

func (runner *recordingRunner) record(spatial, scalars []float32, legal []uint8) []float32 {
	runner.spatial = append(runner.spatial[:0], spatial...)
	runner.scalars = append(runner.scalars[:0], scalars...)
	runner.legal = append(runner.legal[:0], legal...)
	return make([]float32, len(legal))
}

func (runner *recordingRunner) RunActor(spatial, scalars []float32, legal []uint8) ([]float32, error) {
	return runner.record(spatial, scalars, legal), nil
}

func (runner *recordingRunner) RunActorRecurrent(spatial, scalars []float32, legal []uint8, memory []float32, _ bool) ([]float32, []float32, error) {
	return runner.record(spatial, scalars, legal), make([]float32, len(memory)), nil
}

func deploymentContract(t *testing.T) battleai.Contract {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "configs", "models", "qqtang-rule1.onnx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract battleai.Contract
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	return contract
}

func sameFloat32Bits(left, right []float32) int {
	if len(left) != len(right) {
		return 0
	}
	for index := range left {
		if math.Float32bits(left[index]) != math.Float32bits(right[index]) {
			return index
		}
	}
	return -1
}

// TestWorkerObservationMatchesServerInference drives the vectorized
// environment on every eligible real rule-1 map and checks, for every live
// actor at every decision, that the worker's spatial/scalar/legal rows are
// bit-identical to what the server's live inference path feeds the model
// for the same engine state: battleengine.Runtime hands each virtual actor
// engine.Observation plus engine.PolicySnapshot, and battleai.NeuralCandidates
// (configured as internal/server/probe/server.go does: 3500 ms danger
// horizon, decision interval = 100 ms AI tick) encodes them with
// battleenv.EncodeActorAtDecision at the contract's 13x15 shape.
func TestWorkerObservationMatchesServerInference(t *testing.T) {
	root := os.Getenv("QQT_TEST_CLIENT_ROOT")
	if root == "" {
		t.Skip("QQT_TEST_CLIENT_ROOT is not set")
	}
	contract := deploymentContract(t)
	maps, err := battleenv.LoadRuleMaps(root, battleenv.OrdinaryRule{}, 2, contract.Height, contract.Width)
	if err != nil {
		t.Fatal(err)
	}
	decisions := 150
	if testing.Short() {
		maps = maps[:min(len(maps), 4)]
		decisions = 15
	}
	checked, hiddenVisible := 0, 0
	for mapIndex, entry := range maps {
		participants := 4
		if int(entry.PlayerLimit) < participants {
			participants = int(entry.PlayerLimit)
		}
		if mapIndex%3 == 0 && entry.PlayerLimit >= 8 {
			participants = 8
		}
		// Every second map also runs with the server's decision cadence,
		// where actors decide on different ticks.
		serverCadence := mapIndex%2 == 1
		t.Run(fmt.Sprintf("map_%d_%dp_server_cadence_%t", entry.ID, participants, serverCadence), func(t *testing.T) {
			layout := make([]uint8, participants)
			for index := range layout {
				layout[index] = uint8(index%2 + 1)
			}
			batch, err := battleenv.NewBatch(battleenv.Config{
				Maps: []mapdata.CompetitiveMap{entry}, EnvCount: 1,
				ParticipantCount: 8, ParticipantCounts: []int{participants}, TeamLayouts: [][]uint8{layout},
				TickMS: 20, DecisionMS: 100, TrapDurationMS: 6_000, DangerHorizonMS: 3_500,
				BaseSeed: uint64(entry.ID)*7919 + 17, TensorHeight: contract.Height, TensorWidth: contract.Width,
				ReuseTensorBuffers: true, AutoReset: true, WorkerCount: 1,
				ServerCadence: serverCadence, RandomDecisionPhase: serverCadence,
			})
			if err != nil {
				t.Fatal(err)
			}
			random := rand.New(rand.NewPCG(uint64(entry.ID), 99))
			spatialSize := contract.Channels * contract.Height * contract.Width
			actionCount := contract.Actions
			observation, err := batch.Observe()
			if err != nil {
				t.Fatal(err)
			}
			for decision := 0; decision < decisions; decision++ {
				engine := batch.Engine(0)
				for slot, playerID := range batch.PlayerIDs(0) {
					if observation.Active[slot] == 0 {
						continue
					}
					runner := &recordingRunner{}
					candidates := &battleai.NeuralCandidates{
						Contract: contract, Runner: runner, DangerHorizonMS: 3_500, DecisionMS: 100,
					}
					snapshot, err := engine.PolicySnapshot(playerID)
					if err != nil {
						t.Fatal(err)
					}
					actorObservation, err := engine.Observation(playerID)
					if err != nil {
						t.Fatal(err)
					}
					for _, pickup := range actorObservation.Pickups {
						if pickup.State == battleengine.PickupHidden {
							hiddenVisible++
							break
						}
					}
					legalActions, err := engine.LegalActions(playerID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := candidates.CandidateActionsWithSnapshot(snapshot, actorObservation, legalActions, 0); err != nil {
						t.Fatal(err)
					}
					spatial := observation.Spatial[slot*spatialSize : (slot+1)*spatialSize]
					scalars := observation.Scalars[slot*contract.Scalars : (slot+1)*contract.Scalars]
					legal := observation.Legal[slot*actionCount : (slot+1)*actionCount]
					if index := sameFloat32Bits(runner.spatial, spatial); index >= 0 {
						t.Fatalf("decision %d player %d spatial differs at %d (len %d/%d)", decision, playerID, index, len(runner.spatial), len(spatial))
					}
					if index := sameFloat32Bits(runner.scalars, scalars); index >= 0 {
						t.Fatalf("decision %d player %d scalar %d differs: server %v worker %v", decision, playerID, index, runner.scalars[max(index, 0)], scalars[max(index, 0)])
					}
					if string(runner.legal) != string(legal) {
						t.Fatalf("decision %d player %d legal differs: server %v worker %v", decision, playerID, runner.legal, legal)
					}
					checked++
				}
				actions := make([]int16, 8)
				for slot := range actions {
					actions[slot] = -1
					if observation.Active[slot] == 0 {
						continue
					}
					legal := observation.Legal[slot*actionCount : (slot+1)*actionCount]
					var choices []int16
					for id, allowed := range legal {
						if allowed != 0 {
							choices = append(choices, int16(id))
							// Bias toward bubbles so danger, chains, traps
							// and revealed wall items occur early.
							if id >= int(battleengine.ActionPlaceBomb) && id <= 9 {
								choices = append(choices, int16(id), int16(id))
							}
						}
					}
					actions[slot] = choices[random.IntN(len(choices))]
				}
				result, err := batch.StepFlat(actions)
				if err != nil {
					t.Fatal(err)
				}
				observation = result.Observation
			}
		})
	}
	t.Logf("checked %d actor decisions on %d maps; %d had visible hidden pickups", checked, len(maps), hiddenVisible)
}
