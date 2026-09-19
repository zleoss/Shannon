# Troubleshooting

## 📖 中文学习注解

### 本文核心摘要
本文档收集 Shannon 平台常见问题的排查方法。主要涵盖：Token 计数 > 0 但结果为空（GPT-5 Responses API 路由/缓存空响应/历史状态覆写）、工具意外启用或禁用（allowed_tools 三态语义）、会话结果在历史中不可见（单次保存原子操作）等典型问题的症状、原因、修复步骤和验证方法。

### 章节导航
- **Tokens count > 0 but result is empty**: GPT-5 Chat API 返回结构化 parts / 缓存空响应 / 历史状态覆写
- **Tools unexpectedly enabled or disabled**: allowed_tools 三态语义（省略/空列表/非空列表）
- **Session result not visible in history**: 历史状态非原子覆写的修复
- **Common Error Patterns**: 各错误模式及对应的排查路径

### 与 AI Agent 体系的关联
- 问题涉及模块：GPT-5 路由（Python）、allowed_tools 处理（Python + Rust）、Session 管理（Go）
- 排查需要查看 Temporal 历史、Redis 缓存和 PG event_logs

### 阅读建议
遇到对应问题的开发者必读；日常可快速翻阅了解常见问题。

## Tokens count > 0 but result is empty

Symptoms:
- Database shows `completion_tokens` > 0 but `result` is an empty string.
- Temporal/Agent‑Core logs report large token counts.
- Session history may be missing the assistant message.

Causes and fixes:
- GPT‑5 chat responses can return content as structured parts, not a plain string. Fix by routing GPT‑5 to the Responses API and preferring `output_text` (PR #67). Defensive parsing was also added for Chat API paths to join content parts.
- Cached empty response from a previous buggy parse. Clear llm‑service cache or restart the service after applying the fixes.
- Orchestrator session stale‑overwrite (historical): A save after `AddMessage` could clobber history. Fixed by appending the assistant message directly to `sess.History` and saving once.

How to verify:
- Run a complex prompt (multi‑paragraph). Ensure `result` length > 0 and session history includes the assistant entry.
- For GPT‑5 models, confirm the provider path uses Responses API.

## Tools unexpectedly enabled or disabled

Symptoms:
- The model selects or executes tools you did not intend; or it never calls tools when expected.

Cause:
- The LLM service expects `allowed_tools` in the request. Previously, Agent‑Core sent a `tools` field (ignored by the API), allowing role presets to influence tools.

Fix:
- Agent‑Core now sends `allowed_tools`. Semantics:
  - Omit field (or `null`) → role preset may allow tools.
  - Empty list `[]` → tools disabled for this request.
  - Non‑empty list → only those tools are available.

## Session result not visible in history

Cause:
- Historical stale‑overwrite during session update.

Fix:
- Update happens on a single in‑memory session struct: append assistant message, set context/metadata, then save once.

