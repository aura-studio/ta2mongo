#!/usr/bin/env bash
# operator 一次性操作示例脚本：默认演示 ingest（从参数或 stdin 读取 JSON 行）。
#
# 用法：
#   ./start.sh '{"#type":"track","#event_name":"login","#distinct_id":"u1"}'
#   cat events.ndjson | ./start.sh
#   TANGO_RUNTIME_MONGO_URI=mongodb://user:pass@host/db ./start.sh '...'
#
# 其他子命令直接手动运行，例如：
#   go run . operator backfill --config operator.max.yaml
#   go run . operator sql      --config operator.max.yaml 'SELECT ...'
#   go run . operator publish report-sync --config operator.max.yaml --include '#type == "track"'
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CONFIG="$SCRIPT_DIR/operator.max.yaml"

: "${TANGO_RUNTIME_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run . operator ingest --config "$CONFIG" "$@"
