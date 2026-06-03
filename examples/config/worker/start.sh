#!/usr/bin/env bash
# worker 服务启动脚本：领取并执行 report-sync / backfill / sql 任务。
#
# 用法：
#   ./start.sh                                   # instanceID 取自配置文件
#   ./start.sh --instanceID worker-2             # CLI flag 覆盖
#   TANGO_TASKS_INSTANCEID=worker-3 ./start.sh   # 环境变量覆盖
#   TANGO_RUNTIME_MONGO_URI=mongodb://user:pass@host/db ./start.sh
#
# 注意：worker 的 tasks.instanceID 必填，否则启动校验失败。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
CONFIG="$SCRIPT_DIR/worker.max.yaml"

: "${TANGO_RUNTIME_MONGO_URI:=}"

cd "$REPO_ROOT"
exec go run . worker run --config "$CONFIG" "$@"
