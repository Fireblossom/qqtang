// Command qqt-train-worker runs many headless rule-1 battles for
// reinforcement learning and speaks the binary stdin/stdout protocol of
// training/README.md ("Worker protocol"): one JSON hello line, then framed
// RESET / STEP / CLOSE requests, each answered with the observation tensors
// (encoded exactly as the server's live inference encodes them), legal
// masks, flags, rewards and finished-episode info. Logs go to stderr.
//
//	qqt-train-worker -client-root <dir> -config <json file>
//
// With -bench N the worker instead plays N steps with uniformly random legal
// actions in-process (responses are encoded and discarded) and prints the
// throughput to stderr.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"runtime/pprof"
	"time"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("qqt-train-worker: ")
	clientRoot := flag.String("client-root", "", "patched QQTang client directory (map data)")
	configPath := flag.String("config", "", "worker configuration JSON (see config.go); empty = defaults")
	bench := flag.Int("bench", 0, "play N random steps in-process and report throughput instead of serving")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile of the run to this file")
	flag.Parse()
	if os.Getenv("GOGC") == "" {
		// The engine allocates short-lived snapshots and forecasts on every
		// decision; a larger GC target is worth ~20% throughput here.
		debug.SetGCPercent(400)
	}
	if *cpuProfile != "" {
		file, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(file); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}
	if *clientRoot == "" {
		log.Fatal("-client-root is required")
	}
	config, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	maps, err := config.SelectMaps(*clientRoot)
	if err != nil {
		log.Fatal(err)
	}
	started := time.Now()
	worker, err := NewWorker(config, maps)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%d envs x %d agents, %d rule-%d maps, %s spatial, %d bytes/response (+info), ready in %v",
		config.Envs, config.Agents, len(maps), config.Rule, config.SpatialDtype, worker.ResponseBytes(), time.Since(started).Round(time.Millisecond))
	if *bench > 0 {
		err = runBench(worker, *bench, config.Seed)
	} else {
		err = worker.Serve(os.Stdin, os.Stdout, len(maps))
	}
	if err != nil {
		pprof.StopCPUProfile()
		log.Fatal(err)
	}
}

// RandomLegalActions fills actions with a uniformly random legal action per
// active slot and -1 elsewhere.
func RandomLegalActions(worker *Worker, random *rand.Rand, actions []int16) {
	legal := worker.small[:worker.slots*actionCount]
	active := worker.small[worker.slots*actionCount : worker.slots*(actionCount+1)]
	for slot := range actions {
		actions[slot] = -1
		if active[slot] == 0 {
			continue
		}
		row := legal[slot*actionCount : (slot+1)*actionCount]
		count := 0
		for _, allowed := range row {
			count += int(allowed)
		}
		pick := random.IntN(count)
		for id, allowed := range row {
			if allowed == 0 {
				continue
			}
			if pick == 0 {
				actions[slot] = int16(id)
				break
			}
			pick--
		}
	}
}

func runBench(worker *Worker, steps int, seed uint64) error {
	random := rand.New(rand.NewPCG(seed, 0xbe7c4))
	if err := worker.Reset(); err != nil {
		return err
	}
	actions := make([]int16, worker.slots)
	var stepTime, encodeTime time.Duration
	episodes := 0
	started := time.Now()
	for step := 0; step < steps; step++ {
		RandomLegalActions(worker, random, actions)
		begin := time.Now()
		if err := worker.Step(actions); err != nil {
			return err
		}
		middle := time.Now()
		if err := worker.WriteResponse(io.Discard); err != nil {
			return err
		}
		stepTime += middle.Sub(begin)
		encodeTime += time.Since(middle)
		for _, done := range worker.dones {
			episodes += int(done)
		}
	}
	elapsed := time.Since(started)
	fmt.Fprintf(os.Stderr,
		"bench: %d steps, %d decisions in %v: %.0f decisions/s, %.1f steps/s, %.2f ms/step (env %.2f, encode %.2f), %d agent-episodes ended, %d bytes/step\n",
		steps, worker.Decisions, elapsed.Round(time.Millisecond), float64(worker.Decisions)/elapsed.Seconds(),
		float64(steps)/elapsed.Seconds(), float64(elapsed.Microseconds())/1000/float64(steps),
		float64(stepTime.Microseconds())/1000/float64(steps), float64(encodeTime.Microseconds())/1000/float64(steps),
		episodes, worker.ResponseBytes())
	return nil
}
