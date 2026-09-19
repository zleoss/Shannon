# Templates

## 📖 中文学习注解

### 本文核心摘要
本文档是 Shannon 模板工作流系统的完整指南——通过 YAML 定义确定性、零 Token 的工作流（System 1 快速路径）。文档从创建模板 YAML 起步（包含节点定义、策略选择、预算/工具限制），逐步深入到架构实现（Dual-System 设计：模板作为 System 1 零成本路由，AI 分解作为 System 2 灵活兜底）、节点类型（simple/cognitive/dag/supervisor）、输入/输出映射和最佳实践。

### 章节导航
- **Getting Started**: 创建 YAML 模板 → InitTemplateRegistry 加载 → ListTemplates 列出 → 执行
- **Architecture**: Dual-System——System 1（模板，0 Token，确定）和 System 2（AI 分解，全成本，灵活）
- **Node Types**: simple（单 Agent）/ cognitive（认知策略）/ dag（有向无环图）/ supervisor（编排器）
- **Strategy Selection**: 各节点可选的策略（react/cot/reflection/debate/tot）
- **Input/Output Mapping**: 模板变量映射和结果传递
- **Best Practices**: 模板设计技巧、预算控制、节点依赖规划
- **Configuration Reference**: YAML 字段（name/version/defaults/nodes/edges）完整参考

### 与 AI Agent 体系的关联
- 模板注册：`go/orchestrator/internal/workflows/template_catalog.go`
- 模板执行工作流：`go/orchestrator/internal/workflows/template_workflow.go`
- 模板目录：`config/workflows/examples/`
- 学习路由器：`go/orchestrator/internal/workflows/learning_router.go`（System 1 的扩展）

### 阅读建议
需要为常见任务创建可复用工作流的开发者必读；初学者从 Getting Started 入门再到 Architecture。

Deterministic, zero‑token workflows for common patterns. This guide combines getting started steps with deeper architecture and best practices.

---

## Getting Started

1) Create a template YAML under `config/workflows/examples/` (or your folder):

```yaml
name: simple_analysis
version: "1.0.0"
defaults:
  model_tier: medium
  budget_agent_max: 5000
  require_approval: false

nodes:
  - id: analyze
    type: simple
    strategy: react
    tools_allowlist: ["web_search"]
    budget_max: 500
    depends_on: []
```

Tips:
- `type`: `simple | cognitive | dag | supervisor`
- `strategy`: `react | chain_of_thought | reflection | debate | tree_of_thoughts`
- Set per‑node `budget_max` and `tools_allowlist` to constrain execution.

2) Load templates at startup

Loaded via `InitTemplateRegistry` from one or more directories: see `go/orchestrator/internal/workflows/template_catalog.go`.

3) List available templates

```
grpcurl -plaintext -d '{}' localhost:50052 \
  shannon.orchestrator.OrchestratorService/ListTemplates
```

4) Execute a template

Request template execution by name/version and optionally disable AI:

```
grpcurl -plaintext -d '{
  "query": "Summarize this week's tech news",
  "context": {
    "template": "simple_analysis",
    "template_version": "1.0.0",
    "disable_ai": true
  }
}' localhost:50052 shannon.orchestrator.OrchestratorService/SubmitTask
```

Notes:
- When `disable_ai` is true and the template is missing, the request fails fast.
- When `workflows.templates.fallback_to_ai` (or `TEMPLATE_FALLBACK_ENABLED=1`) is enabled, failed template runs can fall back to AI decomposition.

HTTP usage:

```bash
curl -sS -X POST http://localhost:8080/api/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "Weekly research briefing",
    "context": {
      "template": "simple_analysis",
      "template_version": "1.0.0",
      "disable_ai": true
    }
  }'
```

Alias support: `context.template_name` is accepted as an alias for `context.template` when calling the HTTP API.

5) Best practices

- Keep nodes small and deterministic; prefer more nodes over large monoliths.
- Restrict tools explicitly per node.
- Set `defaults.require_approval` when human sign‑off is needed.
- Use `extends` to share defaults; registry validates via `Finalize()`.

---

## Overview & Architecture

Templates provide zero‑token routing (System 1) alongside intelligent AI decomposition (System 2):

```
┌─────────────────────────────────────────────────────────────┐
│                     User Query                               │
└────────────────┬────────────────────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────────────────┐
│              Orchestrator Router                             │
│  1. Check template registry for exact match                  │
│  2. Consult learning router for learned patterns             │
│  3. Fall back to AI decomposition if needed                  │
└────────────────┬────────────────────────────────────────────┘
                 │
        ┌────────┴────────┬─────────────┐
        ▼                 ▼             ▼
┌───────────────┐ ┌──────────────┐ ┌──────────────┐
│  Template     │ │   Learning   │ │     AI       │
│  Workflow     │ │   Router     │ │ Decomposition│
│ (0 tokens)    │ │ (0 tokens)   │ │ (full cost)  │
└───────────────┘ └──────────────┘ └──────────────┘
```

System 1 (Templates): deterministic, version‑gated workflows; per‑node budgets with automatic degradation.
System 2 (Learning Router): recommends strategies for repeated patterns; configurable exploration.

---

## Template Structure

### Basic Format

```yaml
name: template_name
description: "What this does"
version: "1.0.0"
defaults:
  budget_agent_max: 10000
  require_approval: false

nodes:
  - id: node_1
    type: simple                 # simple/cognitive/dag/supervisor
    strategy: react              # react/chain_of_thought/tree_of_thoughts/debate/reflection
    budget_max: 2000
    tools_allowlist:
      - web_search
      - calculator
    depends_on: []               # DAG ordering

  - id: node_2
    type: cognitive
    strategy: tree_of_thoughts
    budget_max: 5000
    depends_on: ["node_1"]

edges:
  - from: node_1
    to: node_2
```

Use `tools_allowlist` on nodes. For hybrid DAG tasks inside `metadata.tasks`, either `tools` or `tools_allowlist` is accepted.

### Node Types

Simple
- Single‑task execution, direct tool invocation, minimal context.

Cognitive
- Complex reasoning tasks; multiple patterns (ReAct/CoT/ToT/etc.).
- Automatic budget‑based degradation (see below) — no extra flag needed.

DAG
- Parallel execution branches and dependencies in one node.

Supervisor
- Aggregates results from branches; orchestrates final synthesis.

---

## Pattern Degradation

### Automatic Budget Management (Current)

Patterns degrade automatically by `budget_max` using thresholds. Example:

```
Tree of Thoughts → Chain of Thought → ReAct
   (budget ↓)           (budget ↓)
```

Runtime signals degradation in node metadata (e.g., `degraded_from`, `degraded_to`).

### on_fail (Validated, Limited Use)

The YAML supports `on_fail` with `degrade_to`, `retry`, and `escalate_to`. These fields are validated today but not fully enforced by the runtime yet. Budget‑based degradation is the active mechanism.

```yaml
nodes:
  - id: analyze
    type: cognitive
    strategy: tree_of_thoughts
    budget_max: 5000
    on_fail:
      degrade_to: chain_of_thought  # Validated; runtime enforcement TBD
      retry: 1
```

---

## Learning Router

When enabled (`continuous_learning.enabled: true` or `CONTINUOUS_LEARNING_ENABLED=1`), the router will consult a strategy recommender before falling back to decomposition. Unknown strategies are safely ignored.

---

## Registry Management

Template loading

```bash
# Default locations
/app/config/workflows/
/app/config/workflows/examples/

# Custom via environment
export TEMPLATES_PATH="/custom/templates:/shared/templates"
```

Hot reload (Future)

```yaml
registry:
  hot_reload: true
  reload_interval: 30s
  validation: strict
```

---

## Validation

Rules
1. Structure: required fields, valid YAML
2. DAG: no cycles, valid dependencies
3. Budget: node budgets ≤ defaults
4. Tools: must exist in registry
5. References: variables resolve

Error model

```go
type ValidationIssue struct { Code, Message string }
type ValidationError struct { Issues []ValidationIssue }
```

---

## Examples

Research summary

```yaml
name: research_summary
version: "1.0.0"
description: "Research a topic and provide a structured summary"

defaults:
  budget_agent_max: 10000
  require_approval: false

nodes:
  - id: discover
    type: simple
    strategy: react
    tools_allowlist: [web_search]
    budget_max: 1500

  - id: expand
    type: cognitive
    strategy: chain_of_thought
    tools_allowlist: [web_search, web_fetch]
    budget_max: 3000
    depends_on: ["discover"]

  - id: synthesize
    type: cognitive
    strategy: tree_of_thoughts
    budget_max: 5000
    depends_on: ["expand"]

edges:
  - from: discover
    to: expand
  - from: expand
    to: synthesize
```

Complex DAG

```yaml
name: market_analysis
version: "1.0.0"
description: "Parallel market research and analysis"

defaults:
  budget_agent_max: 15000
  require_approval: false

nodes:
  - id: competitors
    type: dag
    budget_max: 6000
    metadata:
      tasks:
        - id: competitor_1
          query: "Analyze competitor A"
          tools_allowlist: [web_search]
        - id: competitor_2
          query: "Analyze competitor B"
          tools_allowlist: [web_search]
        - id: competitor_3
          query: "Analyze competitor C"
          tools_allowlist: [web_search]

  - id: market_trends
    type: cognitive
    strategy: chain_of_thought
    budget_max: 3000

  - id: synthesis
    type: supervisor
    budget_max: 6000
    depends_on: ["competitors", "market_trends"]
```

---

## API Reference

Registration & listing

```go
// Load templates from directory
registry, err := workflows.InitTemplateRegistry(logger, "/path/to/templates")

// Get specific template
entry, found := registry.Get("research_summary@1.0.0")

// List all templates
summaries := registry.List()
```

Execution via OrchestratorService

```
grpcurl -plaintext -d '{
  "query": "Summarize quantum computing",
  "context": { "template": "research_summary", "template_version": "1.0.0" }
}' localhost:50052 shannon.orchestrator.OrchestratorService/SubmitTask
```

---

## Troubleshooting

Templates not loading
- Check `TEMPLATES_PATH` env var
- Validate YAML
- Inspect orchestrator logs for validation errors

Budget exceeded
- Rebalance node budgets
- Rely on automatic pattern degradation
- Check for unnecessary sequential dependencies

Learning router quality
- Temporarily increase exploration
- Reset stale patterns if needed
- Review confidence thresholds

Enable debug logging

```bash
export DEBUG_TEMPLATES=true
export LOG_LEVEL=debug
docker compose logs orchestrator | rg -i template
```

---

## Future

- Hot reload for templates
- Visual editor/marketplace
- A/B testing for template variants
- ML‑based budget allocation
