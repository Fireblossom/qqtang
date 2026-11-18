//go:build !onnxruntime || !cgo

package main

import (
	"fmt"

	"qqtang/internal/game/battleai"
)

func loadRunner(string, string, string) (battleai.RecurrentLogitRunner, error) {
	return nil, fmt.Errorf("-model needs a build with -tags onnxruntime and cgo")
}
