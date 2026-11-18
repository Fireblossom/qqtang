#!/bin/sh
# Build the battle rules sidecar (cmd/qqt-battle-sidecar) for the native client.
# Usage: build-sidecar.sh [output]   (needs Go; default output generated/bin/qqt-battle-sidecar)
#
# Offline matches (offline.go) can let the rule-1 model (configs/models/
# qqtang-rule1.onnx) play the AI seats of rules 1 and 4. That needs cgo, the
# onnxruntime build tag and the ONNX Runtime 1.29 library. The script looks
# for the library in QQT_ONNXRUNTIME_LIB, then in the release layouts below;
# when found it builds with -tags onnxruntime and bakes the library and model
# paths in as defaults. Set QQT_SIDECAR_ONNX=0 to build without it; the AI
# seats then use the bot on every rule.
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO=$(CDPATH= cd -- "$HERE/../../.." && pwd)
OUTPUT=${1:-"$HERE/../generated/bin/qqt-battle-sidecar"}

mkdir -p "$(dirname -- "$OUTPUT")"
cd "$REPO"

LIB=${QQT_ONNXRUNTIME_LIB:-}
if [ -z "$LIB" ] && [ "$(uname -s)" = Darwin ]; then
	for candidate in \
		"$REPO/runtime/onnxruntime/darwin-arm64/libonnxruntime.1.29.0.dylib" \
		"$HOME/Downloads/QQTang-Local-v1.3.4/QQTang-Local/runtime/onnxruntime/darwin-arm64/libonnxruntime.1.29.0.dylib" \
		"$HOME/QQTang-Local/runtime/onnxruntime/darwin-arm64/libonnxruntime.1.29.0.dylib"; do
		if [ -f "$candidate" ]; then
			LIB=$candidate
			break
		fi
	done
fi
MODEL="$REPO/configs/models/qqtang-rule1.onnx"
DEFAULTS="-X main.defaultAdventureRules=$REPO/configs/adventure-rules.json"
if [ "${QQT_SIDECAR_ONNX:-1}" != 0 ] && [ -n "$LIB" ] && [ -f "$LIB" ] && [ -f "$MODEL" ]; then
	echo "building with ONNX Runtime: $LIB" >&2
	exec env CGO_ENABLED=1 go build -tags onnxruntime \
		-ldflags "$DEFAULTS -X main.defaultONNXLibrary=$LIB -X main.defaultONNXModel=$MODEL" \
		-o "$OUTPUT" ./cmd/qqt-battle-sidecar
fi
exec go build -ldflags "$DEFAULTS" -o "$OUTPUT" ./cmd/qqt-battle-sidecar
