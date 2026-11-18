//go:build onnxruntime && cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// offlineONNXOptions needs the client data and the ONNX Runtime library
// (QQTANG_TEST_ONNX_RUNTIME, as battleai's tests); the model defaults to
// the repository's configs/models/qqtang-rule1.onnx.
func offlineONNXOptions(t *testing.T) (string, offlineInit) {
	t.Helper()
	root := offlineClientRoot(t)
	library := os.Getenv("QQTANG_TEST_ONNX_RUNTIME")
	if library == "" {
		t.Skip("QQTANG_TEST_ONNX_RUNTIME is not set")
	}
	model := os.Getenv("QQTANG_TEST_ONNX_MODEL")
	if model == "" {
		model, _ = filepath.Abs(filepath.Join("..", "..", "configs", "models", "qqtang-rule1.onnx"))
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model %s: %v", model, err)
	}
	return root, offlineInit{Policy: offlinePolicyONNX, Model: model, Library: library}
}

func TestOfflineONNXPlaysRuleOneAndWrestle(t *testing.T) {
	root, options := offlineONNXOptions(t)
	for _, mapID := range []uint32{1, 1003} {
		options := options
		options.MapID, options.Players, options.SpawnSeed, options.ItemSeed = mapID, offlineTeams(2, 1, 2), 21, 22
		hub, over := offlineRun(t, root, options, 400_000)
		for _, name := range hub.policyName {
			if !strings.HasPrefix(name, "onnx") {
				t.Fatalf("map %d: seat policy %q, want the model", mapID, name)
			}
		}
		t.Logf("map %d ended at %d ms: %+v", mapID, hub.link.clock, over.Results)
	}
}

// TestOfflineONNXAgainstBot: two model seats against two bot seats on map 1.
func TestOfflineONNXAgainstBot(t *testing.T) {
	root, options := offlineONNXOptions(t)
	options.MapID, options.Players, options.SpawnSeed, options.ItemSeed = 1, offlineTeams(2, 1, 2), 31, 32
	options.Policy = offlinePolicyAuto
	for index := range options.Players {
		if options.Players[index].Team == 2 {
			options.Players[index].Policy = offlinePolicyBot
		}
	}
	hub, over := offlineRun(t, root, options, 400_000)
	if !strings.HasPrefix(hub.policyName[1], "onnx") || hub.policyName[3] != "bot" {
		t.Fatalf("policies %+v", hub.policyName)
	}
	t.Logf("ended at %d ms: %+v stats %+v %+v", hub.link.clock, over.Results, *hub.referee.stats[1], *hub.referee.stats[2])
}
