#!/usr/bin/env bash
# 场景 1 启动脚本：standalone 模式 —— 纯上报，不接受 MongoDB 中心指挥。
#
# 用法：
#   ./start.sh
#   TANGO_COMMON_MONGO_URI=mongodb://user:pass@host/db ./start.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
CONFIG="$SCRIPT_DIR/daemon.yaml"

: "${TANGO_COMMON_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run ./cmd/tango daemon standalone --config "$CONFIG" "$@"
