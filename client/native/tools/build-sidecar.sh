#!/bin/sh
# Build the battle rules sidecar (cmd/qqt-battle-sidecar) for the native client.
# Usage: build-sidecar.sh [output]   (needs Go; default output generated/bin/qqt-battle-sidecar)
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO=$(CDPATH= cd -- "$HERE/../../.." && pwd)
OUTPUT=${1:-"$HERE/../generated/bin/qqt-battle-sidecar"}

mkdir -p "$(dirname -- "$OUTPUT")"
cd "$REPO"
exec go build -o "$OUTPUT" ./cmd/qqt-battle-sidecar
