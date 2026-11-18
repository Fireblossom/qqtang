// Command dump_observations plays short rule-1 battles on real maps through
// the server's own inference path (battleai.NeuralCandidates, which encodes
// with battleenv.EncodeActorAtDecision) and records every model call: the
// exact spatial/scalars/legal/memory/reset tensors the server hands to the
// ONNX actor and, when built with -tags onnxruntime and given -model, the
// logits and next_memory ONNX Runtime returned. training/tests use the dump
// to check PyTorch parity on real observations.
//
//	go run ./training/tools/dump_observations -client-root DIR -out DIR
//	go run -tags onnxruntime ./training/tools/dump_observations -client-root DIR -out DIR \
//	    -model configs/models/qqtang-rule1.onnx -ort libonnxruntime.dylib [-compare other.onnx]
//
// Without a model every actor plays random legal actions (the stub runner
// returns random logits). With -compare, a second model is run on exactly the
// same inputs and the maximum logit/memory difference is reported.
//
// Output files (little-endian, N = number of records, in call order):
// spatial.f32 [N,117,13,15], scalars.f32 [N,187], legal.u8 [N,45],
// memory.f32 [N,256], reset.u8 [N], stream.i32 [N] (one id per actor and
// episode; memory chains within a stream), logits.f32 [N,45] and
// next_memory.f32 [N,256] (zeros when no model ran), meta.json.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"qqtang/internal/game/battleai"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
)

const decisionMS = 100

type recorder struct {
	mu      sync.Mutex
	files   map[string]*os.File
	count   int
	compare *comparison
}

type comparison struct {
	runner         battleai.RecurrentLogitRunner
	maxLogitDiff   float64
	maxMemoryDiff  float64
	argmaxMismatch int
}

func newRecorder(dir string) (*recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	rec := &recorder{files: map[string]*os.File{}}
	for _, name := range []string{"spatial.f32", "scalars.f32", "legal.u8", "memory.f32", "reset.u8", "stream.i32", "logits.f32", "next_memory.f32"} {
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		rec.files[name] = file
	}
	return rec, nil
}

func (rec *recorder) write(name string, data any) {
	if err := binary.Write(rec.files[name], binary.LittleEndian, data); err != nil {
		panic(err)
	}
}

func (rec *recorder) close() error {
	for _, file := range rec.files {
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

// recordingRunner sits between battleai.NeuralCandidates and the real (or
// stub) runner and records each call of one actor stream.
type recordingRunner struct {
	inner  battleai.RecurrentLogitRunner
	rec    *recorder
	stream int32
}

func (runner *recordingRunner) RunActor(spatial, scalars []float32, legal []uint8) ([]float32, error) {
	return nil, fmt.Errorf("dump_observations needs a recurrent contract")
}

func (runner *recordingRunner) RunActorRecurrent(spatial, scalars []float32, legal []uint8, memory []float32, reset bool) ([]float32, []float32, error) {
	logits, next, err := runner.inner.RunActorRecurrent(spatial, scalars, legal, memory, reset)
	if err != nil {
		return nil, nil, err
	}
	rec := runner.rec
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.compare != nil {
		otherLogits, otherNext, err := rec.compare.runner.RunActorRecurrent(spatial, scalars, legal, memory, reset)
		if err != nil {
			return nil, nil, err
		}
		best, otherBest := 0, 0
		for index := range logits {
			if legal[index] != 0 {
				rec.compare.maxLogitDiff = math.Max(rec.compare.maxLogitDiff, math.Abs(float64(logits[index]-otherLogits[index])))
			} else if logits[index] != otherLogits[index] {
				rec.compare.maxLogitDiff = math.Inf(1)
			}
			if logits[index] > logits[best] {
				best = index
			}
			if otherLogits[index] > otherLogits[otherBest] {
				otherBest = index
			}
		}
		if best != otherBest {
			rec.compare.argmaxMismatch++
		}
		for index := range next {
			rec.compare.maxMemoryDiff = math.Max(rec.compare.maxMemoryDiff, math.Abs(float64(next[index]-otherNext[index])))
		}
	}
	resetByte := uint8(0)
	if reset {
		resetByte = 1
	}
	rec.write("spatial.f32", spatial)
	rec.write("scalars.f32", scalars)
	rec.write("legal.u8", legal)
	rec.write("memory.f32", memory)
	rec.write("reset.u8", resetByte)
	rec.write("stream.i32", runner.stream)
	rec.write("logits.f32", logits)
	rec.write("next_memory.f32", next)
	rec.count++
	return logits, next, nil
}

// stubRunner returns random logits so actors wander without a model.
type stubRunner struct {
	mu     sync.Mutex
	random *rand.Rand
	hidden int
}

func (runner *stubRunner) RunActorRecurrent(_, _ []float32, legal []uint8, _ []float32, _ bool) ([]float32, []float32, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	logits := make([]float32, len(legal))
	for index := range logits {
		logits[index] = float32(runner.random.NormFloat64())
		if index >= 5 && index < 10 {
			logits[index] -= 1.5 // fewer bubbles than moves
		}
		if legal[index] == 0 {
			logits[index] = -math.MaxFloat32
		}
	}
	return logits, make([]float32, runner.hidden), nil
}

// cadencePolicy mirrors the server's decision cadence
// (internal/server/probe competitiveAIDecisionCadencePolicy): decide every
// 100 ms, hold only the movement key in between, decide at once when the
// engine asks for it.
type cadencePolicy struct {
	base        battleengine.Policy
	steps       int
	remaining   int
	cached      battleengine.Action
	initialized bool
}

func (policy *cadencePolicy) ChooseAction(battleengine.Observation, []battleengine.Action) (battleengine.Action, error) {
	return battleengine.Action{}, fmt.Errorf("cadence policy needs an engine snapshot")
}

func (policy *cadencePolicy) ChooseActionWithSnapshot(snapshot *battleengine.Engine, observation battleengine.Observation, legal []battleengine.Action) (battleengine.Action, error) {
	held := policy.cached
	held.PlaceBomb = false
	held.UseActionID = 0
	allowed := false
	for _, candidate := range legal {
		if candidate == held {
			allowed = true
			break
		}
	}
	if policy.initialized && policy.remaining > 0 && allowed && !snapshot.NeedsImmediatePolicyDecision(observation.PlayerID) {
		policy.remaining--
		return held, nil
	}
	chosen, err := policy.base.(battleengine.SnapshotPolicy).ChooseActionWithSnapshot(snapshot, observation, legal)
	if err != nil {
		return battleengine.Action{}, err
	}
	policy.cached, policy.initialized, policy.remaining = chosen, true, policy.steps-1
	return chosen, nil
}

type episodeMeta struct {
	MapID   uint32 `json:"map_id"`
	MapName string `json:"map_name"`
	Players int    `json:"players"`
	Ticks   int    `json:"ticks"`
	Ended   bool   `json:"ended"`
	Streams []int  `json:"streams"`
}

func main() {
	clientRoot := flag.String("client-root", "", "client directory (with data/ and map files)")
	out := flag.String("out", "", "output directory")
	episodes := flag.Int("episodes", 4, "number of battles")
	maxSeconds := flag.Int("seconds", 60, "maximum battle seconds per episode")
	players := flag.Int("players", 4, "participants per battle (2 teams)")
	seed := flag.Int64("seed", 1, "random seed")
	metadataPath := flag.String("metadata", "configs/models/qqtang-rule1.onnx.json", "model metadata (contract)")
	modelPath := flag.String("model", "", "ONNX model to drive the actors (needs -tags onnxruntime)")
	comparePath := flag.String("compare", "", "second ONNX model evaluated on the same inputs (needs -model)")
	ortPath := flag.String("ort", "", "ONNX Runtime shared library")
	flag.Parse()
	if *clientRoot == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*clientRoot, *out, *episodes, *maxSeconds, *players, *seed, *metadataPath, *modelPath, *comparePath, *ortPath); err != nil {
		fmt.Fprintln(os.Stderr, "dump_observations:", err)
		os.Exit(1)
	}
}

func run(clientRoot, out string, episodes, maxSeconds, players int, seed int64, metadataPath, modelPath, comparePath, ortPath string) error {
	random := rand.New(rand.NewSource(seed))
	catalog, err := mapdata.LoadCatalog(clientRoot)
	if err != nil {
		return err
	}
	var maps []mapdata.CompetitiveMap
	for _, id := range catalog.CompetitiveIDs() {
		entry, _ := catalog.CompetitiveMap(id)
		if int(entry.PlayerLimit) >= players && entry.RequiredItemField == 0 && entry.UsesOrdinaryElimination() {
			maps = append(maps, entry)
		}
	}
	if len(maps) == 0 {
		return fmt.Errorf("no ordinary map for %d players", players)
	}
	sort.Slice(maps, func(i, j int) bool { return maps[i].ID < maps[j].ID })

	contractModel := modelPath
	if contractModel == "" {
		contractModel = metadataPath // LoadContract only needs a non-empty file here
	}
	contract, err := battleai.LoadContract(contractModel, metadataPath)
	if err != nil {
		return err
	}
	var inner battleai.RecurrentLogitRunner = &stubRunner{random: rand.New(rand.NewSource(seed + 1)), hidden: contract.RecurrentHiddenSize}
	rec, err := newRecorder(out)
	if err != nil {
		return err
	}
	if modelPath != "" {
		inner, err = loadRunner(modelPath, metadataPath, ortPath)
		if err != nil {
			return err
		}
		if comparePath != "" {
			other, err := loadRunner(comparePath, comparePath+".json", ortPath)
			if err != nil {
				return err
			}
			rec.compare = &comparison{runner: other}
		}
	}

	var metas []episodeMeta
	stream := int32(0)
	roles := []uint16{1, 2, 3, 4, 5, 6, 7, 8}
	for episode := 0; episode < episodes; episode++ {
		entry := maps[random.Intn(len(maps))]
		participants := make([]battleengine.NativeRuntimeParticipant, players)
		policies := map[uint16]battleengine.Policy{}
		meta := episodeMeta{MapID: entry.ID, MapName: entry.Name, Players: players}
		for index := range participants {
			playerID := uint16(index + 1)
			participants[index] = battleengine.NativeRuntimeParticipant{
				PlayerID: playerID, RoleID: roles[random.Intn(len(roles))], TeamID: byte(index%2 + 1),
				Source: battleengine.ParticipantVirtualAI,
			}
			candidates := &battleai.NeuralCandidates{
				Contract: contract, Runner: &recordingRunner{inner: inner, rec: rec, stream: stream},
				DangerHorizonMS: 3_500, DecisionMS: decisionMS,
			}
			meta.Streams = append(meta.Streams, int(stream))
			stream++
			var base battleengine.Policy = battleai.SampledCandidatePolicy{
				Candidate: candidates, MinP: 0.1, Temperature: 1, Rand: battleai.NewCandidateRand(uint64(random.Int63())),
			}
			if modelPath == "" {
				base = battleengine.GreedyCandidatePolicy{Candidate: candidates}
			}
			policies[playerID] = &cadencePolicy{base: base, steps: decisionMS / 20}
		}
		runtime, err := battleengine.NewRuntimeFromCompetitiveMap(entry, battleengine.CompetitiveRuntimeOptions{
			NativeActionInventory: true,
			SimulationSeed:        uint64(random.Int63()),
			SpawnSeed:             random.Uint32(), ItemSeed: random.Uint32(),
			SpawnMode: battleengine.NativeSpawnModeForMap(entry, false),
			TickMS:    20, VirtualTrapDurationMS: 6_000,
			Participants: participants, Policies: policies,
		})
		if err != nil {
			return fmt.Errorf("map %d: %w", entry.ID, err)
		}
		for tick := 0; tick < maxSeconds*50; tick++ {
			if _, err := runtime.Step(nil); err != nil {
				return fmt.Errorf("map %d tick %d: %w", entry.ID, tick, err)
			}
			meta.Ticks = tick + 1
			if tick%5 == 4 && runtime.EngineSnapshot().Terminal().Ended {
				meta.Ended = true
				break
			}
		}
		metas = append(metas, meta)
		fmt.Fprintf(os.Stderr, "episode %d: map %d %q, %d ticks, ended=%v, records so far %d\n", episode, entry.ID, entry.Name, meta.Ticks, meta.Ended, rec.count)
	}
	if err := rec.close(); err != nil {
		return err
	}
	summary := map[string]any{
		"records": rec.count, "channels": contract.Channels, "height": contract.Height, "width": contract.Width,
		"scalars": contract.Scalars, "actions": contract.Actions, "hidden": contract.RecurrentHiddenSize,
		"model": modelPath, "episodes": metas,
	}
	if rec.compare != nil {
		summary["compare"] = map[string]any{
			"model": comparePath, "max_logit_diff": rec.compare.maxLogitDiff,
			"max_memory_diff": rec.compare.maxMemoryDiff, "argmax_mismatches": rec.compare.argmaxMismatch,
		}
		fmt.Fprintf(os.Stderr, "compare %s: max legal logit diff %g, max memory diff %g, argmax mismatches %d/%d\n",
			comparePath, rec.compare.maxLogitDiff, rec.compare.maxMemoryDiff, rec.compare.argmaxMismatch, rec.count)
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "meta.json"), append(encoded, '\n'), 0o644)
}
