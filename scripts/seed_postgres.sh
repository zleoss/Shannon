#!/usr/bin/env bash
# =============================================================================
# 文件: scripts/seed_postgres.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 向 PostgreSQL 数据库注入种子测试数据
# 【关键内容】 使用 docker-compose 执行 seed_data.sql；支持自定义 COMPOSE_FILE
#             用于开发环境与 CI 测试的数据初始化
# 【协作关系】 被 make seed 调用；依赖 Postgres 容器与 seed_data.sql 文件
# =============================================================================
set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose/docker-compose.yml}"
SEED_FILE="${SEED_FILE:-tests/fixtures/seed_data.sql}"
PGUSER_ENV="${POSTGRES_USER:-shannon}"

if [ ! -f "$SEED_FILE" ]; then
  echo "No seed file at $SEED_FILE (skipping)"
  exit 0
fi

echo "Seeding Postgres with $SEED_FILE..."
docker compose -f "$COMPOSE_FILE" cp "$SEED_FILE" postgres:/seed_data.sql
docker compose -f "$COMPOSE_FILE" exec -T postgres psql -U "$PGUSER_ENV" -d "$POSTGRES_DB" -f /seed_data.sql
echo "Postgres seed complete."
