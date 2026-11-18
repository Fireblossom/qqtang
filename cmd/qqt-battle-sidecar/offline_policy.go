package main

// AI seat policies for offline matches. Rules 1 and 4 (ordinary combat,
// wrestle = rule 1 with relives) can use the server's rule-1 model (U2392,
// configs/models/qqtang-rule1.onnx) exactly as the server deploys it: one
// actor-local recurrent policy per seat, sampled near-best actions (the
// metadata's MinP/temperature) from a seat-seeded stream, a decision every
// 100 ms with the chosen key held in between (probe
// competitiveAIDecisionCadencePolicy). It needs a sidecar built with
// -tags onnxruntime and the ONNX Runtime library; otherwise, and for every
// other rule, the autoplay bot plays the seat.

import (
	"fmt"
	"os"
	"path/filepath"

	"qqtang/internal/game/battleai"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	offlinePolicyAuto = "auto"
	offlinePolicyBot  = "bot"
	offlinePolicyONNX = "onnx"
	// The server's competitive_ai.tick_ms (configs/server*.json).
	offlineONNXDecisionMS = 100
)

// Set by build-sidecar.sh (-ldflags -X) when it finds the files.
var (
	defaultONNXModel   = ""
	defaultONNXLibrary = ""
)

type offlinePolicies struct {
	loaded battleai.LoadedPolicy
	onnx   bool // the model is loaded for this match
	begin  game.GameBeginData
	reason string // why the bot plays rules 1/4
	// Seats whose own policy is the bot.
	botSeats map[uint16]bool
}

func newOfflinePolicies(options offlineInit, begin game.GameBeginData, entry mapdata.CompetitiveMap, competitive, boss bool) (*offlinePolicies, error) {
	policies := &offlinePolicies{begin: begin, botSeats: map[uint16]bool{}}
	mode := options.Policy
	if mode == "" {
		mode = offlinePolicyAuto
	}
	wanted := false
	for _, seat := range options.Players {
		seatMode := seat.Policy
		if seatMode == "" {
			seatMode = mode
		}
		switch seatMode {
		case offlinePolicyBot:
			policies.botSeats[seat.ID] = true
		case offlinePolicyAuto, offlinePolicyONNX:
			wanted = true
			if seatMode == offlinePolicyONNX {
				mode = offlinePolicyONNX // a seat that asks for the model needs it
			}
		default:
			return nil, fmt.Errorf("unknown offline AI policy %q", seatMode)
		}
	}
	if !wanted {
		return policies, nil
	}
	combat := competitive && !boss &&
		(entry.Rule == mapdata.CompetitiveRuleOrdinary || entry.Rule == mapdata.CompetitiveRuleWrestle)
	if !combat {
		if mode == offlinePolicyONNX {
			return nil, fmt.Errorf("the ONNX model only plays rules 1 and 4 without a Boss")
		}
		return policies, nil
	}
	model, library := offlineONNXPaths(options)
	loaded, err := battleai.LoadDeploymentPolicy(battleai.DeploymentPolicyConfig{
		Backend: battleai.DeploymentBackendONNXRuntime, ModelPath: model, MetadataPath: model + ".json",
		SharedLibraryPath: library, IntraOpThreads: 1, InterOpThreads: 1,
		ActorPolicyConfig: battleai.ActorPolicyConfig{DangerHorizonMS: 3_500, DecisionMS: offlineONNXDecisionMS},
	})
	if err != nil {
		if mode == offlinePolicyONNX {
			return nil, err
		}
		policies.reason = err.Error()
		fmt.Fprintf(os.Stderr, "[offline] rule-1 model unavailable, the bot plays: %v\n", err)
		return policies, nil
	}
	policies.loaded, policies.onnx = loaded, true
	return policies, nil
}

// offlineONNXPaths resolves the model and the ONNX Runtime library: the
// request, the environment, the build's defaults, then the repository's
// configs/models next to the sidecar (client/native/generated/bin).
func offlineONNXPaths(options offlineInit) (string, string) {
	model, library := options.Model, options.Library
	if model == "" {
		model = os.Getenv("QQT_ONNX_MODEL")
	}
	if model == "" {
		model = defaultONNXModel
	}
	if model == "" {
		var candidates []string
		if executable, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(executable), "..", "..", "..", "..", "configs", "models", "qqtang-rule1.onnx"))
		}
		if working, err := os.Getwd(); err == nil {
			candidates = append(candidates, filepath.Join(working, "configs", "models", "qqtang-rule1.onnx"),
				filepath.Join(working, "..", "..", "configs", "models", "qqtang-rule1.onnx"))
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				model = candidate
				break
			}
		}
	}
	if library == "" {
		library = os.Getenv("QQT_ONNXRUNTIME_LIB")
	}
	if library == "" {
		library = defaultONNXLibrary
	}
	return model, library
}

func (policies *offlinePolicies) forSeat(playerID uint16) localPolicy {
	if !policies.onnx || policies.botSeats[playerID] {
		return nil
	}
	steps := uint32(offlineONNXDecisionMS / tickMS)
	var actor battleengine.Policy = policies.loaded.Policy
	if factory, ok := actor.(interface {
		NewSeededActorPolicy(seed uint64) battleengine.Policy
	}); ok {
		actor = factory.NewSeededActorPolicy(offlineSampleSeed(policies.begin, playerID))
	}
	return &onnxSeatPolicy{player: playerID, base: actor, steps: steps,
		wait: offlineDecisionPhase(policies.begin, playerID, steps)}
}

func (policies *offlinePolicies) name(playerID uint16) string {
	if policies.onnx && !policies.botSeats[playerID] {
		variant := policies.loaded.Contract.ModelVariant
		if variant == "" {
			variant = "model"
		}
		return "onnx:" + variant
	}
	return "bot"
}

// offlineSampleSeed and offlineDecisionPhase follow probe
// competitiveAISampleSeed and competitiveAIDecisionPhase.
func offlineSampleSeed(begin game.GameBeginData, playerID uint16) uint64 {
	state := uint64(begin.GameID)<<32 ^ uint64(begin.SpawnSeed)<<1 ^ uint64(begin.ItemSeed) ^ uint64(playerID)*0x9E3779B97F4A7C15
	return splitMix(&state)
}

func offlineDecisionPhase(begin game.GameBeginData, playerID uint16, steps uint32) uint32 {
	if steps <= 1 {
		return 0
	}
	state := uint64(begin.GameID)<<32 ^ uint64(begin.SpawnSeed) ^ uint64(begin.ItemSeed)<<1 ^ uint64(playerID)*0xD6E8FEB86659FD93
	return uint32(splitMix(&state) % uint64(steps))
}

func splitMix(state *uint64) uint64 {
	*state += 0x9E3779B97F4A7C15
	z := *state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// onnxSeatPolicy is the server's decision cadence around one seat's model
// policy: infer every steps ticks (sooner when the engine needs a turn
// during a native pass), hold the chosen direction in between, and send
// bubble and item pulses only on the decision that chose them.
type onnxSeatPolicy struct {
	player      uint16
	base        battleengine.Policy
	steps       uint32
	wait        uint32
	remaining   uint32
	cached      battleengine.Action
	initialized bool
	failed      bool
}

func (policy *onnxSeatPolicy) act(engine *battleengine.Engine) battleengine.Action {
	idle := battleengine.Action{PlayerID: policy.player}
	if policy.failed {
		return idle
	}
	legal, err := engine.LegalActions(policy.player)
	if err != nil {
		return idle
	}
	if !policy.initialized && policy.wait > 0 {
		policy.wait--
		return idle
	}
	held := policy.cached
	held.PlaceBomb, held.UseActionID = false, 0
	if policy.initialized && policy.remaining > 0 && containsLegal(legal, held) && !engine.NeedsImmediatePolicyDecision(policy.player) {
		policy.remaining--
		return held
	}
	observation, err := engine.Observation(policy.player)
	if err != nil {
		return idle
	}
	var chosen battleengine.Action
	if snapshotPolicy, ok := policy.base.(battleengine.SnapshotPolicy); ok {
		snapshot, snapErr := engine.PolicySnapshot(policy.player)
		if snapErr != nil {
			return idle
		}
		chosen, err = snapshotPolicy.ChooseActionWithSnapshot(snapshot, observation, legal)
	} else {
		chosen, err = policy.base.ChooseAction(observation, legal)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[offline] model of seat %d failed: %v\n", policy.player, err)
		policy.failed = true
		return idle
	}
	policy.cached, policy.initialized, policy.remaining = chosen, true, policy.steps-1
	return chosen
}

func containsLegal(legal []battleengine.Action, wanted battleengine.Action) bool {
	for _, candidate := range legal {
		if candidate == wanted {
			return true
		}
	}
	return false
}
