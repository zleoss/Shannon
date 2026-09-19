# Getting Started with Templates

## 📖 中文学习注解

### 本文核心摘要
本文档是 Shannon 模板工作流的快速入门指南——面向只想快速创建和运行模板的用户。文档以最小化的 YAML 示例（simple_analysis 模板）展示如何创建、加载、列出和执行模板。包含 node type（simple/cognitive/dag/supervisor）和 strategy（react/cot/reflection/debate/tot）的可用选项列表、Loading 方式（InitTemplateRegistry）和 gRPC ListTemplates API。

### 章节导航
- **Create a Template**: 最小化 YAML 示例和字段说明
- **Load Templates**: InitTemplateRegistry 多目录加载
- **List Available Templates**: gRPC ListTemplates API
- **Execute a Template**: 执行模板的完整 curl 示例

### 与 AI Agent 体系的关联
同 templates.md，但篇幅更短、侧重快速启动，适合新用户首次体验模板功能。

### 阅读建议
初次尝试模板工作流的用户优先阅读此文档，之后再阅读 templates.md 深入了解架构。

This short guide shows how to create, load, and run a template‑based workflow (System 1).

## 1) Create a Template

Create a YAML file under `config/workflows/examples/` (or your own folder). Minimal example:

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

## 2) Load Templates

Templates are loaded at orchestrator startup via `InitTemplateRegistry` and can come from one or more directories. See `go/orchestrator/internal/workflows/template_catalog.go`.

## 3) List Available Templates

Use the new gRPC API:

```
grpcurl -plaintext -d '{}' localhost:50052 \
  shannon.orchestrator.OrchestratorService/ListTemplates
```

Response contains `name`, `version`, `key`, and `content_hash`.

## 4) Execute a Template

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

## 5) Best Practices

- Keep nodes small and deterministic; prefer more nodes over large monoliths.
- Restrict tools explicitly per node.
- Set `defaults.require_approval` when human sign‑off is needed.
- Use `extends` to share defaults; verify with `registry.Finalize()`.
