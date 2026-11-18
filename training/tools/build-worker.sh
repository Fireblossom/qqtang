#!/bin/sh
# Builds qqt-train-worker for this Mac and as a static linux/amd64 binary for
# the GPU cluster (no Go toolchain needed there):
#   training/bin/qqt-train-worker-darwin-arm64   (or -darwin-amd64)
#   training/bin/qqt-train-worker-linux-amd64    (CGO_ENABLED=0, static)
# Usage: training/tools/build-worker.sh   (GO=/path/to/go to pick a toolchain)
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
out="$root/training/bin"
go=${GO:-go}
mkdir -p "$out"
cd "$root"
host_arch=$("$go" env GOARCH)
echo "building darwin/$host_arch"
CGO_ENABLED=0 GOOS=darwin GOARCH="$host_arch" "$go" build -trimpath -ldflags "-s -w" \
	-o "$out/qqt-train-worker-darwin-$host_arch" ./cmd/qqt-train-worker
echo "building linux/amd64 (static)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$go" build -trimpath -ldflags "-s -w" \
	-o "$out/qqt-train-worker-linux-amd64" ./cmd/qqt-train-worker
ls -l "$out"
