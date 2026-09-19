# API Reference (LLM Service)

## 📖 中文学习注解

### 本文核心摘要
本文档是 Python LLM Service 的 API 参考，记录了 `/agent/query` 接口的请求/响应格式。请求体包含 query、context（role/model_tier/prompt_params/history/attachments）、agent_id、allowed_tools、model_tier、model_override、max_tokens、temperature 等字段。响应包含 success、response（最终答案文本）、tokens_used、model_used、provider 和 metadata。

### 章节导航
- **POST /agent/query**: 请求体字段说明——query（必填）、context（角色/附件/历史）、allowed_tools（工具白名单语义）、model 相关参数
- **Response**: success/response/tokens_used/model_used/provider/metadata
- **Note**: GPT-5 路由到 Responses API 使用 output_text；Chat provider 对 content list 进行文本拼接防御性归一化

### 与 AI Agent 体系的关联
- 实现位置：`python/llm-service/llm_service/api/agent.py`
- 由 Go Orchestrator 的 Agent 活动通过 HTTP 调用该接口
- allowed_tools 控制工具可用性，与角色预设联动
- attachments 通过 Redis 传递，支持图片/PDF/文本文件

### 阅读建议
需要直接调用 LLM Service 的开发者必读；通过 Gateway 使用的用户可略过，因为 Gateway 会封装该接口。

## POST /agent/query

Request body (fields shown are the most relevant):
- `query` (string) – task or question.
- `context` (object, optional) – additional parameters; may include `role`, `model_tier`, `prompt_params`, `history`, `attachments`.
  - `attachments` (array, optional) – file attachments as `[{id, media_type, filename, size_bytes}]` refs (resolved from Redis by the agent).
- `agent_id` (string, optional) – identifier for observability.
- `allowed_tools` (array of strings, optional) – explicit tool allowlist.
  - Omit or `null` → role presets may enable tools.
  - `[]` → tools disabled.
  - Non‑empty list → only these tools are available (names must match registered tools: built‑in, OpenAPI, MCP).
- `model_tier` (string, optional) – `small|medium|large`.
- `model_override` (string, optional) – provider‑specific model id.
- `max_tokens` (int, optional) – response limit.
- `temperature` (float, optional) – sampling.

Response body:
- `success` (bool)
- `response` (string) – final answer text.
- `tokens_used` (int) – total tokens (prompt + completion).
- `model_used` (string)
- `provider` (string)
- `metadata` (object) – may include `allowed_tools`, `role`.

Notes:
- GPT‑5 models are routed to the Responses API; the server prefers `output_text` when available.
- Chat providers defensively normalize content by joining text parts when a list is returned.

