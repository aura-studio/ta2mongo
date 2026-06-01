#!/usr/bin/env bash
# cluster 模式启动脚本：上报 + 从 MongoDB 控制面文档同步并热重载上报 filter。
#
# 用法：
#   ./start.sh
#   TANGO_GENERIC_MONGO_URI=mongodb://user:pass@host/db ./start.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CONFIG="$SCRIPT_DIR/cluster.max.yaml"

: "${TANGO_GENERIC_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run . daemon cluster --config "$CONFIG" "$@"
