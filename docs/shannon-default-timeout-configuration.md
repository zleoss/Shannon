# Shannon Default Timeout Configuration

## 📖 中文学习注解

### 本文核心摘要
本文档是 Shannon 各模块默认超时配置的参考表。涵盖 Agent 执行超时（默认 30s）、编排器活动超时（分解 30s / 合成 180s / 研究 480s / 压缩 8s 等）、HTTP 客户端超时（工具元数据 2s / gRPC 连接 3s）、LLM Provider 超时（OpenAI/Anthropic/Google 各 300s）、以及 Gateway Web 超时。所有默认值与代码对齐，并标注了环境变量覆写方式。

### 章节导航
- **Agent & Task Execution**: Agent 执行超时（30s）/ 执行强制超时（30s）/ WASI 超时（30s）
- **Orchestrator Activities**: 各活动的超时（分解/合成/研究/验证/压缩 等）
- **HTTP Client Timeouts**: 内部 HTTP 调用和 gRPC 连接的超时配置
- **LLM Provider Timeouts**: OpenAI/Anthropic/Google/Bedrock 的 Provider 超时
- **Gateway Web Timeouts**: Gateway 的读/写/空闲超时

### 与 AI Agent 体系的关联
- 配置代码：Go（`go/orchestrator/` 各文件中硬编码默认值）、Rust（`rust/agent-core/src/config.rs`）
- 环境变量覆写：对应 .env 和 docker-compose 中的 AGENT_TIMEOUT_SECONDS 等
- 影响 Agent 执行、分解、合成等工作流环节的响应时间

### 阅读建议
运维和性能调优开发者必读；重点关注 Agent 和 LLM Provider 超时对用户体验的影响。

Reference values aligned with the current codebase. Defaults noted come from code; sample `.env` and docker‑compose overrides are called out where relevant.

## Agent & Task Execution

| Configuration   | Default (code)   | Env Var                 | Location                                            | Description                                                       |
| --------------- | ---------------- | ----------------------- | --------------------------------------------------- | ----------------------------------------------------------------- |
| Agent Execution | 30s              | AGENT_TIMEOUT_SECONDS   | go/orchestrator/internal/activities/agent.go        | Max runtime per agent execution (set via env; compose defaults 600s) |
| Enforce Timeout | 30s              | ENFORCE_TIMEOUT_SECONDS | rust/agent-core/src/config.rs                       | Agent-core per-request enforcement timeout (sample .env sets 120s) |
| WASI Timeout    | 30s              | WASI_TIMEOUT_SECONDS    | rust/agent-core/src/config.rs                       | Agent-core WASI execution timeout (sample .env sets 60s)          |

## Orchestrator Activities

| Activity             | Default      | Env Var                   | Location                                         | Description                                              |
| -------------------- | ------------ | ------------------------- | ------------------------------------------------ | -------------------------------------------------------- |
| ResearchWorkflow Activities | 480s (8 min) | N/A | go/orchestrator/internal/workflows/strategies/research.go | Default Activity timeout for research workflow (SynthesizeResultsLLM can take 5+ min for deep research) |
| DecomposeTask        | 30s          | DECOMPOSE_TIMEOUT_SECONDS | go/orchestrator/internal/activities/decompose.go | HTTP timeout for task decomposition                      |
| Synthesis (Standard) | 180s (3 min) | N/A                       | go/orchestrator/internal/activities/synthesis.go | Non-research synthesis timeout                           |
| Synthesis (Research) | 300s (5 min) | N/A                       | go/orchestrator/internal/activities/synthesis.go | Research synthesis timeout                               |
| Research Refinement  | 300s (5 min) | N/A                       | go/orchestrator/internal/activities/research_refine.go | Research query refinement timeout                  |
| Session Title        | 15s          | N/A                       | go/orchestrator/internal/activities/session_title.go   | Generate session title timeout                       |
| Verify Activity      | 120s (2 min) | N/A                       | go/orchestrator/internal/activities/verify.go    | Verification HTTP client timeout                         |
| Context Compression  | 8s           | N/A                       | go/orchestrator/internal/activities/context_compress.go | Context compression HTTP timeout                    |

## HTTP Client Timeouts (Internal)

| Client                    | Default             | Location                                    | Purpose                                                     |
| ------------------------- | ------------------- | ------------------------------------------- | ----------------------------------------------------------- |
| Tool Metadata Fetch       | 2s                  | go/orchestrator/internal/activities/agent.go | Best-effort tool metadata HTTP fetch                        |
| Tools List                | 5s                  | go/orchestrator/internal/activities/agent.go | LLM service: list available tools                           |
| Tools Select              | 5s                  | go/orchestrator/internal/activities/agent.go | LLM service: tool selection                                 |
| Agent gRPC                | Agent timeout + 30s | go/orchestrator/internal/activities/agent.go | gRPC call with buffer (e.g., 630s if agent timeout is 600s) |
| Agent Query (forced tool) | 2 min (120s)        | go/orchestrator/internal/activities/agent.go | HTTP client for /agent/query (forced tool path)             |
| Dial Context              | 3s                  | go/orchestrator/internal/activities/agent.go | gRPC connection establishment                               |

## LLM Provider Timeouts (config/models.yaml)

| Provider   | Default | Location          | Notes                                            |
| ---------- | ------- | ----------------- | ------------------------------------------------ |
| OpenAI     | 300s    | config/models.yaml | Long timeout for potentially slow responses      |
| Anthropic  | 300s    | config/models.yaml | Increased from 60s in current config             |
| Google     | 300s    | config/models.yaml | Increased from 60s in current config             |
| Bedrock    | 90s     | config/models.yaml | AWS Bedrock                                      |
| Ollama     | 120s    | config/models.yaml | Local model serving                              |
| Meta/Llama | 90s     | config/models.yaml | Via Together AI                                  |
| DeepSeek   | 60s     | config/models.yaml |                                                  |
| Qwen       | 60s     | config/models.yaml |                                                  |
| XAI        | 60s     | config/models.yaml | Grok models                                      |
| ZAI        | 60s     | config/models.yaml | Custom provider                                  |

Default provider fallback: 60s (python/llm-service/llm_provider/base.py) if not specified.

## Workflow Configuration (via config/features.yaml)

| Setting            | Default      | Location                                          | Description                     |
| ------------------ | ------------ | ------------------------------------------------- | ------------------------------- |
| Reflection Timeout | 5000ms (5s)  | go/orchestrator/internal/activities/config.go     | Reflection activity timeout     |
| Hybrid Dependency  | 360s (6 min) | go/orchestrator/internal/activities/config.go     | Hybrid pattern dependency wait  |
| P2P Coordination   | 360s (6 min) | go/orchestrator/internal/activities/config.go     | Peer-to-peer agent coordination |
| LLM Timeout        | 30s          | rust/agent-core/src/config.rs (.env example 120s) | Agent-core LLM request timeout  |

## Security & Integration

| Configuration       | Default         | Env Var                     | Location                                      | Description                          |
| ------------------- | --------------- | --------------------------- | --------------------------------------------- | ------------------------------------ |
| Approval Timeout    | 1800s (30 min)  | N/A (per-request)           | go/orchestrator/internal/server/service.go    | Human approval wait; override via API |
| OpenAPI Fetch       | 30s             | OPENAPI_FETCH_TIMEOUT       | python/llm-service/llm_service/tools/openapi_tool.py | Fetching OpenAPI specs               |
| MCP Timeout         | 10s             | MCP_TIMEOUT_SECONDS         | python/llm-service/llm_service/mcp_client.py  | MCP tool server calls                |
| Python WASI Session | 3600s (1 hour)  | PYTHON_WASI_SESSION_TIMEOUT | python/llm-service/llm_service/tools/builtin/python_wasi_executor.py | WASI interpreter session lifetime    |

## Circuit Breaker & Resilience

| Setting           | Default | Location                                               | Description                                  |
| ----------------- | ------- | ------------------------------------------------------ | -------------------------------------------- |
| Circuit Reset     | 30s     | go/orchestrator/internal/activities/circuit_breaker.go | Time before attempting to close open circuit |
| Provider Recovery | 60s     | python/llm-service/llm_provider/manager.py            | Provider circuit breaker recovery window     |

## Key Insights

1. Longest defaults: Provider timeouts (OpenAI/Anthropic/Google) at 300s; approval wait at 1800s by default.
2. Shortest defaults: Tool metadata fetch (2s), Dial context (3s), Reflection (5s).
3. Notes:
   - Agent execution default in code is 30s; compose sets 600s by default via env.
   - DecomposeTask default is 30s (override via DECOMPOSE_TIMEOUT_SECONDS).
4. Provider variance: Several major providers (OpenAI, Anthropic, Google) use 300s; most others use 60–120s.

## Recommendations

- Slow LLM APIs: prefer providers configured at 300s where appropriate (OpenAI/Anthropic/Google).
- Complex workflows: set `AGENT_TIMEOUT_SECONDS` to ~600s.
- Production: monitor DecomposeTask latency; keep default 30s unless evidence supports increase.
- Local models (Ollama): 120s default is typically sufficient.

