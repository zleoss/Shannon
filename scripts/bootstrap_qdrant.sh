#!/usr/bin/env bash
# =============================================================================
# 文件: scripts/bootstrap_qdrant.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 引导初始化 Qdrant 向量数据库的基础配置
# 【关键内容】 设置 QDRANT_URL 默认值；创建必要的集合与索引
#             用于开发环境首次启动 Qdrant
# 【协作关系】 被 docker-compose 启动流程调用；依赖 Qdrant 实例运行
# =============================================================================
set -euo pipefail

QDRANT_URL="http://localhost:6333"

echo "Bootstrapping Qdrant collections (if needed)..."

create_collection() {
  local name="$1"
  curl -fsS -X PUT "$QDRANT_URL/collections/$name" \
    -H 'Content-Type: application/json' \
    -d '{
      "vectors": {"size": 1536, "distance": "Cosine"},
      "on_disk_payload": true
    }' >/dev/null && echo " - ensured collection: $name"
}

create_collection "tool_results"
create_collection "cases"

echo "Qdrant bootstrap complete."

