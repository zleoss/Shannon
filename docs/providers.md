# Providers and Routing

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Shannon 中 LLM Provider 的路由和工具门控语义。GPT-5 系列模型路由到 OpenAI Responses API（避免 Chat API 返回空 content）、Chat 内容归一化防御（content 为 list 时提取 text 拼接）、以及 allowed_tools 的三种语义（省略→角色预设决定、空列表→禁用工具、非空列表→仅列出的工具可用）。

### 章节导航
- **GPT-5 Family Routing**: GPT-5 模型路由到 Responses API，优先使用 output_text
- **Chat Content Normalization**: 对 Chat API 返回的 content list 进行 text 提取拼接
- **Tool Gating Semantics**: allowed_tools 的三态语义——省略/空列表/非空列表

### 与 AI Agent 体系的关联
- Provider 路由：`python/llm-service/llm_service/llm_provider/` 下各 provider 实现
- GPT-5 特殊处理：`python/llm-service/llm_service/llm_provider/openai_provider.py`
- 工具门控：`python/llm-service/llm_service/api/agent.py` 中的 allowed_tools 处理

### 阅读建议
集成 LLM Provider 的开发者必读；普通用户重点了解 allowed_tools 三种语义即可。

## GPT‑5 family routing

- GPT‑5 models are routed to the OpenAI Responses API.
- Prefer `output_text` when present to avoid empty content when the API returns structured blocks.
- A low reasoning effort is used during synthesis to encourage producing final text.

Why:
- Some GPT‑5 chat responses return content as structured parts; using Responses API avoids empty `message.content`.

## Chat content normalization (defense in depth)

- For Chat Completions (OpenAI and OpenAI‑compatible), if `message.content` is a list, extract `.text` from each part and join.
- This provides backward compatibility and protects against provider variations.

## Tool gating semantics

- The LLM service expects `allowed_tools` in `/agent/query`.
- Semantics:
  - Omit field → role presets may enable tools.
  - `[]` → tools disabled.
  - `["name", …]` → only those tools are available (built‑in, OpenAPI, or MCP by registered name).

