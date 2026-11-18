#!/usr/bin/env bash
# Load an exported actor through the server's Go ONNX Runtime path
# (internal/game/battleai, build tag onnxruntime) and run its contract tests.
#
#   training/tools/check-onnx-go.sh MODEL.onnx [METADATA.json] [LIBONNXRUNTIME]
#
# METADATA defaults to MODEL.onnx.json. The runtime library defaults to
# $QQTANG_ONNXRUNTIME_LIB or the macOS arm64 library of the local release.
# Needs Go with cgo (GOTOOLCHAIN=local; set PATH to your Go first).
set -euo pipefail

model=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
metadata=${2:-$model.json}
runtime=${3:-${QQTANG_ONNXRUNTIME_LIB:-$HOME/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/onnxruntime/darwin-arm64/libonnxruntime.1.29.0.dylib}}
repo=$(cd "$(dirname "$0")/../.." && pwd)

cd "$repo"
QQTANG_TEST_ONNX_MODEL=$model \
QQTANG_TEST_ONNX_METADATA=$metadata \
QQTANG_TEST_ONNX_RUNTIME=$runtime \
CGO_ENABLED=1 GOTOOLCHAIN=${GOTOOLCHAIN:-local} \
	go test -count=1 -tags onnxruntime -run 'ONNXRuntime' -v ./internal/game/battleai/
