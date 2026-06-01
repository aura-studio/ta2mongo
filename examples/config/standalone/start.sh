#!/usr/bin/env bash
# standalone 模式启动脚本：纯上报，不接受 MongoDB 中心指挥。
#
# 用法：
#   ./start.sh
#   TANGO_COMMON_MONGO_URI=mongodb://user:pass@host/db ./start.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CONFIG="$SCRIPT_DIR/standalone.yaml"

: "${TANGO_COMMON_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run . daemon standalone --config "$CONFIG" "$@"
