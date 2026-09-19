# LLM Service Response Caching

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Python LLM Service 的响应缓存机制。Shannon 采用「状态与计算分离」架构——Go Orchestrator 负责所有持久化状态（Qdrant 向量存储、语义搜索），Python LLM Service 为无状态计算层仅维护精确匹配缓存。缓存仅缓存非流式调用的最终响应，使用 Redis（推荐）或内存作为后端，按配置 TTL 自动过期。文档明确了缓存范围（只缓存 exact-match，语义相似性由 Go 负责）。

### 章节导航
- **Separation of State and Compute**: Go 管状态，Python 管计算——单数据源、确定性重放
- **What Is Cached**: 非流式 completions 的最终响应（exact-match 缓存）
- **What Is Not Cached**: 流式 chunk、Provider KV 缓存、语义匹配（Go 负责）
- **Single Cache Strategy**: Manager 层缓存（llm_provider/manager.py），Redis 键前缀 llm:cache:*
- **Backends**: 内存（LRU, 最大 1000 条）和 Redis（TTL 配置, 共享多进程）
- **Configuration**: config/models.yaml 的 prompt_cache 段（enabled + ttl_seconds）

### 与 AI Agent 体系的关联
- 缓存实现：`python/llm-service/llm_service/llm_provider/manager.py`
- 配置位置：`config/models.yaml` 的 prompt_cache 段
- 语义搜索由 Go Orchestrator 通过 Qdrant 实现
- 对于流式请求，缓存响应会作为单 chunk 发射

### 阅读建议
关注 API 响应延迟和成本优化的开发者必读；了解哪些被缓存、哪些不被缓存即可（避免对语义缓存有错误预期）。

This document explains the Python LLM service's exact-match response cache. It does not modify or depend on any provider's internal KV‑cache.

## Architecture: Separation of State and Compute

Shannon follows a clean architecture where:
- **Go Orchestrator** owns all persistent state (Qdrant vector store, session memory, semantic search)
- **Python LLM Service** is stateless compute (provider abstraction with simple exact-match caching only)

This separation ensures:
- Single source of truth for semantic memory
- Deterministic workflow replay in Temporal
- Clean debugging and maintenance

## What Is Cached
- The final response of non‑streaming completion calls.
- When streaming is requested via the manager, cached responses are emitted as a single chunk.
- **Only exact-match caching** - semantic similarity search is handled by Go Orchestrator

## What Is Not Cached
- Partial streaming chunks.
- Provider‑internal attention KV state (not controllable from client).
- Semantic similarity matches (Go Orchestrator's responsibility)

## Single Cache Strategy

Shannon uses **one exact-match cache** at the manager layer for simplicity and consistency:

### Location
- **Manager-layer cache** in `llm_provider/manager.py`
- Applied across **all call paths** (including streaming)
- Uses Redis key prefix: `llm:cache:*`

### Backends
- **In-memory (default)**: LRU behavior, bounded by entry count (max 1000)
- **Redis (recommended)**: Set `REDIS_HOST`/`REDIS_PORT`/`REDIS_PASSWORD` to enable shared cache across processes
  - Key format: `llm:cache:{sha256_hash}`
  - TTL from `prompt_cache.ttl_seconds` (default 3600s)

### Configuration
- YAML: `config/models.yaml`
  - `prompt_cache.enabled` (bool, default true)
  - `prompt_cache.ttl_seconds` (int, default 3600)
  - `prompt_cache.max_cache_size_mb` is informational
- Environment: Set `REDIS_HOST` (and optionally `REDIS_PORT`/`REDIS_PASSWORD`) to use Redis
- Per-request: Override TTL via `CompletionRequest.cache_ttl`

## Cache Key Derivation
By default, the key is a SHA‑256 hash of a stable JSON with:
- `messages` (content only)
- `model_tier`
- `model` (explicit override if provided)
- `temperature`
- `max_tokens`
- `functions`
- `seed`

Notes:
- Parameters not listed above (e.g., `top_p`, `presence_penalty`, `response_format`) are not part of the default key. If your usage depends on them, set `CompletionRequest.cache_key` explicitly.

## Behavior Summary
- On non‑streaming calls: the manager checks cache first; if hit, it returns the cached `CompletionResponse`.
- On streaming calls via `LLMManager.stream_complete(...)`: if a cached result exists, it is emitted as a single text chunk; otherwise, text chunks are yielded from the provider.
- Expiry: entries expire after `ttl_seconds` (YAML) or request override.
- **Semantic similarity matching** is handled by Go Orchestrator before calling the LLM service.

## Metrics & Observability
- Prometheus metrics exposed for requests, tokens, cost, and latency
- Cache hits indicated by `cached` flag in `CompletionResponse`
- Manager logs cache hits with current hit rate (in-memory only; Redis shows 0.0)

## Safety & Consistency
- Cached responses are immutable snapshots of prior outputs; ensure you do not cache prompts containing sensitive/ephemeral data unless intended.
- For tenant‑scoped isolation, include tenant/session identifiers in your cache key by setting `CompletionRequest.cache_key`.
