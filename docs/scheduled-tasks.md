# Scheduled Tasks

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Shannon 的定时任务系统——基于 Temporal Schedule API 实现 Cron 表达式触发的周期性任务执行。系统提供资源限制（每用户最多 50 个定时任务、最小间隔 60 分钟、每次执行最大预算 $10）、暂停/恢复/删除操作、执行历史与成本追踪以及多租户隔离。架构包含 Gateway → Schedule Manager → Temporal Schedule API → ScheduledTaskWorkflow → OrchestratorWorkflow 的完整链路。

### 章节导航
- **Features**: Cron 调度、资源限制、预算控制、历史追踪、暂停/恢复/删除、多租户隔离
- **Architecture**: Gateway → Schedule Manager → Temporal → ScheduledTaskWorkflow → OrchestratorWorkflow
- **Components**: Schedule Manager（业务逻辑+配额执行）、ScheduledTaskWorkflow（包装执行）、Schedule Activities（Temporal 活动）
- **Configuration**: 环境变量（SCHEDULE_MAX_PER_USER/SCHEDULE_MIN_INTERVAL_MINS/SCHEDULE_MAX_BUDGET_USD）
- **Database Tables**: scheduled_tasks（配置）和 scheduled_task_executions（执行历史）
- **API Reference**: 创建/查看/暂停/恢复/删除定时任务的 API 说明和示例

### 与 AI Agent 体系的关联
- Schedule Manager：`go/orchestrator/internal/schedules/manager.go`
- ScheduledTaskWorkflow：`go/orchestrator/internal/workflows/scheduled/`
- 相关活动：`go/orchestrator/internal/activities/schedule_activities.go`
- 数据库迁移：包含 scheduled_tasks 和 scheduled_task_executions 表

### 阅读建议
需要定时执行 AI 任务的开发者必读；重点关注 Architecture 和 API Reference 章节。

Shannon supports recurring task execution using Temporal's native Schedule API. Users can create cron-based schedules that automatically execute tasks at specified intervals.

## Features

- **Cron-based scheduling** with timezone support
- **Resource limits** to prevent abuse
- **Budget control** per execution
- **Execution history** with cost tracking
- **Pause/Resume/Delete** operations
- **Multi-tenant isolation** with user/tenant ownership

## Architecture

```
User → Gateway → Orchestrator gRPC → Schedule Manager → Temporal Schedule API
                                                             ↓
                                           ScheduledTaskWorkflow (wrapper)
                                                             ↓
                                           OrchestratorWorkflow (existing)
```

### Components

- **Schedule Manager** (`internal/schedules/manager.go`): Business logic, Temporal API integration, resource limit enforcement
- **ScheduledTaskWorkflow** (`internal/workflows/scheduled/`): Wrapper workflow that tracks execution, enforces tenant quota, and delegates to existing workflows
- **Schedule Activities** (`internal/activities/schedule_activities.go`): Temporal activities including `PauseScheduleForQuota` for quota enforcement
- **Database Tables**:
  - `scheduled_tasks`: Schedule configuration (cron, query, budget, etc.)
  - `scheduled_task_executions`: Execution history with timestamps, status, cost

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SCHEDULE_MAX_PER_USER` | `50` | Maximum schedules per user |
| `SCHEDULE_MIN_INTERVAL_MINS` | `60` | Minimum interval between runs (minutes) |
| `SCHEDULE_MAX_BUDGET_USD` | `10.0` | Maximum budget per execution (USD) |

### Example

```bash
# Allow 100 schedules per user with $20 budget per run
SCHEDULE_MAX_PER_USER=100
SCHEDULE_MAX_BUDGET_USD=20.0
SCHEDULE_MIN_INTERVAL_MINS=30
```

## API Endpoints

All endpoints require authentication and enforce user/tenant ownership.

### Create Schedule

```bash
POST /api/v1/schedules
Content-Type: application/json
Authorization: Bearer <token>

{
  "name": "Daily summary",
  "description": "Generate daily activity summary",
  "cron_expression": "0 9 * * *",
  "timezone": "America/New_York",
  "task_query": "Summarize yesterday's activity",
  "task_context": {
    "report_format": "markdown"
  },
  "max_budget_per_run_usd": 5.0,
  "timeout_seconds": 600
}
```

**Response:**
```json
{
  "schedule_id": "550e8400-e29b-41d4-a716-446655440000",
  "message": "Schedule created successfully",
  "next_run_at": "2025-12-16T09:00:00-05:00"
}
```

### List Schedules

```bash
GET /api/v1/schedules?page=1&page_size=50&status=ACTIVE
Authorization: Bearer <token>
```

**Response:**
```json
{
  "schedules": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "Daily summary",
      "cron_expression": "0 9 * * *",
      "timezone": "America/New_York",
      "status": "ACTIVE",
      "next_run_at": "2025-12-16T09:00:00-05:00",
      "total_runs": 45,
      "successful_runs": 43,
      "failed_runs": 2
    }
  ],
  "total": 1,
  "page": 1,
  "page_size": 50
}
```

### Get Schedule

```bash
GET /api/v1/schedules/{id}
Authorization: Bearer <token>
```

### Update Schedule

```bash
PUT /api/v1/schedules/{id}
Content-Type: application/json
Authorization: Bearer <token>

{
  "cron_expression": "0 10 * * *",
  "max_budget_per_run_usd": 8.0
}
```

### Pause Schedule

```bash
POST /api/v1/schedules/{id}/pause
Content-Type: application/json
Authorization: Bearer <token>

{
  "reason": "Temporary maintenance"
}
```

### Resume Schedule

```bash
POST /api/v1/schedules/{id}/resume
Content-Type: application/json
Authorization: Bearer <token>

{
  "reason": "Maintenance complete"
}
```

### Delete Schedule

```bash
DELETE /api/v1/schedules/{id}
Authorization: Bearer <token>
```

## Cron Expression Format

Uses standard cron syntax (5 fields):

```
┌───────────── minute (0 - 59)
│ ┌───────────── hour (0 - 23)
│ │ ┌───────────── day of month (1 - 31)
│ │ │ ┌───────────── month (1 - 12)
│ │ │ │ ┌───────────── day of week (0 - 6) (Sunday to Saturday)
│ │ │ │ │
* * * * *
```

### Examples

| Expression | Description |
|------------|-------------|
| `0 9 * * *` | Daily at 9:00 AM |
| `0 */4 * * *` | Every 4 hours |
| `0 0 * * 1` | Every Monday at midnight |
| `30 8 1 * *` | First day of month at 8:30 AM |
| `0 12 * * 1-5` | Weekdays at noon |

## Database Schema

### scheduled_tasks

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `user_id` | UUID | Owner user ID |
| `tenant_id` | UUID | Tenant ID (nullable) |
| `name` | VARCHAR(255) | Schedule name |
| `description` | TEXT | Optional description |
| `cron_expression` | VARCHAR(100) | Cron schedule |
| `timezone` | VARCHAR(50) | IANA timezone (e.g., "America/New_York") |
| `task_query` | TEXT | Task query to execute |
| `task_context` | JSONB | Additional context parameters |
| `max_budget_per_run_usd` | DECIMAL(10,2) | Budget limit per execution |
| `timeout_seconds` | INTEGER | Workflow timeout |
| `temporal_schedule_id` | VARCHAR(255) | Temporal schedule ID (unique) |
| `status` | VARCHAR(20) | ACTIVE, PAUSED, or DELETED |
| `next_run_at` | TIMESTAMP | Next scheduled execution |
| `last_run_at` | TIMESTAMP | Last execution time |
| `total_runs` | INTEGER | Total executions |
| `successful_runs` | INTEGER | Successful executions |
| `failed_runs` | INTEGER | Failed executions |

### scheduled_task_executions

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `schedule_id` | UUID | Foreign key to scheduled_tasks |
| `task_id` | VARCHAR(255) | Temporal workflow ID |
| `status` | VARCHAR(20) | RUNNING, COMPLETED, FAILED, CANCELLED |
| `total_cost_usd` | DECIMAL(10,4) | Execution cost |
| `error_message` | TEXT | Error details (if failed) |
| `started_at` | TIMESTAMP | Execution start time |
| `completed_at` | TIMESTAMP | Execution completion time |

## Implementation Details

### Validation

1. **Cron expression**: Validated using `robfig/cron/v3` parser before creating Temporal schedule
2. **Resource limits**: Checked before schedule creation:
   - User must have < `SCHEDULE_MAX_PER_USER` active schedules
   - Budget must be ≤ `SCHEDULE_MAX_BUDGET_USD`
3. **Ownership**: All operations verify user_id and tenant_id match authenticated context

### Execution Flow

1. Temporal triggers `ScheduledTaskWorkflow` at scheduled time
2. Workflow records execution start in `scheduled_task_executions` and `task_executions`
3. **Quota pre-check** (`scheduled_quota_check_v1`): Checks tenant daily/monthly token quota via `CheckTenantQuota` activity. If exceeded → records FAILED status, emits `QUOTA_EXCEEDED` event, returns nil (no Temporal retry)
4. Workflow executes `OrchestratorWorkflow` as child workflow with task query
5. Child workflow result captured (success/failure, cost, tokens)
6. **Quota usage recording** (`scheduled_quota_record_v1`): Records consumed tokens via `RecordTenantQuotaUsage` activity (for both COMPLETED and FAILED runs). Falls back to `result.TokensUsed` when metadata lacks `total_tokens`
7. Workflow records execution completion with status, cost, and metadata
8. Schedule statistics updated (`total_runs`, `successful_runs`, `failed_runs`)

### Error Handling

- **Schedule creation failure**: Rollback Temporal schedule if DB insert fails
- **Execution failure**: Recorded in execution history, schedule remains active
- **Budget exceeded**: Child workflow fails with budget error
- **Quota exceeded**: Scheduled run rejected with FAILED status, `QUOTA_EXCEEDED` event emitted for webhook/LINE notification. Schedule remains active (next run will re-check)
- **Quota check failure**: Fail-open — if the quota check activity errors, execution proceeds (matches gateway behavior)
- **Temporal unavailable**: Schedule operations return service unavailable

## Deployment

### Database Migration

```bash
PGPASSWORD=shannon psql -h localhost -U shannon -d shannon \
  -f migrations/postgres/009_scheduled_tasks.sql
```

### Service Restart

Scheduling requires orchestrator service restart to load schedule manager:

```bash
docker compose -f deploy/compose/docker-compose.yml restart orchestrator
```

## Monitoring

### Metrics

- Check Temporal UI for schedule execution history
- Query `scheduled_task_executions` for cost analysis
- Monitor schedule statistics in `scheduled_tasks` table

### Temporal UI

Navigate to `http://localhost:8088` → Schedules to view:
- Schedule status (running/paused)
- Recent executions
- Next scheduled time
- Execution backlog

### Database Queries

```sql
-- Active schedules by user
SELECT user_id, COUNT(*)
FROM scheduled_tasks
WHERE status = 'ACTIVE'
GROUP BY user_id;

-- Total cost per schedule
SELECT s.name, SUM(e.total_cost_usd) as total_cost
FROM scheduled_tasks s
JOIN scheduled_task_executions e ON s.id = e.schedule_id
WHERE e.status = 'COMPLETED'
GROUP BY s.id, s.name
ORDER BY total_cost DESC;

-- Failure rate
SELECT
  name,
  total_runs,
  failed_runs,
  ROUND(100.0 * failed_runs / NULLIF(total_runs, 0), 2) as failure_rate
FROM scheduled_tasks
WHERE total_runs > 0
ORDER BY failure_rate DESC;
```

## Limitations

- Minimum interval: 60 minutes (configurable)
- Maximum schedules per user: 50 (configurable)
- Maximum budget per execution: $10 USD (configurable)
- Timezone support: IANA timezone database
- Cron precision: Minute-level (no seconds)

## Future Enhancements

- Schedule templates (daily/weekly/monthly presets)
- Execution result notifications (email, webhook)
- Dynamic budget adjustment based on historical costs
- Schedule dependencies (chain multiple schedules)
- Retry policies for failed executions
