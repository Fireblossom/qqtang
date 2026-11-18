#!/bin/bash
# Run INSIDE the 42 cluster container (ssh 42), after push_to_cluster.sh.
# Sets up the uv environment on /mlde and starts (or resumes) training in a
# detached tmux session (nohup fallback). Safe to re-run after the container
# was reclaimed: the run resumes from its latest checkpoint (run.resume=auto).
#
#   bash /mlde/qqtang-rl/repo/training/scripts/cluster_run.sh                # configs/h100.toml, run "h100"
#   RUN=h100-b CONFIG=configs/h100.toml bash .../cluster_run.sh --set ppo.lr=1e-4
#   bash .../cluster_run.sh --status      # session, GPU, last log lines
#   bash .../cluster_run.sh --stop        # SIGTERM: finish the update, checkpoint, exit
#
# Everything persistent lives on /mlde (container-local disk is lost on reclaim):
#   $DEST/runs/$RUN/{train.log,metrics.jsonl,eval.jsonl,checkpoints/,league/,export/,workers/}
#   $DEST/runs/$RUN/stdout.log   (process output)
#   $DEST/venv                   (uv project environment), $DEST/uv-cache
set -euo pipefail
DEST=${DEST:-/mlde/qqtang-rl}
RUN=${RUN:-h100}
CONFIG=${CONFIG:-configs/h100.toml}
SESSION=qqtrain-$RUN
REPO=$DEST/repo/training
RUN_DIR=$DEST/runs/$RUN
export UV_CACHE_DIR=${UV_CACHE_DIR:-$DEST/uv-cache}
export UV_PROJECT_ENVIRONMENT=$DEST/venv
export PATH=$HOME/.local/bin:$PATH

case "${1:-}" in
--status)
	tmux has-session -t "$SESSION" 2>/dev/null && echo "tmux session $SESSION running" || echo "no tmux session $SESSION"
	pgrep -af "qqtrain.train" || true
	nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total --format=csv || true
	tail -n 5 "$RUN_DIR/train.log" 2>/dev/null || true
	exit 0
	;;
--stop)
	pkill -TERM -f "qqtrain.train --config" && echo "sent SIGTERM; training checkpoints and exits after the current update" || echo "not running"
	exit 0
	;;
esac

if ! command -v uv >/dev/null 2>&1; then
	echo "installing uv into ~/.local/bin"
	curl -LsSf https://astral.sh/uv/install.sh | sh
fi
cd "$REPO"
uv sync --frozen 2>/dev/null || uv sync
chmod +x bin/qqt-train-worker-linux-amd64

if pgrep -f "qqtrain.train --config" >/dev/null; then
	echo "training already running:"; pgrep -af "qqtrain.train --config"; exit 1
fi

# One worker process per core, leaving 4 cores for the trainer process; the
# config's value wins when it asks for fewer.
cores=$(nproc)
want=$(( cores > 8 ? cores - 4 : 4 ))
cfg_workers=$(uv run python - "$CONFIG" <<'EOF'
import sys, tomllib
print(tomllib.load(open(sys.argv[1], "rb")).get("worker", {}).get("num_workers", 0))
EOF
)
workers=$(( cfg_workers > 0 && cfg_workers < want ? cfg_workers : want ))
echo "cores=$cores -> worker.num_workers=$workers; run dir $RUN_DIR"
mkdir -p "$RUN_DIR"

cmd=(uv run python -m qqtrain.train --config "$CONFIG"
	--set "run.dir=\"$RUN_DIR\""
	--set "worker.num_workers=$workers"
	--set "worker.command=[\"$REPO/bin/qqt-train-worker-linux-amd64\"]"
	--set "worker.client_root=\"$DEST/qqtang-local/runtime/client-patched\""
	"$@")
printf '%q ' "${cmd[@]}" >"$RUN_DIR/command.txt"; echo >>"$RUN_DIR/command.txt"

if command -v tmux >/dev/null 2>&1; then
	tmux new-session -d -s "$SESSION" -c "$REPO" \
		"$(printf '%q ' "${cmd[@]}") >>'$RUN_DIR/stdout.log' 2>&1"
	echo "started in tmux session $SESSION (tmux attach -t $SESSION); log: $RUN_DIR/train.log"
else
	nohup "${cmd[@]}" >>"$RUN_DIR/stdout.log" 2>&1 </dev/null &
	echo "started with nohup (pid $!); log: $RUN_DIR/train.log"
fi
