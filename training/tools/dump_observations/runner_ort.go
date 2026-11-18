//go:build onnxruntime && cgo

package main

import "qqtang/internal/game/battleai"

func loadRunner(model, metadata, library string) (battleai.RecurrentLogitRunner, error) {
	return battleai.LoadONNXRuntimeRunner(model, metadata, library, battleai.ONNXRuntimeConfig{IntraOpThreads: 1, InterOpThreads: 1})
}
