#!/bin/sh
# Decompile and convert the original client's UI scripts to Python 3.
# Usage: prepare-ui.sh <client-root> [output-dir]
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CLIENT_ROOT=${1:?usage: prepare-ui.sh <client-root> [output-dir]}
OUTPUT=${2:-"$HERE/../generated/scripts"}

rm -rf "$OUTPUT"
exec uv run --system-certs --no-project --python 3.12 \
    --with uncompyle6==3.9.3 --with xdis==6.1.7 --with spark-parser==1.9.0 \
    python -I "$HERE/prepare_ui.py" --client-root "$CLIENT_ROOT" --output "$OUTPUT"
