#!/usr/bin/env bash
# standalone 服务启动脚本：常驻上报，追尾日志写入 MongoDB。
#
# 用法：
#   ./start.sh
#   TANGO_RUNTIME_MONGO_URI=mongodb://user:pass@host/db ./start.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CONFIG="$SCRIPT_DIR/standalone.max.yaml"

: "${TANGO_RUNTIME_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run . standalone --config "$CONFIG" "$@"
