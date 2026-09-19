# Token Budget and Cost Tracking

## 📖 中文学习注解

### 本文核心摘要
本文档详细说明 Shannon 的双路径 Token 追踪系统，确保每次 Agent 执行的 Token 用量精确记录一次（预算启用时由活动记录、禁用时由模式记录）。文档涵盖预算启用/禁用的完整决策流程、各模式（并行/顺序/React/Research 等）的 Token 记录方式、token_usage 数据库表结构、以及成本计算指标。还包含预算检查点的实现细节和工作流终止策略。

### 章节导航
- **Architecture**: 双路径记录——预算启用时 ExecuteAgentWithBudget 记录，禁用时各 Pattern 自行记录
- **Budget Flow**: 预算检查点（budget_checkpoint）——在工作流关键节点检查预算是否超限
- **Pattern Recording**: 各模式（Parallel/Sequential/React/Research/DAG）的 Token 记录策略
- **Database Schema**: token_usage 表结构及字段说明
- **Cost Calculation**: 基于 config/models.yaml 定价的输入/输出 Token 分别计费
- **Termination Strategy**: 超预算后的终止策略（硬停止 vs 降级）

### 与 AI Agent 体系的关联
- 预算管理器：`go/orchestrator/internal/workflows/budget.go` 及 budget_manager.go
- Token 记录活动：`go/orchestrator/internal/activities/agent.go` 中的 ExecuteAgentWithBudget
- 定价配置中心：`config/models.yaml` 的 pricing 段
- 数据库：token_usage 表记录每次调用的 Token/费用详情
- 模式内记录：`go/orchestrator/internal/patterns/` 下各模式的 token 记录逻辑

### 阅读建议
平台运维和成本管控人员必读；AI 应用开发者重点看 Budget Flow 和 Pattern Recording 以合理设置预算；简单使用场景可略读 Termination Strategy 细节。

## Overview

Shannon implements a dual-path token tracking system that ensures accurate cost reporting across all workflow patterns while preventing duplicate recordings when budgets are enabled.

**Key Principle**: Every agent execution records token usage **exactly once**, through either the budgeted activity (when budgets are enabled) or the workflow pattern (when budgets are disabled).

## Architecture

### Recording Paths

```
┌─────────────────────────────────────────────────────────────────┐
│                     Workflow Execution                          │
└────────────────┬────────────────────────────────────────────────┘
                 │
                 ├── Budget Enabled? ──────┐
                 │                          │
        ┌────────▼─────────┐     ┌─────────▼──────────┐
        │   YES (Budget)   │     │  NO (Non-Budget)   │
        └────────┬─────────┘     └──────────┬─────────┘
                 │                           │
                 ▼                           ▼
    ┌────────────────────────┐   ┌──────────────────────────┐
    │ ExecuteAgentWithBudget │   │    ExecuteAgent          │
    │    (activity)          │   │    (activity)            │
    └────────┬───────────────┘   └──────────┬───────────────┘
             │                               │
             ▼                               │
    ┌────────────────────────┐              │
    │ Records token usage    │              │
    │ in activity (budget.go)│              │
    └────────────────────────┘              │
                                             ▼
                                  ┌──────────────────────────┐
                                  │ Pattern records tokens   │
                                  │ (parallel/react/etc.)    │
                                  └──────────────────────────┘
                 │                           │
                 └───────────┬───────────────┘
                             ▼
                ┌────────────────────────────┐
                │    token_usage table       │
                │  (one record per agent)    │
                └────────────────────────────┘
```

## Core Components

### 1. Budgeted Execution Activity

**File**: `go/orchestrator/internal/activities/budget.go`

**Function**: `ExecuteAgentWithBudget`

**Responsibilities**:
- Enforces token budget limits per agent
- Calls LLM service with token constraints
- **Records token usage immediately** (line 346-356)
- Returns execution result to workflow

**Recording Logic**:
```go
// Line 346-356: Records usage inside the activity
err = b.budgetManager.RecordUsage(ctx, &budget.BudgetTokenUsage{
    UserID:         input.UserID,
    SessionID:      input.AgentInput.SessionID,
    TaskID:         input.TaskID,
    AgentID:        input.AgentInput.AgentID,
    Model:          actualModel,
    Provider:       actualProvider,
    InputTokens:    inputTokens,
    OutputTokens:   outputTokens,
    IdempotencyKey: idempotencyKey,
})
```

**When Used**:
- `budgetPerAgent > 0` (Parallel, Sequential, Hybrid patterns)
- `opts.BudgetAgentMax > 0` (React, Chain-of-Thought, Debate, Tree-of-Thoughts)
- SupervisorWorkflow with budget constraints

---

### 2. Non-Budgeted Execution Activity

**File**: `go/orchestrator/internal/activities/agent.go`

**Function**: `ExecuteAgent`

**Responsibilities**:
- Executes agent without budget constraints
- Calls LLM service
- **Does NOT record token usage** (pattern does it)
- Returns execution result to workflow

**When Used**:
- SimpleTaskWorkflow
- Workflows without budget constraints
- Decompose/Refine/Synthesis phases (always non-budgeted)

---

### 3. Workflow Pattern Recording

All workflow patterns implement conditional token recording to avoid duplicates:

#### Guard Pattern

```go
// Budgeted path: Activity records, pattern skips
if budgetPerAgent <= 0 {  // or opts.BudgetAgentMax <= 0
    _ = workflow.ExecuteActivity(ctx, constants.RecordTokenUsageActivity, ...)
}
```

#### Patterns with Recording

| Pattern | File | Guard Location | Agent IDs |
|---------|------|----------------|-----------|
| **Parallel** | `patterns/execution/parallel.go` | Line 267 | Result AgentID |
| **Sequential** | `patterns/execution/sequential.go` | Line 289 | Result AgentID |
| **React** | `patterns/react.go` | Lines 154, 367, 493 | reasoner, action, synthesizer |
| **Chain-of-Thought** | `patterns/chain_of_thought.go` | Lines 145, 236 | cot-reasoner, cot-clarifier |
| **Debate** | `patterns/debate.go` | Lines 201, 354 | debater AgentIDs |
| **Tree-of-Thoughts** | `patterns/tree_of_thoughts.go` | Lines 272-350 | tot-generator-{id} |
| **Reflection** | `patterns/wrappers.go`, `patterns/reflection.go` | Lines 212-245 (initial), 123-159 (synth) | reflection-initial, reflection-synth |
| **ScheduledTask** | `workflows/scheduled/scheduled_task_workflow.go` | Version gate `scheduled_quota_record_v1` | N/A (records tenant quota, not per-agent) |

#### Patterns without Recording (Delegates)

| Pattern | File | Delegation Target |
|---------|------|-------------------|
| **Hybrid** | `patterns/execution/hybrid.go` | Delegates to Parallel (line 275) |

---

### 4. Workflow-Level Recording (Always Records)

Some workflow phases always record usage (no budget constraints apply):

**OrchestratorWorkflow - Decompose**
- File: `internal/workflows/orchestrator_router.go:218`
- AgentID: `"decompose"`
- Metadata: `{"phase": "decompose"}`

**SupervisorWorkflow - Decompose**
- File: `internal/workflows/supervisor_workflow.go:337`
- AgentID: `"decompose"`
- Metadata: `{"phase": "decompose"}`

**ResearchWorkflow - Phases**
- Refine: `strategies/research.go:204` → AgentID: `"research-refiner"`
- Decompose: `strategies/research.go:265` → AgentID: `"decompose"`
- Synthesis: `strategies/research.go:826` → AgentID: `"synthesis"`

**SimpleTaskWorkflow**
- File: `internal/workflows/simple_workflow.go:487`
- AgentID: `"simple-agent"`
- Always records (single agent, no budget splitting)

---

## Token Usage Recording

### Database Schema

**Table**: `token_usage`

```sql
CREATE TABLE token_usage (
    id                uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id           uuid,
    provider          varchar(50)  NOT NULL,
    model             varchar(255) NOT NULL,
    prompt_tokens     integer      NOT NULL,
    completion_tokens integer      NOT NULL,
    total_tokens      integer      NOT NULL,
    cost_usd          numeric(10,6) NOT NULL,
    created_at        timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
    task_id           uuid REFERENCES task_executions(id) ON DELETE CASCADE
);
```

**Indexes**:
- Primary key on `id`
- Foreign key on `task_id` → `task_executions(id)`
- Index on `user_id` for user-level cost queries
- Index on `provider`, `model` for provider/model analytics
- Index on `created_at` for time-series queries

---

### Recording Flow

```
┌──────────────────────────────────────────────────────────────────┐
│ 1. LLM Provider Returns Response                                │
│    - model_used: "gpt-5-2025-08-07"                             │
│    - provider: "openai"                                         │
│    - input_tokens: 732                                          │
│    - output_tokens: 1464                                        │
└────────────────────┬─────────────────────────────────────────────┘
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│ 2. Activity Captures Token Counts                               │
│    - AgentExecutionResult.InputTokens = 732                     │
│    - AgentExecutionResult.OutputTokens = 1464                   │
│    - AgentExecutionResult.ModelUsed = "gpt-5-2025-08-07"        │
│    - AgentExecutionResult.Provider = "openai"                   │
└────────────────────┬─────────────────────────────────────────────┘
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│ 3. Recording Decision (Budgeted vs Non-Budgeted)                │
│                                                                  │
│  IF budgetPerAgent > 0:                                         │
│    → ExecuteAgentWithBudget records in activity (budget.go:346) │
│    → Workflow pattern SKIPS recording (guard prevents duplicate)│
│                                                                  │
│  ELSE (budgetPerAgent <= 0):                                    │
│    → ExecuteAgent does NOT record                               │
│    → Workflow pattern records via RecordTokenUsageActivity      │
└────────────────────┬─────────────────────────────────────────────┘
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│ 4. RecordTokenUsageActivity (activities/session.go)             │
│    - Resolves task_id from workflow_id                          │
│    - Calculates cost: pricing.CostForSplit(model, in, out)      │
│    - Inserts into token_usage table                             │
└────────────────────┬─────────────────────────────────────────────┘
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│ 5. Database Persistence (PostgreSQL)                            │
│    INSERT INTO token_usage (                                    │
│      user_id, provider, model,                                  │
│      prompt_tokens, completion_tokens, total_tokens,            │
│      cost_usd, task_id                                          │
│    ) VALUES (...)                                               │
└────────────────────┬─────────────────────────────────────────────┘
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│ 6. Workflow Aggregation (metadata/aggregate.go)                 │
│    - Sums all token_usage records for task                      │
│    - Calculates total cost                                      │
│    - Populates TaskResult.Metadata:                             │
│      {                                                           │
│        "model_used": "gpt-5-2025-08-07",                        │
│        "provider": "openai",                                    │
│        "input_tokens": 4362,                                    │
│        "output_tokens": 8727,                                   │
│        "total_tokens": 13089,                                   │
│        "cost_usd": 0.183258                                     │
│      }                                                           │
└────────────────────┬─────────────────────────────────────────────┘
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│ 7. API Response (GET /api/v1/tasks/{id})                        │
│    {                                                             │
│      "task_id": "task-...",                                     │
│      "status": "TASK_STATUS_COMPLETED",                         │
│      "usage": {                                                 │
│        "total_tokens": 13089,                                   │
│        "prompt_tokens": 4362,                                   │
│        "completion_tokens": 8727,                               │
│        "cost_usd": 0.183258                                     │
│      },                                                          │
│      "metadata": {                                              │
│        "model_used": "gpt-5-2025-08-07",                        │
│        "provider": "openai"                                     │
│      }                                                           │
│    }                                                             │
└──────────────────────────────────────────────────────────────────┘
```

---

## Zero-Token Executions

### Tool-Only Paths

Some agent executions return **0 LLM tokens** by design when no LLM inference occurs:

**Common Scenarios**:
- **Forced tool calls**: Agent uses tools without LLM reasoning (`forced_tools` mode)
- **Cache hits**: Response served entirely from cache (future feature)
- **Tool-only workflows**: Workflows that only execute tools without text generation

**Example** (from ResearchWorkflow):
```go
// Tool-only web search execution
result := executeWebSearch(ctx, query)
// Returns: InputTokens=0, OutputTokens=0, ToolExecutions=[...]
```

See [Deep Research Overview](deep-research-overview.md#gap-filling-and-convergence) for details on gap-filling tool execution.

---

### Recording Behavior

**Default**: Zero-token executions are **NOT recorded** to `token_usage` table

**Rationale**:
- No LLM cost to track
- No billable token consumption
- Reduces database writes for non-LLM operations

**Implementation**:
```go
// Pattern recording guards (parallel.go, sequential.go)
if result.TokensUsed == 0 && result.InputTokens == 0 {
    if recordZeroToken, ok := context["record_zero_token"].(bool); !ok || !recordZeroToken {
        logger.Warn("Skipping zero-token recording (no LLM inference occurred)")
        return nil  // Skip recording
    }
}
```

**Locations**:
- `go/orchestrator/internal/workflows/patterns/execution/sequential.go:316-380`
- `go/orchestrator/internal/workflows/patterns/execution/parallel.go:295-320`

---

### Enabling Zero-Token Recording

To record zero-token executions for **audit trail purposes**:

**Via API Context**:
```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "Task with tool-only execution",
    "context": {
      "record_zero_token": true
    }
  }'
```

**Via Workflow Context**:
```go
baseContext := map[string]interface{}{
    "record_zero_token": true,  // Enable zero-token recording
}
```

**When Enabled**:
- Zero-token records inserted into `token_usage` table
- `cost_usd` = 0.0
- Metadata can distinguish tool-only vs. LLM executions

**Use Cases**:
- Compliance/audit requirements (track all agent executions)
- Debugging tool execution workflows
- Analytics on tool-only vs. LLM paths

---

### Distinguishing Zero-Token Types

When reviewing `token_usage` records with 0 tokens:

```sql
-- Find zero-token executions
SELECT
    tu.model,
    tu.provider,
    tu.total_tokens,
    tu.cost_usd,
    te.metadata->>'tool_executions' as tools_used
FROM token_usage tu
JOIN task_executions te ON tu.task_id = te.id
WHERE tu.total_tokens = 0
ORDER BY tu.created_at DESC;
```

**Interpretation**:
- `tools_used IS NOT NULL` → Forced tool call (expected zero)
- `tools_used IS NULL` → Potential issue (LLM should have returned tokens)

---

## Preventing Duplicate Recordings

### The Problem (Before Fix)

**Issue**: Budgeted execution recorded usage twice:
1. **Activity recording**: `budget.go:346` (inside ExecuteAgentWithBudget)
2. **Pattern recording**: `parallel.go:289` (workflow-level)

**Impact**: 2× token count, 2× cost calculation, duplicate database rows

**Example**:
```
Actual usage: 2,196 tokens ($0.030744)
Recorded:     4,392 tokens ($0.061488)  ← 2× overcount
```

---

### The Solution (Option B - Conditional Guards)

**Strategy**: Prevent pattern-level recording when budgets are enabled

**Implementation**: Guard all pattern-level recording with budget checks

```go
// BEFORE (duplicates)
_ = workflow.ExecuteActivity(ctx, constants.RecordTokenUsageActivity, ...)

// AFTER (no duplicates)
if budgetPerAgent <= 0 {  // Only record if NOT budgeted
    _ = workflow.ExecuteActivity(ctx, constants.RecordTokenUsageActivity, ...)
}
```

**Guard Variations**:
- Parallel/Sequential/Hybrid: `if budgetPerAgent <= 0`
- React/CoT/Debate: `if opts.BudgetAgentMax <= 0`
- Tree-of-Thoughts: `if tokenBudget > 0 { budgeted } else { record }`

---

### Verification

**Test Query**:
```sql
-- Check for duplicate recordings (should return 0 rows)
SELECT
  prompt_tokens,
  completion_tokens,
  model,
  COUNT(*) as record_count
FROM token_usage tu
JOIN task_executions te ON tu.task_id = te.id
WHERE te.workflow_id = 'task-...'
GROUP BY prompt_tokens, completion_tokens, model
HAVING COUNT(*) > 1;
```

**Expected Result**: 0 rows (no duplicates)

---

## Budget Configuration

### Setting Budgets

**Via API**:
```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "Complex research task",
    "context": {
      "budget_agent_max": 50000,  # Max tokens per agent
      "budget_total_max": 200000  # Max tokens for entire workflow
    }
  }'
```

**Via Template**:
```yaml
name: research_workflow
defaults:
  budget_agent_max: 30000
  budget_total_max: 150000
```

**Via Environment**:
```bash
# Default budgets in .env
BUDGET_AGENT_MAX=40000
BUDGET_TOTAL_MAX=200000
```

---

### Budget Enforcement

**SupervisorWorkflow Example**:
```go
// Determine budget per agent
agentMaxTokens := 0
if v, ok := baseContext["budget_agent_max"].(int); ok {
    agentMaxTokens = v
}

// Calculate per-agent budget
budgetPerAgent := 0
if agentMaxTokens > 0 && len(tasks) > 0 {
    budgetPerAgent = agentMaxTokens / len(tasks)
}

// Execute with budget
if budgetPerAgent > 0 {
    // Budgeted path: activity records
    err := workflow.ExecuteActivity(ctx,
        constants.ExecuteAgentWithBudgetActivity,
        activities.BudgetedAgentInput{
            AgentInput: agentInput,
            MaxTokens:  budgetPerAgent,
            UserID:     input.UserID,
            TaskID:     workflowID,
            ModelTier:  modelTier,
        }).Get(ctx, &result)
} else {
    // Non-budgeted path: pattern records
    err := workflow.ExecuteActivity(ctx,
        activities.ExecuteAgent,
        agentInput).Get(ctx, &result)
}
```

---

## Cost Calculation

### Pricing Configuration

**File**: `config/models.yaml`

```yaml
pricing:
  defaults:
    combined_per_1k: 0.005
  models:
    openai:
      gpt-5-2025-08-07:
        input_per_1k: 0.0060   # $0.006 per 1K input tokens
        output_per_1k: 0.0180  # $0.018 per 1K output tokens
```

See [centralized-pricing.md](centralized-pricing.md) for details.

---

### Calculation Logic

**File**: `go/orchestrator/internal/pricing/pricing.go`

**Function**: `CostForSplit`

```go
func CostForSplit(model string, inputTokens, outputTokens int) float64 {
    provider := DetectProvider(model)

    // Lookup pricing config
    inputPrice := getInputPricePerToken(provider, model)
    outputPrice := getOutputPricePerToken(provider, model)

    // Calculate cost
    inputCost := float64(inputTokens) * inputPrice
    outputCost := float64(outputTokens) * outputPrice

    return inputCost + outputCost
}
```

**Example**:
```
Model: gpt-5-2025-08-07
Input: 732 tokens × $0.000006 = $0.004392
Output: 1464 tokens × $0.000018 = $0.026352
Total: $0.030744
```

---

## Metadata Aggregation

### Aggregation Logic

**File**: `go/orchestrator/internal/metadata/aggregate.go`

**Function**: `AggregateAgentMetadata` (legacy helper)

This helper builds a lightweight summary (model_used, provider, input_tokens, output_tokens, total_tokens) from in-memory agent execution results and may add a best-effort `agent_usages` array for debugging.

> ⚠️ `agent_usages` is deprecated for external consumers. For complete usage and cost data (including synthesis, decomposition, utilities, and failed attempts), rely on `model_breakdown`, which is built from the `token_usage` table.

Workflows still call `AggregateAgentMetadata` to populate task-level metadata, but billing, reporting, and the public API should treat `model_breakdown` as the source of truth.

---

## Monitoring and Debugging

### Database Queries

**Check token usage for a task**:
```sql
SELECT
    tu.model,
    tu.provider,
    tu.prompt_tokens,
    tu.completion_tokens,
    tu.total_tokens,
    tu.cost_usd,
    tu.created_at
FROM token_usage tu
JOIN task_executions te ON tu.task_id = te.id
WHERE te.workflow_id = 'task-...'
ORDER BY tu.created_at;
```

**Aggregate costs by task**:
```sql
SELECT
    te.workflow_id,
    COUNT(tu.id) as agent_count,
    SUM(tu.prompt_tokens) as total_input,
    SUM(tu.completion_tokens) as total_output,
    SUM(tu.total_tokens) as total_tokens,
    SUM(tu.cost_usd) as total_cost
FROM task_executions te
LEFT JOIN token_usage tu ON tu.task_id = te.id
WHERE te.user_id = '...'
GROUP BY te.workflow_id
ORDER BY total_cost DESC
LIMIT 10;
```

**Detect duplicate recordings**:
```sql
SELECT
    prompt_tokens,
    completion_tokens,
    model,
    COUNT(*) as duplicates
FROM token_usage
WHERE task_id = (
    SELECT id FROM task_executions WHERE workflow_id = 'task-...'
)
GROUP BY prompt_tokens, completion_tokens, model
HAVING COUNT(*) > 1;
```

---

### Orchestrator Logs

**Budget tracking**:
```bash
docker compose -f deploy/compose/docker-compose.yml logs orchestrator | \
  grep "Budget used"
```

**Token recording**:
```bash
docker compose -f deploy/compose/docker-compose.yml logs orchestrator | \
  grep "RecordTokenUsage"
```

**Cost calculation**:
```bash
docker compose -f deploy/compose/docker-compose.yml logs orchestrator | \
  grep "cost_usd"
```

---

### Temporal Workflow History

**View workflow events**:
```bash
docker compose exec temporal temporal workflow describe \
  --workflow-id task-... \
  --address temporal:7233
```

**Check activity executions**:
```bash
docker compose exec temporal temporal workflow show \
  --workflow-id task-... \
  --address temporal:7233 | \
  grep -A 5 "RecordTokenUsageActivity"
```

---

## Best Practices

### 1. Always Use Split Token Counts

**Preferred**:
```go
cost := pricing.CostForSplit(model, inputTokens, outputTokens)
```

**Avoid**:
```go
cost := pricing.CostForTokens(model, totalTokens)  // Less accurate
```

**Reason**: Input and output tokens have different prices

---

### 2. Propagate Model Information

**Required in AgentExecutionResult**:
- `ModelUsed` (e.g., "gpt-5-2025-08-07")
- `Provider` (e.g., "openai")
- `InputTokens` (prompt tokens)
- `OutputTokens` (completion tokens)

**Example**:
```go
result := activities.AgentExecutionResult{
    Response:      llmResponse.Content,
    TokensUsed:    llmResponse.Usage.TotalTokens,
    InputTokens:   llmResponse.Usage.PromptTokens,
    OutputTokens:  llmResponse.Usage.CompletionTokens,
    ModelUsed:     llmResponse.Model,
    Provider:      "openai",
    AgentID:       agentID,
}
```

---

### 3. Guard Pattern-Level Recording

**When adding new patterns**, always guard recording:

```go
// Check if budgeted execution is active
if budgetPerAgent <= 0 {  // or opts.BudgetAgentMax <= 0
    // Record token usage
    _ = workflow.ExecuteActivity(ctx,
        constants.RecordTokenUsageActivity,
        activities.TokenUsageInput{
            UserID:       userID,
            SessionID:    sessionID,
            TaskID:       workflowID,
            AgentID:      agentID,
            Model:        result.ModelUsed,
            Provider:     result.Provider,
            InputTokens:  result.InputTokens,
            OutputTokens: result.OutputTokens,
            Metadata:     map[string]interface{}{"phase": "pattern_name"},
        }).Get(ctx, nil)
}
```

---

### 4. Include Phase Metadata

**Purpose**: Distinguish recording sources in analytics

**Examples**:
- `{"phase": "parallel"}`
- `{"phase": "react-reasoner"}`
- `{"phase": "decompose"}`
- `{"phase": "synthesis"}`

**Usage**:
```sql
-- Analyze token usage by phase
SELECT
    metadata->>'phase' as phase,
    COUNT(*) as executions,
    AVG(total_tokens) as avg_tokens,
    SUM(cost_usd) as total_cost
FROM token_usage
GROUP BY metadata->>'phase'
ORDER BY total_cost DESC;
```

---

### 5. Verify No Duplicates After Changes

**After modifying token recording logic**:

1. Run a test workflow with budgets enabled
2. Query for duplicates:
```sql
SELECT COUNT(*) FROM (
    SELECT prompt_tokens, completion_tokens, model, COUNT(*)
    FROM token_usage
    WHERE task_id IN (
        SELECT id FROM task_executions
        WHERE workflow_id = 'test-task-...'
    )
    GROUP BY prompt_tokens, completion_tokens, model
    HAVING COUNT(*) > 1
) duplicates;
```
3. Expected result: `0`

---

## Related Documentation

- [Centralized Pricing Configuration](centralized-pricing.md) - Pricing configuration and cost calculation
- [Rate-Aware Budgeting](rate-aware-budgeting.md) - Rate limit management and control
- [Multi-Agent Workflow Architecture](multi-agent-workflow-architecture.md) - Workflow patterns and execution
- [Pattern Usage Guide](pattern-usage-guide.md) - When to use each workflow pattern
- [API Reference](api-reference.md) - REST API endpoints for task submission and status

---

## Troubleshooting

### Missing Token Counts

**Symptom**: `input_tokens` and `output_tokens` are 0 in API response

**Causes**:
1. LLM provider not returning token counts
2. Agent activity not capturing token counts
3. Fallback to `TokensUsed` (total only)

**Solution**:
```go
// Ensure LLM response includes token counts
if result.InputTokens == 0 && result.OutputTokens == 0 {
    if result.TokensUsed > 0 {
        // Approximate split (60/40)
        result.InputTokens = result.TokensUsed * 6 / 10
        result.OutputTokens = result.TokensUsed - result.InputTokens
    }
}
```

---

### Duplicate Recordings

**Symptom**: Token counts are 2× expected value

**Causes**:
1. Pattern-level recording not guarded
2. Budget check missing or incorrect
3. Both activity and pattern recording

**Solution**:
1. Add budget guard to pattern recording
2. Verify guard condition: `if budgetPerAgent <= 0`
3. Run duplicate detection query
4. Review Temporal workflow history for duplicate activities

---

### Incorrect Costs

**Symptom**: Cost doesn't match expected value

**Causes**:
1. Wrong pricing configuration in `models.yaml`
2. Model name mismatch (e.g., "gpt-5" vs "gpt-5-2025-08-07")
3. Provider detection failure
4. Using combined pricing instead of split pricing

**Solution**:
1. Verify pricing in `config/models.yaml`
2. Check model name matches exactly: `SELECT DISTINCT model FROM token_usage`
3. Force provider: `result.Provider = "openai"`
4. Use `CostForSplit` instead of `CostForTokens`

---

### Budget Not Enforced

**Symptom**: Agent exceeds budget without error

**Causes**:
1. Budget not passed to activity
2. Budget check disabled
3. Using `ExecuteAgent` instead of `ExecuteAgentWithBudget`

**Solution**:
1. Verify budget in context: `budget_agent_max`
2. Check activity call:
```go
// Should use ExecuteAgentWithBudget
err := workflow.ExecuteActivity(ctx,
    constants.ExecuteAgentWithBudgetActivity,
    activities.BudgetedAgentInput{
        AgentInput: agentInput,
        MaxTokens:  budgetPerAgent,  // ← Ensure > 0
        ...
    })
```
3. Review budget manager logs: `grep "Budget exceeded"`

---

## Cache-Aware Token Accounting (migration 121)

Shannon tracks **two parallel total-token fields**, persisted on both `token_usage` (per-LLM-call) and `task_executions` (per-workflow):

| Field | Formula | Purpose |
|-------|---------|---------|
| `total_tokens` | `prompt_tokens + completion_tokens` | OpenAI-compatible API responses; response-shaped metadata |
| `cache_aware_total_tokens` | `prompt + completion + cache_read + cache_creation` | Quota tracking, true cost, enterprise billing |

The two are intentionally distinct: `total_tokens` keeps OpenAI semantics so existing SDK clients (and `usage.total_tokens` in `/v1/chat/completions` / `/v1/completions` responses) remain unchanged. `cache_aware_total_tokens` is the field enterprise quota counters should read.

### Invariant

For every row in either table:

```
cache_aware_total_tokens
  == prompt_tokens + completion_tokens + cache_read_tokens + cache_creation_tokens
```

Run `scripts/verify_cache_quota_invariant.sh` to confirm. Empty output (per-row mismatch = 0, per-task mismatch = 0) means the invariant holds.

### Write Paths

There are **four** write paths to `token_usage`. All must populate `cache_aware_total_tokens`:

| Path | File | Note |
|------|------|------|
| BudgetManager (orchestrated workflows) | `internal/budget/manager.go` `RecordUsage` / `storeUsage` | Computes `CacheAwareTotalTokens` immediately after `TotalTokens` |
| Gateway completions proxy | `cmd/gateway/internal/openai/handler.go` `recordCompletionUsage` | `/v1/completions` bypasses the orchestrator — its INSERT computes the field inline |
| Gateway tools proxy | `cmd/gateway/internal/handlers/tools.go` `recordToolUsage` | `/api/v1/tools/.../execute` direct INSERT — stores `tokens` under `completion_tokens` so the invariant holds without fabricating cache fields |
| TaskExecution writer (rollup, separate table) | `internal/db/task_writer.go` | Writes the per-workflow rollup into `task_executions`; defensive recompute right before each `INSERT` so callers cannot drift |

The zero-token guard in `internal/activities/budget.go` (`shouldRecordUsage`) admits pure cache hits: a call with `input + output == 0` but `cache_read > 0` or `cache_creation > 0` is recorded so prompt-cache cost still bills.

### Read / Quota Paths Updated

| Path | File | Behavior |
|------|------|----------|
| Workflow rollup SUM | `internal/server/service.go` (`getTaskMetadataFromDB`) | `SUM(cache_aware_total_tokens)` with parts-based fallback |
| Per-agent breakdown | `internal/server/service.go` (`agent_usages`) | Emits `cache_aware_total_tokens` alongside `total_tokens` |
| Usage report | `internal/budget/manager.go` `GetUsageReport` | `UsageReport.CacheAwareTotalTokens` populated; `ModelUsage.CacheAwareTokens` per model |
| Session quota update | `internal/workflows/strategies/research.go` | `session.TokensUsed` reads `report.CacheAwareTotalTokens` (falls back to `TotalTokens` mid-migration) |
| Session API (`tokens_used`) | `cmd/gateway/internal/handlers/session.go` | Both single-session and list endpoints SUM `cache_aware_total_tokens` |
| Per-task in-memory metadata | `internal/metadata/aggregate.go` | Emits `meta["cache_aware_total_tokens"]` in addition to `total_tokens` |

### gRPC Surface

`RecordTokenUsageRequest` (proto tags 7-9) carries `cache_read_tokens`, `cache_creation_tokens`, `cache_creation_1h_tokens`. The `OrchestratorService.RecordUsage` hook takes a `RecordUsageDetails` struct with `CacheAwareTotalTokens` precomputed for enterprise overrides.

### shannon-cloud Cherry-Pick Checklist

After cherry-picking the `feat/cache-aware-quota-rollup` commits to `shannon-cloud`:

1. Confirm `migrations/postgres/121_cache_aware_total_tokens.sql` is applied in production.
2. In shannon-cloud's override of `OrchestratorService.RecordUsage`, switch the quota counter from any `tokensUsed`-style argument to `details.CacheAwareTotalTokens`.
3. **Audit any cloud-side SQL** that reads `SUM(total_tokens)` / `tokens_used` / writes its own `token_usage` rows. Each occurrence is a candidate cache leak. The OSS audit found four write paths and six read paths (table above) — assume the cloud diff has the same shape.
4. Run `scripts/verify_cache_quota_invariant.sh` against the production database — expect both mismatch counts to be 0.
5. Watch quota dashboards: per-tenant token usage will jump (this is the leak finally being recorded — not a regression).
