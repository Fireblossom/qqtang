#!/bin/sh
# Run on the Mac: build the Linux worker and copy everything the trainer needs
# to the 42 cluster's persistent storage (/mlde), reached with `ssh 42`.
#
#   training/scripts/push_to_cluster.sh            # code + worker binary + client data
#   SKIP_BUILD=1 training/scripts/push_to_cluster.sh
#   CLIENT=/path/to/QQTang-Local training/scripts/push_to_cluster.sh
#
# Layout on the cluster (DEST, default /mlde/qqtang-rl):
#   $DEST/repo/training/...                     trainer code, configs, scripts
#   $DEST/repo/training/bin/qqt-train-worker-linux-amd64
#   $DEST/repo/configs/models/qqtang-rule1.onnx(.json)   frozen U2392 baseline
#   $DEST/qqtang-local/runtime/client-patched/{map,config,object/mapElem}
#   $DEST/qqtang-local/data/qqt_competitive_map_rules.json
# (the worker resolves data/ as <client-root>/../../data). Runs, checkpoints
# and logs go to $DEST/runs (written by the trainer, not copied from here).
set -eu
HOST=${HOST:-42}
DEST=${DEST:-/mlde/qqtang-rl}
CLIENT=${CLIENT:-$HOME/Downloads/QQTang-Local-v1.3.4/QQTang-Local}
root=$(cd "$(dirname "$0")/../.." && pwd)

if [ "${SKIP_BUILD:-0}" != 1 ]; then
	GO=${GO:-$(command -v go || echo /Users/cduan/sdk/go1.26.10/bin/go)} \
		GOTOOLCHAIN=local "$root/training/tools/build-worker.sh"
fi
test -x "$root/training/bin/qqt-train-worker-linux-amd64" || {
	echo "missing training/bin/qqt-train-worker-linux-amd64 (run training/tools/build-worker.sh)" >&2
	exit 1
}
test -d "$CLIENT/runtime/client-patched/map" || {
	echo "client data not found under $CLIENT (set CLIENT=...)" >&2
	exit 1
}

ssh "$HOST" "mkdir -p '$DEST/repo/configs/models' '$DEST/qqtang-local/runtime/client-patched/object' '$DEST/qqtang-local/data' '$DEST/runs'"

# Code: training/ without local artefacts; the worker binary is included.
rsync -az --delete \
	--exclude '.venv/' --exclude '__pycache__/' --exclude 'runs/' --exclude '.pytest_cache/' \
	--exclude 'bin/qqt-train-worker-darwin-*' \
	"$root/training/" "$HOST:$DEST/repo/training/"
rsync -az "$root/configs/models/" "$HOST:$DEST/repo/configs/models/"

# Client data the worker reads (map catalog, map files, element table, configs).
c="$CLIENT/runtime/client-patched"
rsync -az "$c/map" "$c/config" "$HOST:$DEST/qqtang-local/runtime/client-patched/"
rsync -az "$c/object/mapElem" "$HOST:$DEST/qqtang-local/runtime/client-patched/object/"
if [ -f "$CLIENT/data/qqt_competitive_map_rules.json" ]; then
	rsync -az "$CLIENT/data/qqt_competitive_map_rules.json" "$HOST:$DEST/qqtang-local/data/"
fi
echo "pushed to $HOST:$DEST; now: ssh $HOST 'bash $DEST/repo/training/scripts/cluster_run.sh'"
