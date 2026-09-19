# Providers Models Endpoint

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Python LLM Service 的 `/providers/models` 端点——一个实时模型注册表调试接口。通过 GET 请求可查看各 Provider（OpenAI/Anthropic 等）当前可用的模型列表、按 tier（small/medium/large）筛选、验证 config/models.yaml 中的模型配置是否正确加载、快速排查 API Key 和连接等环境问题。文档还包含 workflow stage 的模型覆写说明。

### 章节导航
- **Purpose**: 查看可用模型、验证配置、调试环境问题
- **Endpoint**: GET /providers/models，可选 tier 查询参数
- **Response Schema**: 每个模型的 id/name/tier/context_window/cost/supports_tools/available 等字段
- **Model Override**: 在 workflow 的不同阶段覆写模型（decompose/synthesis/agent 分别指定）

### 与 AI Agent 体系的关联
- 端点实现：`python/llm-service/llm_service/api/providers.py`
- 模型数据源：`config/models.yaml` 的 model_catalog 段
- 用于调试 Provider 连接和模型配置问题

### 阅读建议
运维和调试人员必读；部署后建议用此端点验证模型配置是否正确。

This page documents the live model registry endpoint exposed by the Python LLM service and how to override models for specific workflow stages.

## Purpose

- Inspect which models are currently available per provider (OpenAI, Anthropic).  
- Verify configured models from `config/models.yaml` (model_catalog).  
- Quickly debug environment issues (e.g., API keys, connectivity).  

## Endpoint

- URL: `/providers/models`
- Method: `GET`
- Optional query: `tier=small|medium|large` to filter results by logical tier.

Example:

```bash
# All providers and models
curl http://localhost:8000/providers/models | jq

# Filter by tier
curl "http://localhost:8000/providers/models?tier=small" | jq
```

Response (shape):

```json
{
  "openai": [
    {
      "id": "gpt-5-mini-2025-08-07",
      "name": "gpt-5-mini-2025-08-07",
      "tier": "small",
      "context_window": 128000,
      "cost_per_1k_prompt_tokens": 0.0,
      "cost_per_1k_completion_tokens": 0.0,
      "supports_tools": true,
      "supports_streaming": true,
      "available": true
    }
  ],
  "anthropic": [
    {
      "id": "claude-haiku-4-5-20251001",
      "name": "claude-haiku-4-5-20251001",
      "tier": "small",
      "context_window": 200000,
      "cost_per_1k_prompt_tokens": 0.001,
      "cost_per_1k_completion_tokens": 0.005,
      "supports_tools": true,
      "supports_streaming": true,
      "available": true
    }
  ]
}
```

Notes:
- Models are sourced from `config/models.yaml` (under `model_catalog`) and exposed via this endpoint. Dynamic discovery from provider APIs is not performed.
- Anthropic models use versioned IDs (Claude 4.5 family with dated releases).
- Pricing is centralized in `config/models.yaml`. The LLM service loads pricing from this file for consistent cost tracking across all services.  

## Model Overrides (Per‑Stage)

Set these in the repo root `.env` to override stage‑specific models in the Python service:

```dotenv
# Provider API keys
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=...

# Stage‑specific overrides
COMPLEXITY_MODEL_ID=gpt-5-mini-2025-08-07
DECOMPOSITION_MODEL_ID=gpt-5-2025-08-07
```

- `COMPLEXITY_MODEL_ID`: used by `/complexity/analyze`  
- `DECOMPOSITION_MODEL_ID`: used by `/agent/decompose`  

If unset, the service selects models by tier.  

## Centralized Pricing Configuration

All model pricing is defined in `config/models.yaml` under the `pricing` section:

```yaml
pricing:
  models:
    openai:
      gpt-5-mini:
        input_per_1k: 0.00015
        output_per_1k: 0.0006
```

This single source of truth is used by:
- **Go Orchestrator**: Budget management and cost tracking
- **Rust Agent Core**: Token cost calculation
- **Python LLM Service**: Provider cost reporting

See [Centralized Pricing Documentation](centralized-pricing.md) for details.

## Response Caching (Overview)

The Python LLM service implements client‑side response caching for non‑streaming completions.

- Default: in‑memory LRU with TTL from `config/models.yaml` → `prompt_cache.ttl_seconds`.
- Distributed: enable Redis by setting `REDIS_URL` (or `REDIS_HOST`/`REDIS_PORT`/`REDIS_PASSWORD`).
- Keying: deterministic hash of messages plus key parameters (tier/model/temperature/max_tokens/functions/seed).
- Not KV‑cache: this does not modify provider‑side attention caches.

Details: see [LLM Service Response Caching](llm-service-caching.md).

## Requirements

- Run the Python service:

```bash
cd python/llm-service
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
uvicorn main:app --reload
```

- Ensure relevant API keys are present in `.env` before starting.  

## Troubleshooting

- Empty `openai` results: ensure `OPENAI_API_KEY` is set if you intend to call OpenAI, and verify the OpenAI provider is enabled in your config.  
- Missing models: check `config/models.yaml` for entries under `model_catalog.openai` (or other providers) and that `MODELS_CONFIG_PATH` points to the intended file.  
- Anthropic missing: set `ANTHROPIC_API_KEY` if using Anthropic and verify `model_catalog.anthropic` entries exist.  

## Model References

- OpenAI (GPT‑5 family)
  - GPT‑5: https://platform.openai.com/docs/models/gpt-5
  - GPT‑5 mini: https://platform.openai.com/docs/models/gpt-5-mini
  - GPT‑5 nano: https://platform.openai.com/docs/models/gpt-5-nano

- Anthropic (Claude 4.x)
  - Claude models overview: https://docs.claude.com/en/docs/about-claude/models/overview
    - Claude Sonnet 4.5
    - Claude Haiku 4.5
    - Claude Opus 4.1
