#!/usr/bin/env bash
# gateway 服务启动脚本：常驻 HTTP/REST 接入层。
#
# 用法：
#   ./start.sh
#   ./start.sh --role.gateway.addr :9090                    # 覆盖监听地址
#   TANGO_DAO_MONGO_URI=mongodb://user:pass@host/db ./start.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CONFIG="$SCRIPT_DIR/gateway.max.yaml"

: "${TANGO_DAO_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run . gateway --config "$CONFIG" "$@"
