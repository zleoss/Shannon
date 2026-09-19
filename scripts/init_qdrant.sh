#!/usr/bin/env bash
# =============================================================================
# 文件: scripts/init_qdrant.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 初始化 Qdrant 集合的包装脚本
# 【关键内容】 调用共享迁移脚本初始化 Qdrant 集合
#             设计在 qdrant-init Docker 服务内运行，也支持本地执行
# 【协作关系】 被 docker-compose qdrant-init 服务调用；依赖 Qdrant 实例
# =============================================================================
set -euo pipefail

# Wrapper to initialize Qdrant collections using the shared migrations script.
# Designed to run inside the qdrant-init Docker service, but also works locally.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
HOST_PY_SCRIPT="$REPO_ROOT/migrations/qdrant/create_collections.py"
CONTAINER_PY_SCRIPT="/app/migrations/qdrant/create_collections.py"

if [[ -f "$CONTAINER_PY_SCRIPT" ]]; then
  TARGET_SCRIPT="$CONTAINER_PY_SCRIPT"
elif [[ -f "$HOST_PY_SCRIPT" ]]; then
  TARGET_SCRIPT="$HOST_PY_SCRIPT"
else
  echo "Unable to locate create_collections.py for Qdrant initialization" >&2
  exit 1
fi

exec python "$TARGET_SCRIPT"
