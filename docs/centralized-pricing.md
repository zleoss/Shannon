# Centralized Pricing Configuration

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Shannon 的集中式定价配置系统——所有服务（Go 编排器、Rust Agent Core、Python LLM Service）共享 `config/models.yaml` 作为定价单一数据源。文档详细介绍了 YAML 中 `pricing` 段的结构（defaults 通用默认值 + models 各 model 输入/输出/合并单价）、Go/Rust/Python 三端的实现位置与使用方式、热重载机制以及验证步骤。

### 章节导航
- **Configuration Structure**: pricing.defaults（未知模型的通用单价）和 pricing.models（按 provider > model_id 细粒度定价）
- **Implementation Details**: Go 端定价计算（pricing.go）、Rust 端（pricing.rs）、Python 端价格获取
- **Hot Reload**: models.yaml 被 config manager 监听，修改后自动重新加载
- **Validation**: 基本校验（禁止负值）和完整的验证脚本

### 与 AI Agent 体系的关联
- 配置源文件：`config/models.yaml` 的 pricing 段
- Go 定价实现：`go/orchestrator/internal/pricing/pricing.go`
- Rust 定价实现：`rust/agent-core/src/pricing.rs`
- 用于每次 Agent 调用的成本计算，记录到 token_usage 表

### 阅读建议
平台运维和计费相关开发者必读；普通应用开发者了解即可。

## Overview

The Shannon platform now has a centralized pricing configuration system that manages model costs across all services (Go orchestrator, Rust agent-core, Python llm-service). All pricing data is maintained in a single source of truth: `config/models.yaml`.

## Configuration Structure

The pricing configuration is defined in `config/models.yaml` under the `pricing` section:

```yaml
pricing:
  defaults:
    combined_per_1k: 0.005  # Default cost per 1K tokens when model is unknown
  models:
    <provider>:
      <model_id>:
        input_per_1k: 0.0005   # Cost per 1K input tokens
        output_per_1k: 0.0015  # Cost per 1K output tokens
        combined_per_1k: 0.005 # Optional: Used when only total tokens are known
```

## Implementation Details

### Go Orchestrator

**Location**: `go/orchestrator/internal/pricing/pricing.go`

- Loads pricing configuration from `config/models.yaml` (or `MODELS_CONFIG_PATH` env var)
- Provides functions:
  - `DefaultPerToken()`: Returns default cost per token
  - `PricePerTokenForModel(model string)`: Returns model-specific cost per token
  - `CostForTokens(model string, tokens int)`: Calculates total cost
  - `CostForSplit(model string, inputTokens, outputTokens int)`: Accurate split pricing
  - `ModifiedTime()`: Returns config file modification time
- Used in:
  - `internal/server/service.go`: Calculates workflow execution costs
  - `internal/activities/session.go`: Updates session cost tracking

Hot reload & validation:

- `models.yaml` is watched by the config manager; on change, pricing is reloaded
- Basic validation ensures no negative values under `pricing` section

Fallback metrics:

- `shannon_pricing_fallback_total{reason="missing_model|unknown_model"}`
  increments whenever defaults are used (missing or unknown model name)

### Rust Agent-Core

**Location**: `rust/agent-core/src/llm_client.rs`

- Function `pricing_cost_per_1k(model)` reads pricing from same config file
- Function `calculate_cost(model, tokens)` uses centralized pricing with fallback
- Attempts to read from:
  1. `MODELS_CONFIG_PATH` environment variable
  2. `/app/config/models.yaml` (Docker container path)
  3. `./config/models.yaml` (local development)
- Falls back to hardcoded heuristics if config unavailable

### Python LLM Service

**Location**: `python/llm-service/llm_provider/manager.py`

- `LLMManager` loads pricing overrides after initializing providers
- Function `_load_and_apply_pricing_overrides()`:
  - Reads from `config/models.yaml` (or `MODELS_CONFIG_PATH`)
  - Applies pricing to provider models matching by model_id or key
- Overrides provider-specific pricing with centralized values
- Maintains compatibility with existing provider configurations

## Cost Calculation Logic

1. **When input/output tokens are known separately**:
   - Uses `input_per_1k` and `output_per_1k` for precise calculation

2. **When only total tokens are known**:
   - Uses `combined_per_1k` if specified
   - Otherwise averages: `(input_per_1k + output_per_1k) / 2`

3. **When model is unknown**:
   - Uses `defaults.combined_per_1k`
   - If config unavailable, falls back to a generic default (e.g., `0.002` per 1K tokens)
   - Increments `shannon_pricing_fallback_total`

## Environment Variables

- `MODELS_CONFIG_PATH`: Override default config file location
  - Used by all services (Go, Rust, Python)
  - Takes precedence over default paths

## Default Paths

Services look for configuration in this order:

1. `$MODELS_CONFIG_PATH` (if set)
2. `/app/config/models.yaml` (Docker containers)
3. `./config/models.yaml` (local development)

## Testing

### Go Testing

```bash
cd go/orchestrator
go test -v ./internal/pricing/...
```

### Manual Verification

The implementation was verified to:

- Load pricing configuration correctly from `config/models.yaml`
- Calculate costs accurately for known models
- Fall back to defaults for unknown models
- Apply pricing overrides in Python LLM service

## Migration Notes

### What Changed

- **Go**: Service uses `pricing.CostForTokens()` / `CostForSplit()` instead of inline heuristics
- **Rust**: `calculate_cost()` checks centralized config before fallback
- **Python**: Manager applies pricing overrides after provider initialization
- **Config**: Added `pricing` section to `config/models.yaml`

### Backward Compatibility

- All services maintain fallback logic for missing configuration
- Existing provider-specific pricing is preserved unless overridden
- No changes to activity/proto signatures required

## Workflow Coverage

For detailed information on which workflows have true per-model costs vs approximations, see [workflow-pricing-coverage.md](workflow-pricing-coverage.md).

### Summary

- **Production Ready** (true costs): Simple, DAG v2, Supervisor, React, Streaming (single & parallel)
- **Using Defaults**: Exploratory, Scientific patterns

## Enhancements

1. **Split token tracking**: Workflows now track input/output tokens separately
2. **Price validation**: Non-negative validation implemented
3. **Hot-reload support**: Pricing reloads on `models.yaml` changes
4. **Model threading**: Production workflows pass model names for accurate costs
5. **Fallback metrics**: `shannon_pricing_fallback_total` tracks coverage

### Potential Improvements

1. **Cost alerts**: Implement threshold notifications when costs exceed limits
2. **Usage analytics**: Track model-specific usage patterns and costs
3. **Pattern extensions**: Add per-agent tracking to Exploratory/Scientific if usage warrants

## Configuration Example

Current pricing for common models:

```yaml
pricing:
  defaults:
    combined_per_1k: 0.005
  models:
    openai:
      gpt-5-2025-08-07:
        input_per_1k: 0.0060
        output_per_1k: 0.0180
      gpt-5-nano-2025-08-07:
        input_per_1k: 0.00010
        output_per_1k: 0.00040
    anthropic:
      claude-sonnet-4-5-20250929:
        input_per_1k: 0.0030
        output_per_1k: 0.0150
      claude-haiku-4-5-20251001:
        input_per_1k: 0.00025
        output_per_1k: 0.00125
    deepseek:
      deepseek-chat:
        input_per_1k: 0.0001
        output_per_1k: 0.0002
```

## Workflow Coverage (True Cost vs. Approximate)

Streaming now reports true per‑model costs (including token splits where available).

- Simple: True cost (per‑model, total tokens)
- DAG v2: True cost (per‑agent, input/output split)
- Supervisor: True cost (per‑agent, input/output split)
- React: True cost (per‑agent, input/output split)
- Streaming (single/parallel): True cost (per‑agent, total tokens; split when provided)
- Exploratory (ToT/Debate/Reflect): Approximate (aggregate totals)
- Scientific (CoT/Debate/ToT/Reflect): Approximate (aggregate totals)

Note: We will upgrade Exploratory/Scientific to true per‑agent costs when these patterns surface model IDs and token splits from their internal agent calls.
