#!/usr/bin/env bash
# 场景 2 启动脚本：agent 模式 —— 上报 + 配置同步 + 领取两种任务派发（backfill / sql）。
#
# 用法：
#   ./start.sh                                  # instanceID 取自配置文件
#   ./start.sh --instanceID node-2              # CLI flag 覆盖
#   TANGO_AGENT_INSTANCEID=node-3 ./start.sh    # 环境变量覆盖
#   TANGO_COMMON_MONGO_URI=mongodb://user:pass@host/db ./start.sh
#
# 注意：agent 模式下 instanceID 必填，否则启动校验失败。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
CONFIG="$SCRIPT_DIR/daemon.yaml"

: "${TANGO_COMMON_MONGO_URI:=}"
# 若想用主机名作为实例 ID，取消下一行注释：
# export TANGO_AGENT_INSTANCEID="${TANGO_AGENT_INSTANCEID:-$(hostname)}"

cd "$REPO_ROOT"
exec go run ./cmd/tango daemon agent --config "$CONFIG" "$@"
