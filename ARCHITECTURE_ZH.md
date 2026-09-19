# Shannon 项目架构总览（中文导读）

> 本文档面向 Shannon 项目的学习者，从最外层俯瞰整个多语言 AI Agent 平台的代码结构、每个目录与核心文件在 AI Agent 体系中的角色定位、跨服务调用关系以及推荐学习路径。
>
> 侦察依据：基于对 `go/`、`rust/`、`python/`、`protos/`、`config/`、`docs/` 的代码扫描精确到行号后整理，所有 `file_path:line_number` 引用均可在仓库中实地核对。

---

## 0. 一句话理解

**Shannon = 一个企业级、分布式、多语言的"AI Agent 编排平台"**：用 **Go + Temporal** 做工作流编排（大脑），**Python** 做 LLM 调用与工具执行（手脚），**Rust** 做执行网关与沙箱（免疫系统），三者通过 **gRPC + HTTP** 协作，形成"接收请求 → 复杂度路由 → 多 agent 执行 → 工具调用 → 沙箱隔离 → 结果合成"的端到端 Agent 闭环。

---

## 1. 顶层目录结构与作用

```
Shannon/
├── go/orchestrator/    # Go 编排器：HTTP 网关 + Temporal Workflow + gRPC server
├── rust/agent-core/    # Rust Agent 内核：执行网关 + WASI 沙箱 + gRPC server
├── rust/firecracker-executor/  # Rust 微 VM 执行器（Python 代码隔离）
│   └── guest-agent/    # 运行在 microVM 内的 vsock server 子 crate
├── python/llm-service/ # Python LLM 服务：LLM Provider + 工具系统 + Agent Loop
├── python/playwright-service/  # Python 浏览器自动化服务
├── protos/             # 所有 protobuf 定义（跨语言契约）
├── config/             # 热重载 YAML 配置（models.yaml / shannon.yaml / templates/ ...）
├── docs/               # 官方架构文档（91 篇 Markdown）
├── deploy/compose/     # Docker Compose 编排
├── migrations/         # Postgres 数据库迁移脚本
├── scripts/            # 安装/生成脚本（buf、proto、smoke 等）
├── tests/              # 端到端与集成测试
├── clients/            # 多语言客户端 SDK
├── examples/           # 示例代码
├── desktop/            # Tauri 桌面客户端（前端 + Rust）
└── Makefile            # 顶层构建/测试/CI 命令入口
```

### AI Agent 体系中的角色映射

| 模块 | AI Agent 体系中的角色 | 类比 |
|---|---|---|
| `go/orchestrator/cmd/gateway` | **对外 API 关卡**：接收用户请求并鉴权 | 大脑的入口神经 |
| `go/orchestrator/internal/workflows` | **规划与编排**：复杂度路由 → workflow 选型 → fan-out/合成 | 大脑的额叶决策区 |
| `go/orchestrator/internal/activities` | **原子动作执行器**：执行 agent 单步、调用 LLM、合成、持久化 | 大脑的运动皮层 |
| `go/orchestrator/internal/budget` | **预算/Token 经济**：防止失控消耗 | 大脑的"成本意识" |
| `go/orchestrator/internal/streaming` | **SSE 实时输出**：把执行过程实时推给前端 | 神经信号传递 |
| `python/llm-service/llm_provider` | **多厂商 LLM 接入**：统一封装 OpenAI/Anthropic/Google/... | 多语种说话能力 |
| `python/llm-service/llm_service/api/agent.py` | **Agent 推理循环**：tool call 决策 → 执行 → 反馈 | 单个聪明小工 |
| `python/llm-service/llm_service/tools` | **工具系统**：内置/MCP/OpenAPI/插件，扩展能力边界 | 小工的工具箱 |
| `python/playwright-service/` | **浏览器执行**：自动化操作网页 | 小工的浏览器 |
| `rust/agent-core` | **执行网关 + WASI 沙箱**：拦截危险工具、限流、隔离代码执行 | 免疫系统 + 安全工房 |
| `rust/firecracker-executor` | **Firecracker microVM 隔离**：更高强度的 Python 代码隔离 | 高度隔离的安全工房 |
| `protos/` | **跨语言契约**：所有 gRPC 接口的单一真相源 | 跨语种翻译协议 |
| `config/` | **运行时配置**：模型、定价、模板、Skills 热重载 | 行为参数表 |

---

## 2. 整体架构：多服务协作图

```
                          ┌─────────────────────────────────────────────────┐
                          │   客户端（OpenAI SDK / 浏览器 / 自研 App）         │
                          └─────┬───────────────────────────────────┬───────┘
                                │ HTTP POST                          │ SSE/WS
                                ▼                                    ▼
                  ┌─────────────────────────┐         ┌──────────────────────────┐
                  │ Go: :8080 — Gateway     │         │ Go: :8081 — Admin HTTP  │
                  │ /v1/chat/completions    │         │ /events /stream/auth      │
                  │ /v1/completions  (proxy)│         │ /approval /timeline      │
                  │ /api/v1/tasks*         │         │ /websocket /daemon        │
                  │ /api/v1/tools*         │         │ httpapi/*.go              │
                  └────────────┬────────────┘         └─────────────┬─────────────┘
                               │ gRPC :50052                       │
                               ▼                                    │
                  ┌────────────────────────────────────────────────┴───────────┐
                  │ Go: :50052 + Temporal — Orchestrator 进程                  │
                  │                                                              │
                  │   main.go                                                    │
                  │     ├─ Temporal Worker ──┐                                    │
                  │     │   注册 workflows/activities                            │
                  │     │                    │                                    │
                  │     ├─ gRPC: OrchestratorService / SessionService           │
                  │     │   / StreamingService                                    │
                  │     │                                                         │
                  │     └─ 子系统：budget / pricing / streaming / skills / daemon │
                  │                  │                                            │
                  │   workflows/orchestrator_router.go                            │
                  │     → 复杂度路由 → strategy workflow                          │
                  │                                                              │
                  └─────────┬──────────────────────────┬───────────────────────────┘
                            │ HTTP 调用 Python          │ gRPC 调用 Rust
                            ▼                          ▼
       ┌────────────────────────────────┐    ┌────────────────────────────────────┐
       │ Python: :8000 — LLM Service     │    │ Rust: :50051 — agent-core          │
       │                                  │    │                                     │
       │ FastAPI (main.py)                │    │ tonic gRPC server (main.rs)         │
       │   ├─ /agent/query   (单轮带tools)│    │   gRPC Services:                    │
       │   ├─ /agent/loop    (agent 单步)  │    │     - AgentService (工具执行网关)   │
       │   ├─ /tools/*       (工具管理)    │    │     - SandboxService (文件/命令沙箱) │
       │   ├─ /completions   (OpenAI proxy│    │   核心能力:                          │
       │   ├─ /embeddings    (向量化)      │    │     - RequestEnforcer (限流/熔断/超时) │
       │   └─ /complexity    (复杂度分析)  │    │     - WasiSandbox (wasmtime 沙箱)   │
       │                                  │    │     - ToolExecutor (路由到 firecracker │
       │ llm_provider/manager             │    │         或 WASI 或直执)           │
       │   ├─ OpenAIProvider               │    │     - WorkspaceManager + MemoryManager │
       │   ├─ AnthropicProvider            │    │                                     │
       │   ├─ GoogleProvider    ...         │    └──────┬──────────────────────┘
       │                                  │           │ HTTP :9001 (POST /execute)
       │ llm_service/tools/                │           ▼
       │   ├─ builtin/*  (web/file/calc/   │    ┌────────────────────────────────────┐
       │   │   python/browser/...)          │    │ Rust Firecracker Executor           │
       │   ├─ mcp.py    (远程 MCP 工具)     │    │   vm_pool.rs + vm_runner.rs          │
       │   ├─ openapi_tool.py (OpenAPI→Tool)│    │   维护 warm pool，每 session 独占 VM  │
       │   └─ plugin_loader.py (热重载)    │    │   通过 vsock UDS 与 guest-agent 通信  │
       │                                  │    │   guest-agent 内执行 Python 代码      │
       │ llm_service/roles/                │    │                                      │
       │   ├─ deep_research/*              │    └────────────────────────────────────┘
       │   └─ swarm/* (Lead/Agent 协议)    │
       │                                  │  另：Python llm-service 通过 gRPC (client)
       │ llm_service/events.py             │  → 调用 Rust agent-core 的 AgentService /
       │   → HTTP POST orchestrator:8081   │  SandboxService（WASI 沙箱执行）
       │     /events (推送执行事件)         │
       └──────────────────────────────────┘
                                  ▲
                                  │ HTTP（BrowserTool 客户端）
                                  │
                                  ▼
                  ┌────────────────────────────────────┐
                  │ Python: :9100 — Playwright Service │
                  │   app.py + session_manager.py      │
                  │   浏览器会话复用、SSRF 防护、滑块消除 │
                  └────────────────────────────────────┘

外部依赖：PostgreSQL（task_executions 等持久化）/ Redis（session/streaming/cache 分发）/
         Temporal（workflow 持久化）/ Qdrant（可选向量库）
```

### 跨语言调用关系速查

| 发起方 | 接收方 | 协议 | 入口 | 用途 |
|---|---|---|---|---|
| 客户端 | Go gateway | HTTP | `POST /api/v1/tasks` 等 | 接受任务 |
| Go gateway | Go orchestrator | gRPC | `OrchestratorService.SubmitTask` | 触发 workflow |
| Go orchestrator | Python llm-service | HTTP | `/agent/query`、`/agent/loop`、`/tools/*`、`/complexity` | 单步 agent、调用 LLM、工具管理 |
| Go orchestrator | Rust agent-core | gRPC | `AgentService.ExecuteTask`、`SandboxService.*` | WASI 沙箱、文件/命令隔离 |
| Python llm-service | Rust agent-core | gRPC (client) | `AgentService.ExecuteTask`、`SandboxService.FileRead/...` | `PythonWasiExecutorTool` 通过 gRPC 调 WASI 沙箱 |
| Python llm-service | Python playwright-service | HTTP | `POST /browser/action`、`POST /capture` | `BrowserTool` 调浏览器 |
| Python llm-service | Go orchestrator | HTTP | `POST /events` (EventEmitter) | 把 LLM/tool 事件回推 |
| Rust agent-core | Rust firecracker-executor | HTTP | `POST /execute` | 高隔离 Python 执行 |
| Rust firecracker-executor | guest-agent | vsock UDS | port 5005 单行 JSON | VM 内 Python 执行 |
| 所有服务 | Postgres / Redis / Qdrant | 各自协议 | — | 状态、缓存、向量 |

---

## 3. Go Orchestrator 详解

### 3.1 进程结构：**两个进程**

| 进程 | 监听 | 入口 | 作用 |
|---|---|---|---|
| Gateway | `:8080` | `go/orchestrator/cmd/gateway/main.go:36` | 对外 HTTP API 关卡：注册路由以方法前缀语法（Go 1.22 `http.ServeMux`），装配 handler + middleware，gRPC 调用 orchestrator |
| Orchestrator | gRPC `:50052`、admin HTTP `:8081` | `go/orchestrator/main.go:51` | Temporal Worker + gRPC service 实现 + 子系统初始化（budget/pricing/streaming/skills/daemon/embeddings/vectordb） |

### 3.2 4 个对外 HTTP API 端点

| 端点 | 是否经过编排 | handler | 用途 |
|---|:---:|---|---|
| `POST /v1/chat/completions` | ✅ | `cmd/gateway/internal/openai/handler.go:84` `ChatCompletions` | OpenAI 兼容、auto 工具选择/深度研究/swarm/strategy |
| `POST /v1/completions` | ❌ 薄代理 | `cmd/gateway/internal/openai/handler.go:644` `Completions` | 直接转发给 LLM，单次调用、无编排 |
| `POST /api/v1/tasks` | ✅ | `cmd/gateway/internal/handlers/task.go:510` `SubmitTask` | Shannon 原生同步任务 |
| `POST /api/v1/tasks/stream` | ✅ | `cmd/gateway/internal/handlers/task.go:626` `SubmitTaskAndGetStreamURL` | Shannon 原生 SSE |

**关键原则**：`/v1/completions` **不**经 Temporal workflow，没有工具选择/任务分解/strategy。需要编排能力必须走 `/v1/chat/completions` 或 `/api/v1/tasks*`。

### 3.3 路由处理注册位置

所有路由在 `cmd/gateway/main.go:207` 的 `http.NewServeMux()` 上注册，关键映射：

| 路由 | mux 注册位置 | handler 函数 |
|---|---|---|
| `POST /api/v1/tasks` | `cmd/gateway/main.go:264` | `taskHandler.SubmitTask` |
| `POST /api/v1/tasks/stream` | `cmd/gateway/main.go:279` | `taskHandler.SubmitTaskAndGetStreamURL` |
| `GET /api/v1/tasks/{id}/stream` | `cmd/gateway/main.go:315` | `taskHandler.StreamTask` |
| `POST /api/v1/tasks/{id}/cancel` | `cmd/gateway/main.go:337` | `taskHandler.CancelTask` |
| `POST /api/v1/tasks/{id}/pause` | `cmd/gateway/main.go:352` | `taskHandler.PauseTask` |
| `POST /api/v1/tasks/{id}/resume` | `cmd/gateway/main.go:367` | `taskHandler.ResumeTask` |
| `GET /api/v1/tools` | `cmd/gateway/main.go:736` | `toolsHandler.ListTools` |
| `GET /api/v1/tools/{name}` | `cmd/gateway/main.go:746` | `toolsHandler.GetTool` |
| `POST /api/v1/tools/{name}/execute` | `cmd/gateway/main.go:756` | `toolsHandler.ExecuteTool` |
| `POST /v1/chat/completions` | `cmd/gateway/main.go:775` | `openaiHandler.ChatCompletions` |
| `POST /v1/completions` | `cmd/gateway/main.go:788` | `openaiHandler.Completions` |
| `GET /v1/models` | `cmd/gateway/main.go:803` | `openaiHandler.ListModels` |

**重要规则**：危险工具（`bash_executor`、`file_write`）在 gateway 层被默认阻断，仅在编排工作流内可用。

### 3.4 Temporal Workflow 路由器：项目大脑

**入口**：`internal/workflows/orchestrator_router.go:32` `OrchestratorWorkflow` — 所有任务真正进入编排的开始。

**决策序列（按顺序短路）**：
1. 模板命中 → `TemplateWorkflow`
2. `skip_synthesis=true` → `AgentWorkflow`
3. `force_swarm=true` → `SwarmWorkflow`（早路由 `:272`）
4. `force_research=true` → `ResearchWorkflow`（早路由 `:322`，含 HITL plan review）
5. **学习路由器**（`recommendStrategy` `:1414`） → 历史决策推荐
6. `DecomposeTask` → 复杂度评分 + 多子任务分解
7. 预算预检 `BudgetPreflight`（`:794-837`）
8. 角色处理（`agent/role/browser_use`）
9. 认知策略识别
10. **主 switch**（`:1021-1149`）：
    - `case isSimple && !forceP2P` → `SimpleTaskWorkflow`（复杂度 < 0.3 且单子任务）
    - `case false:` → `SupervisorWorkflow`（**已禁用**，所有多任务全走 DAG）
    - `default` → `DAGWorkflow`

**策略路由子函数** `routeStrategyWorkflow`（`:1275`）处理 `simple/react/exploratory/research/scientific/browser_use`，**DAG 不在其中**（DAG 走主 switch 的 `default`）。

### 3.5 Workflow 与触发条件总表

| Workflow | 文件:行号 | 触发条件 | 在 AI Agent 体系中的作用 |
|---|---|---|---|
| `OrchestratorWorkflow` | `internal/workflows/orchestrator_router.go:32` | 唯一入口 | 路由分发器（大脑的总调度） |
| `SimpleTaskWorkflow` | `internal/workflows/simple_workflow.go:22` | 复杂度 < 0.3 且单子任务 | 单 agent 直执无 fan-out |
| `SupervisorWorkflow` | `internal/workflows/supervisor_workflow.go:43` | **已禁用** (`case false:`) | 历史遗留的计划 supervisor |
| `StreamingWorkflow` | `internal/workflows/streaming_workflow.go:21` | `EnableStreamingWorkflows` | 流式 agent 执行 |
| `ParallelStreamingWorkflow` | `internal/workflows/streaming_workflow.go:312` | 同上 | 并行流式 agent |
| `TemplateWorkflow` | `internal/workflows/template_workflow.go:66` | context 携带 `template` 名 | 按 YAML 模板计划执行 |
| `SwarmWorkflow` | `internal/workflows/swarm_workflow.go:1737` | `force_swarm: true` | 多 agent 持久 swarm，Lead 规划 |
| `AgentLoop`（swarm 子） | `internal/workflows/swarm_workflow.go:488` | SwarmWorkflow 内每个 agent | swarm agent 个体循环 |
| `AgentWorkflow` | `internal/workflows/agent_workflow.go:42` | context 携带 `agent: <id>` | 单一确定性 agent |
| `DAGWorkflow` (**默认**) | `internal/workflows/strategies/dag.go:27` | 所有非简单多任务 | fan-out/fan-in 合成 |
| `ReactWorkflow` | `internal/workflows/strategies/react.go:22` | `strategy == "react"` | 推理循环（ReAct） |
| `ResearchWorkflow` | `internal/workflows/strategies/research.go` | `force_research` 或 `strategy == "research"` | 深度研究 + HITL plan |
| `ExploratoryWorkflow` | `internal/workflows/strategies/exploratory.go:19` | `strategy == "exploratory"` | Tree-of-Thoughts 探索 |
| `ScientificWorkflow` | `internal/workflows/strategies/scientific.go:21` | `strategy == "scientific"` | 假设-实验-评估 |
| `BrowserUseWorkflow` | `internal/workflows/strategies/browser_use.go:26` | `role=="browser_use"` 或 strategy | 浏览器自动化编排 |
| `DomainAnalysisWorkflow` | `internal/workflows/strategies/domain_analysis_workflow.go:83` | 由研究 workflow 子调用 | 域内特定分析 |
| `ScheduledTaskWorkflow` | `internal/workflows/scheduled/scheduled_task_workflow.go` | Temporal Schedule 触发 | 定时任务包装 |

Workflows 注册位置：`internal/registry/registry.go:42` `RegisterWorkflows`；Activities 注册：`internal/registry/registry.go:91` `RegisterActivities`。

### 3.6 关键 Activities（原子动作执行器）

| Activity | 位置 | 作用 |
|---|---|---|
| `ExecuteAgent` | `internal/activities/agent.go:1941` | 调用 Python `/agent/loop`，执行一次 agent 单步 |
| `ExecuteAgentWithForcedTools` | `internal/activities/agent.go:1961` | 强制指定工具集 |
| `ExecuteSimpleTask` | `internal/activities/simple_task.go:41` | 简单任务直接执行 |
| `AgentLoopStep` | `internal/activities/agent_loop.go:133` | swarm 子 agent 单步 |
| `LeadDecision` | `internal/activities/lead.go:122` | swarm lead 决策 |
| `SynthesizeResults` | `internal/activities/synthesis.go:456` | 结果合成 |
| `DecomposeTask` | `internal/activities/decompose.go:51` | 调 Python `/complexity`，输出 `ComplexityScore + Mode + CognitiveStrategy` |
| `PersistAgentExecution` | `internal/activities/persistence.go:79` | 写 `agent_executions` 表 |
| `PersistToolExecution` | `internal/activities/persistence.go:183` | 写 `tool_executions` 表 |
| `EmitTaskUpdate` | `internal/activities/stream_events.go` | 发布流式事件 |
| `BudgetActivities.CheckTokenBudget*` | `internal/activities/budget.go:79/110/151` | 预算预检三模式（普通/背压/熔断） |
| `RequestApproval` | `internal/activities/human_intervention.go` | HITL 审批 |

### 3.7 预算/复杂度/定价/Agent 命名等子系统

| 子系统 | 关键文件:行号 | 作用 |
|---|---|---|
| **预算管理** | `internal/budget/manager.go:82` `BudgetManager` / `:191` `CheckBudget` / `:739` `CheckBudgetWithBackpressure` / `:960` `CheckBudgetWithCircuitBreaker` | token 上限、背压、熔断器、优先级 |
| **复杂度阈值** | `internal/workflows/orchestrator_router.go:103-106` (`simpleThreshold=0.3` 默认) | 路由器简单/复杂分流 |
| **定价热重载** | `internal/pricing/pricing.go:160` `Reload` / `:181` `PricePerTokenForModel` / `:270` `CostForSplitWithCache` | 来自 `config/models.yaml` |
| **模型/Provider 识别** | `internal/models/provider.go:18` `DetectProvider` | Go 端识别 model → provider |
| **编排骨干类型** | `internal/models/types.go:29` `TaskRequest` / `:50` `ComplexityScore` / `:62` `AgentTask` / `:72` `AgentResult` / `:92` `TokenUsage` | 跨 activity 数据载体 |
| **Agent 命名**（强制规则） | `internal/agents/names.go:82` `GetAgentName(workflowID, index)` | **唯一推荐入口**（CLAUDE.md 强制要求），固定日本车站名池保证 Temporal replay 确定性 |
| **角色 → 工具集** | `internal/roles/cache.go` `roles.AllowedTools(role)` | 强制工具与浏览器角色 |
| **DAL** | `internal/db/client.go:81` `NewClient` / `:333` `QueueWrite`（异步批量写）/ `:438` `WithTransactionCB`（熔断事务） | sqlx + 异步批量 |
| **主表实体** | `internal/db/models.go:80` `TaskExecution` / `:138` `AgentExecution` / `:162` `ToolExecution` / `:186` `SessionArchive` | `task_executions` 是核心表 |
| **Session（Redis）** | `internal/session/manager.go:98` `CreateSession` / `:187` `GetSession` | 会话状态以 Redis 为主，归档写库 |
| **Skills 加载** | `internal/skills/registry.go:14` `NewRegistry` / `:23` `LoadDirectory` / `:108` `Get` | YAML skills 热重载 |
| **Streaming** | `internal/streaming/manager.go` `Get()` / `InitializeRedis` | Redis pubsub + ring buffer |
| **Templates** | `internal/templates/registry.go` 等 | TemplateWorkflow 用的 YAML 模板注册 |
| **Daemon（Shan CLI）** | `internal/daemon/hub.go:57` `NewHub` / `:197` `Dispatch` / `:388` `HandleReply` | WebSocket + Redis dispatch 桥 |
| **Embeddings / VectorDB** | `internal/embeddings/service.go` / `internal/vectordb/client.go` | RAG 检索子系统 |
| **学习路由器** | `internal/workflows/orchestrator_router.go:1414` `recommendStrategy` | 基于历史记录的策略推荐 |

### 3.8 gRPC Service 实现

| Service | 位置 | 暴露方法 |
|---|---|---|
| `OrchestratorService` | `internal/server/service.go:362` `SubmitTask` | 任务提交、查询、取消、暂停、恢复 |
| `SessionService` | 同文件附近 | session 管理 |
| `StreamingService` | 同文件附近 | 流式推送 |
| `httpapi.*` | `internal/httpapi/streaming.go / events_ingest.go / timeline.go / websocket.go / approval.go / auth.go / daemon_signal.go` | admin HTTP `:8081` 路由 |

---

## 4. Rust Agent-Core 详解

### 4.1 模块结构（无子目录，扁平 `src/`）

| 文件 | 一句话职责 | AI Agent 体系中位置 |
|---|---|---|
| `main.rs` | tonic server 启动，注册 AgentService + SandboxService + reflection | 进程入口 |
| `lib.rs` | `pub mod` 汇总，`wasi` feature 门控 `sandbox`/`wasi_sandbox` | 模块总成 |
| `grpc_server.rs` | `AgentService` 实现，工具调用网关；FSM 已移除 | 工具执行关卡 |
| `sandbox_service.rs` | `SandboxService` 实现：文件的读/写/列表/搜索/编辑/删除 + 命令执行（路径隔离） | 会话工作区持久层 |
| `enforcement.rs` | **执行网关核心**：令牌桶限流、滚动窗口熔断器、per-request 超时、token 上限、可选 Redis 分布式限流 | 免疫系统 |
| `wasi_sandbox.rs` | wasmtime 沙箱：挂 `/workspace` + `/memory`，fuel/epoch 限制 | WASM 安全执行 |
| `sandbox.rs` | 备用沙箱（`setrlimit` 资源限制），被 `wasi` feature 门控 | 备用沙箱 |
| `tools.rs` | `ToolExecutor`：路由（calculator 本地 / firecracker / WASI），LLM 服务 `/tools/list` 调用 | 工具路由与执行 |
| `tool_registry.rs` | `ToolCapability`/`ToolDiscoveryRequest`，供 `DiscoverTools`/`GetToolCapability` | 工具能力注册 |
| `tool_cache.rs` | 工具结果 TTL 缓存 | 工具结果加速 |
| `safe_commands.rs` | "安全命令"枚举（ls/cat/head/wc/mkdir/rm/...），元字符黑名单，**用 Rust 原生替代 shell 调用** | 命令注入防御 |
| `firecracker_client.rs` | Firecracker executor HTTP 单例客户端，503 触发 WASI fallback | 高隔离后端对接 |
| `llm_client.rs` | LLM 服务 HTTP 客户端 | 内部协作 |
| `workspace.rs` | `WorkspaceManager`（每 session 独立目录、配额） | 会话工作区 |
| `memory_manager.rs` | `MemoryManager`（按 `user_id` 隔离） | 用户记忆持久化 |
| `memory.rs` | `MemoryPool`（512MB + 后台 sweeper） | 内存池 |
| `config.rs` | `Config`、`EnforcementConfig`、`WasiConfig`、`FirecrackerExecutorConfig` 等，全局单例 | 配置中心 |
| `metrics.rs` | Prometheus 指标导出 | 可观测性 |
| `tracing.rs` | OpenTelemetry tracing 初始化 | 可观测性 |
| `proto.rs` | `shannon.common` proto 模块（疑似遗留） | — |
| `error.rs` | 错误类型 | — |
| `build.rs` | `tonic-build` 编译 `common/agent/sandbox` 三个 `.proto` 并产出 `shannon_descriptor.bin` 用于反射 | 构建脚本 |

### 4.2 gRPC 接口（`agent.proto`）

```protobuf
service AgentService {
  rpc ExecuteTask(ExecuteTaskRequest) returns (ExecuteTaskResponse);
  rpc StreamExecuteTask(ExecuteTaskRequest) returns (stream TaskUpdate);
  rpc GetCapabilities(GetCapabilitiesRequest) returns (GetCapabilitiesResponse);
  rpc HealthCheck(HealthCheckRequest) returns (HealthCheckResponse);
  rpc DiscoverTools(DiscoverToolsRequest) returns (DiscoverToolsResponse);
  rpc GetToolCapability(GetToolCapabilityRequest) returns (GetToolCapabilityResponse);
}
```

handler 实现：`impl AgentService for AgentServiceImpl` 在 `grpc_server.rs:988`。各 RPC：
- `ExecuteTask` — `grpc_server.rs:989` `execute_task`：主执行入口，串接 enforcement → 工具直执/多工具/firecracker 路由 + workspace 配额预检
- `StreamExecuteTask` — `grpc_server.rs:1472`：流式执行 `Stream<TaskUpdate>`
- `GetCapabilities` — `grpc_server.rs:1702`：能力声明
- `HealthCheck` — `grpc_server.rs:1739`
- `DiscoverTools` — `grpc_server.rs:1759`
- `GetToolCapability` — `grpc_server.rs:1772`

### 4.3 Enforcement Gateway（免疫系统）

所有运行时安全控制在单文件 `enforcement.rs`+ 配置 `config.rs:373` `EnforcementConfig`：

| 关注点 | 文件:行号 | 作用 |
|---|---|---|
| 主结构 `RequestEnforcer` | `enforcement.rs:15` | 持桶表、滚动窗口表、可选 Redis |
| 综合入口 `enforce` | `enforcement.rs:83` | 顺序：token 上限 → rate_check → circuit_breaker → timeout 包装回调 → cb_record |
| 本地限流（令牌桶） | `enforcement.rs:45` `rate_check` / `:208` `TokenBucket` | per-key RPS |
| 分布式限流（Redis） | `enforcement.rs:150` `RedisLimiter` / `:189` `try_take` | Lua 脚本原子令牌桶 |
| 熔断器 | `enforcement.rs:64` `cb_allow` / `:75` `cb_record` / `:240` `RollingWindow` | 错误率阈值 |
| 超时 | `enforcement.rs:112-121` `tokio::time::timeout` | per-request |
| 工具白名单二级校验 | `grpc_server.rs:155-160`、`:476-491`、`:531-544` | `available_tools` × context `allowed_tools` |
| Workspace 配额 | `grpc_server.rs:1119-1141`（预检）、`sandbox_service.rs:246-275` | 默认 500MB |
| **安全命令拦截** | `safe_commands.rs:12-49` `SafeCommand` 枚举、`:53-54` `DANGEROUS_PATTERNS` | 元字符 `| ; && || > < >> $( \` 直接拒；命令仅白名单 |

### 4.4 WASI 沙箱（核心安全特性）

`wasi_sandbox.rs:17` `pub struct WasiSandbox`：
- **Engine 配置**（`:41-78`）：`wasm_reference_types` + `wasm_bulk_memory` + `consume_fuel` + `epoch_interruption`；64MB 内存 guard
- **挂载点**：会话工作空间→`/workspace`（`DirPerms::all()`），用户记忆→`/memory`
- **资源限制**：`StoreLimitsBuilder`（`:405`）限制 `instances/tables/memories`、`table_elements`
- **超时**：`epoch_interruption` 与 `tokio::time::timeout` 双重
- **系统调用**：`wasmtime_wasi::preview1::add_to_linker_sync`（`:455`）注入 WASI preview1
- **feature gate**：`lib.rs:15-16`、`:24-25` `#[cfg(feature="wasi")]` 门控 `sandbox`/`wasi_sandbox`

### 4.5 Firecracker Executor（高隔离 Python 沙箱）

`rust/firecracker-executor/` 是独立 HTTP 服务（默认 `:9001`），用 Firecracker microVM 给 Python 代码提供一进程一 VM 的最高隔离：

| 关注点 | 文件:行号 |
|---|---|
| HTTP 路由 | `src/main.rs:524-531`：`/execute`、`/workspace/{download,list,cleanup}`、`/health`、`/metrics` |
| **VmPool**（warm pool + session 亲和） | `src/vm_pool.rs:38` `VmPool` / `:77` `acquire` / `:145` `release` / `:200` `maintain_warm_pool` |
| 单次执行编排 | `src/vm_runner.rs:66` `VmRunner::execute`，错误分类 `:16` `ExecuteError`（`PoolExhausted`/`VmBootFailed`/`ExecutionFailed`） |
| vsock UDS 通信 | `src/vsock_client.rs:25` `execute_guest_via_uds`，重试退避最多 10 次 |
| ext4 工作空间同步 | `src/workspace_sync.rs`（ext4 ↔ 目录 rsync 双向） |
| Firecracker REST API | `src/firecracker_api.rs:12`（经 Unix socket + hyper http1） |
| **guest-agent**（VM 内可执行） | `guest-agent/src/main.rs:226` `main` / `:242` `VsockListener::bind_with_cid_port(VMADDR_CID_ANY, 5005)` / `:58` `execute_python` |
| 协议 | 单行 JSON：`{"code":"...","stdin":null,"timeout_seconds":30}` → `{"success":true,"stdout":"...","exit_code":0}` |
| 503 → WASI fallback | `agent-core/src/firecracker_client.rs:164-168`；可用 `DISABLE_WASI_FALLBACK=1` 禁止回退 |

### 4.6 Cargo Features

`rust/agent-core/Cargo.toml:10-13`：
- `default = ["wasi"]`
- `wasi = ["dep:wasmtime", "dep:wasmtime-wasi"]`
- `default-no-wasi = []`

`wasi` feature 门控的源码：`lib.rs:15-16,24-25`、`grpc_server.rs:11-12,39-42,50-51,83-84,998-1000`、`tools.rs:1-2,64-65,90-109,111-119`。

---

## 5. Python LLM-Service 详解

### 5.1 关键洞察：**Python 不实现 gRPC 服务端**

经过对 `python/llm-service/` 全量搜索 `grpc.aio.server` / `add_*Servicer_to_server`，**没有任何业务侧 gRPC 服务端实现**。`grpc_gen/*_pb2_grpc.py` 仅为 protoc 生成的样板。

Python 服务的角色：
- 对外（Go orchestrator）：**HTTP server**（FastAPI :8000）
- 对 Rust agent-core：**gRPC client**（调 AgentService 执行 WASI 沙箱；调 SandboxService 操作文件）

### 5.2 应用入口与路由注册

- `main.py:127` `app = FastAPI(...)`
- `main.py:74` `lifespan` 启动时初始化 `ProviderManager` / Redis / emitter
- `main.py:103` `provider_manager = ProviderManager(settings)`（全局单例）
- `main.py:148-160` 注册 12 个 router
- `main.py:163` Prometheus metrics 挂载 `/metrics`

### 5.3 API 路由清单（`llm_service/api/`）

| 文件 | 路由前缀 | 用途 |
|---|---|---|
| `health.py` | `/health/*` | 健康检查 |
| `completions.py` | `/completions` | OpenAI 兼容补全（薄代理） |
| `agent.py` | `/agent/*` | **核心** — Agent 推理循环 |
| `lead.py` | `/lead/*` | Swarm Lead 决策 |
| `tools.py` | `/tools` | 工具管理 |
| `evaluate.py` | `/evaluate/*` | 结果评估 |
| `verify.py` | `/verify/*` | 引文核验（BM25） |
| `context.py` | `/context/*` | 上下文压缩 |
| `embeddings.py` | `/embeddings` | 向量嵌入 |
| `complexity.py` | `/complexity` | 任务复杂度分析 |
| `providers.py` | `/providers/*` | 可用模型列表 |
| `memory.py` | `/memory/*` | 记忆抽取 |
| `mcp_mock.py` | — | MCP mock |

**核心端点**：
| 端点 | 位置 | 用途 |
|---|---|---|
| `POST /agent/query` | `llm_service/api/agent.py:1011` `agent_query` | **单轮含完整工具迭代循环**（`while True:` 在 `:1943`，迭代预算 `:1895`） |
| `POST /agent/loop` | `llm_service/api/agent.py:3901` `agent_loop_step` | **自主 agent 单步**（一次 LLM 调用解析 JSON 决策，多轮由 Go 编排） |
| `POST /agent/research-plan` | `agent.py:4181` | 研究计划 |
| `POST /agent/decompose` | `agent.py:4355` | 任务分解 |
| `GET /agent/models` | `agent.py:5207` | 模型列表 |
| `GET /roles` | `agent.py:5221` | 角色列表 |

### 5.4 LLM Provider 体系

**两层架构**：
1. **核心抽象与管理器** — `llm_provider/`
   - `base.py:231` `LLMProvider` 抽象基类（`complete` / `stream_complete` / `count_tokens`）
   - `base.py:405` `LLMProviderRegistry` 注册表
   - `base.py:459` `CacheManager` 内存缓存
   - `base.py:62` `compute_token_cost`（缓存感知，单一真相源，对齐 Go `pricing.CostForSplitWithCache`）
   - `base.py:899` `TokenCounter`、`:948` `RateLimiter`
   - `manager.py:119` `LLMManager` 主管理器（`complete :574`、`_select_provider :794`、`_hedged_complete :1013`、熔断 `_CircuitBreaker :1346`、Redis 缓存 `_RedisCacheManager :1225`、热重载 `reload :1162`）
   - 单例 `get_llm_manager()` — `manager.py:1214`

2. **门面层** — `llm_service/providers/`（保留旧 API）
   - `__init__.py:63` `ProviderManager` 委托给 `LLMManager`
   - `__init__.py:178` `generate_completion`（agent loop 实际调用入口）
   - `__init__.py:300` `stream_completion`
   - `__init__.py:465` `_emit_events`（发射 LLM_PROMPT/PARTIAL/OUTPUT 事件）

**各厂商 Provider**（统一抽象，可选依赖 try/except 保证容错）：

| Provider | 文件:行号 |
|---|---|
| `OpenAIProvider`（tiktoken 精确）| `llm_provider/openai_provider.py:17` |
| `AnthropicProvider`（1h/5m cache）| `llm_provider/anthropic_provider.py:399` |
| `GoogleProvider` | `llm_provider/google_provider.py:24` |
| `OpenAICompatibleProvider` | `llm_provider/openai_compatible.py:24` |
| `GroqProvider` | `llm_provider/groq_provider.py:23` |
| `XAIProvider` | `llm_provider/xai_provider.py:28` |
| `MiniMaxProvider` | `llm_provider/minimax_provider.py:53` |
| `LiteLLMProvider`（100+ backends）| `llm_provider/litellm_provider.py:77` |

**修改 LLM provider 时必改 4 处**（CLAUDE.md 约束）：
- `config/models.yaml`（单一真相源）
- Go: `pricing/pricing.go` + `models/provider.go`
- Python: provider 文件 + `llm_service/providers/__init__.py`

### 5.5 Agent Loop 详解（项目核心逻辑之一）

**两种模式**：

#### A. `/agent/query` — 单轮内含完整工具迭代（合 vesicle）
- 入口 `agent.py:1011` `agent_query`
- 工具迭代主循环 `agent.py:1943` `while True:`
- 迭代预算 `agent.py:1895` `max_tool_iterations = _get_budget("max_tool_iterations", default_iterations)`
- 工具执行 `agent.py:2561` `_execute_and_format_tools`，`await tool.execute(...)` 在 `:2901`
- **interpretation pass**（工具结果综合）：`agent.py:2231-2443`，helper `build_interpretation_messages :518`、`aggregate_tool_results :557`、`validate_interpretation_output :786`
- 系统 prompt 渲染 `agent.py:1084` `render_system_prompt`，角色 preset `:1060`
- 工具选择 `agent.py:1532-1537`（`max_tools` + `filter_tools_by_task_type`）

#### B. `/agent/loop` — 自主 agent 单步（Go 编排多轮）
- 入口 `agent.py:3901` `agent_loop_step`
- 请求模型 `AgentLoopStepRequest`（`:3355`，含 `max_iterations: 25`）
- **不跑多轮**：一次 LLM 调用（`agent.py:3967`）解析 JSON 决策（`action`：`tool_call`/`idle`/`done`/`send_message`/`publish_data` 等）
- 返回结构化 `AgentLoopStepResponse`（`agent.py:4112`），多轮迭代与历史维护在 **Go orchestrator** 侧
- 预算/上下文裁剪 `agent.py:3680`（`while total_chars > max_prompt_chars`）

### 5.6 工具系统（`llm_service/tools/`）

| 子模块 | 文件:行号 | 作用 |
|---|---|---|
| 抽象基类 | `tools/base.py:103` `Tool` / `:50` `ToolMetadata` / `:34` `ToolParameter` / `:70` `ToolResult` / `:21` `ToolParameterType` | 工具父类 + 元数据 |
| 注册表 | `tools/registry.py:16` `ToolRegistry` / `:306` `get_registry()` | 单例注册 |
| 内置工具入口 | `tools/builtin/__init__.py:5-44` | 导出汇总 |
| MCP 工具 | `tools/mcp.py:33` `create_mcp_tool_class` | 动态生成远程 MCP 工具类 |
| MCP HTTP 客户端 | `mcp_client.py:47` `HttpStatelessClient`（`_SimpleBreaker :15` 熔断、域名 allowlist、重试） | 远程 MCP 通信 |
| OpenAPI→工具 | `tools/openapi_parser.py` + `tools/openapi_tool.py:607` `load_openapi_tools_from_config` | 动态从 OpenAPI spec 注册工具 |
| 插件热重载 | `tools/plugin_loader.py:24` `ToolPluginLoader` / `:372` `enable_hot_reload` / `:430` `PluginFileHandler`（watchdog） | 工具热插拔 |
| 文本格式化 | `tools/text_formatter.py:38` `format_tool_text` | 喂回 LLM 的可读化 |
| Vendor 适配器 | `tools/vendor_adapters/__init__.py:21` `get_vendor_adapter` | 厂商差异兼容 |

**内置工具清单**（`tools/builtin/`）：

| 工具类 | 文件:行号 | 说明 |
|---|---|---|
| `WebSearchTool` | `web_search.py:864` | Exa/Firecrawl/Google/Serper/SerpAPI/SearchAPI/Bing 多 provider |
| `WebFetchTool` | `web_fetch.py:1321` | LLM 抽取 `:37`、Firecrawl `:575` |
| `WebSubpageFetchTool` | `web_subpage_fetch.py:81` | 子页面抓取 |
| `WebCrawlTool` | `web_crawl.py:42` | 全站爬取 |
| `CalculatorTool` / `StatisticalCalculatorTool` | `calculator.py:52 / :206` | 安全计算 |
| `FileRead/Write/List/Search/Edit/Delete` | `file_ops.py:168/417/620/845/1159/1363` | 文件操作（经 Rust SandboxService） |
| `DiffFilesTool` / `JsonQueryTool` | `data_tools.py:79 / :280` | 数据工具 |
| `BashExecutorTool` | `bash_executor.py:18` | **危险** — 仅 workflow 内可用 |
| `PythonWasiExecutorTool` | `python_wasi_executor.py:54` | 经 gRPC 调 Rust WASI 沙箱执行 |
| `XSearchTool` | `x_search.py:123` | X/Twitter 搜索 |
| `BrowserTool` | `browser_use.py:168` | 调用 playwright-service |
| `SessionFileWrite/ListTool` | `session_file.py:19 / :113` | 会话级文件 |

危险工具（`bash_executor`、`file_write`）由 `ToolMetadata.dangerous` 标记（`base.py:64`），在网关层拦截，**仅在编排工作流内可用**。

### 5.7 Roles 子系统

| 路径 | 作用 |
|---|---|
| `llm_service/roles/presets.py` | 角色 preset 总线 |
| `llm_service/roles/deep_research/` | 深度研究角色（`deep_research_agent.py`、`research_supervisor.py`、`research_refiner.py`、`domain_discovery.py`、`domain_prefetch.py`、`quick_research_agent.py`） |
| `llm_service/roles/swarm/` | Swarm 多智能体协议（`agent_protocol.py`、`lead_protocol.py`、`role_prompts.py`） |

### 5.8 Token 计数与成本

- 抽象：`llm_provider/base.py:255` `LLMProvider.count_tokens`
- 通用估算：`base.py:899`（3.5 chars/token）
- **OpenAI tiktoken**（唯一真实分词器）：`openai_provider.py:12` import、`:57` 编码器缓存、`:63` `count_tokens`
- **缓存感知成本**（含 Anthropic 1h/5m cache、kimi/xai/OpenAI cache 折扣）：`base.py:62` `compute_token_cost`

### 5.9 配置 / 缓存 / 热重载

| 子系统 | 位置 | 说明 |
|---|---|---|
| Settings (env 驱动) | `llm_service/config.py:6` | Pydantic BaseSettings，含 Redis、PostgreSQL、各 provider key、模型 tier、缓存、限流、预算、事件发射 |
| YAML 单一真相 | `config/models.yaml` → `LLMManager.load_config` (`manager.py:178`) 经 `_translate_unified_config` (`:376`）解析，回退路径：`MODELS_CONFIG_PATH → /app/config/models.yaml → ./config/models.yaml` |
| LLM Redis 缓存 | `manager.py:524` Redis 后端 / `base.py:459` 内存回退 |
| 附件存储 | `attachments.py:18`/`:41`，Redis key `shannon:att:{id}`，TTL 1800s |
| 插件热重载 | `plugin_loader.py:14` watchdog Observer，`:372` enable / `:436` on_modified |
| LLM 配置热重载 | `manager.py:1162` `async def reload` / `providers/__init__.py:84` `reload` |
| 事件发射 | `llm_service/events.py:11` `EventEmitter`，异步队列后台 worker 向 orchestrator `:8081/events` 批量 POST |
| Prometheus 指标 | `llm_service/metrics.py:65` `MetricsCollector` / `:136` 单例 `metrics`；ASGI 挂载 `main.py:163` |

---

## 6. Playwright-Service 详解

独立的 FastAPI 进程（默认 `:9100`），与 llm-service 分离部署，专做无头浏览器自动化。两者通过 HTTP 解耦：llm-service 内的 `BrowserTool` 是其客户端。

### 关键文件

| 文件 | 行号 | 职责 |
|---|---|---|
| `app.py` | `:536` | FastAPI 入口、生命周期、截图/分段/会话化动作 |
| `session_manager.py` | `:53` | `BrowserSessionManager`：按 sessionid 复用 context、TTL 5min、最多 50 session |
| `security.py` | — | `validate_url_for_ssrf`：纯 stdlib SSRF 防护，阻断 RFC1918/loopback/CGNAT |

### HTTP 端点

| 方法+路径 | 行号 | 作用 |
|---|---|---|
| `GET /health` | `app.py:543` | 存活 + 浏览器连接 |
| `POST /capture` | `app.py:721` | 单页截图 |
| `POST /capture/sections` | `app.py:755` | LP 分段截图 |
| `POST /browser/action` | `app.py:1129` | 状态化浏览器动作（navigate/click/type/screenshot/scroll/wait/extract/evaluate） |
| `POST /browser/close` | `app.py:1293` | 关闭会话 |
| `GET /browser/sessions` | `app.py:1303` | 会话统计 |

### 关键特性

- `playwright_stealth`（`app.py:32`）掩盖 `navigator.webdriver` 指纹
- WAF 代理回退（`app.py:77` `_get_proxy_browser`，ScraperAPI 住宅代理）
- 弹窗消除（`POPUP_DETECTION_JS :144` / `POPUP_REMOVAL_JS :186`）
- 滑块拼接（`DETECT_SCROLL_HIJACK_JS :233` / `NAVIGATE_TO_SLIDE_JS :272`）
- `ENABLE_BROWSER_EVALUATE`（`app.py:51`）默认关闭 JS `evaluate` 降低风险
- `FixedWindowRateLimiter`（`app.py:455`）

---

## 7. Protobuf 契约（跨语言接口）

`protos/` 下 7 个 `.proto` 文件，由 `make proto` 生成 Go/Rust/Python 三套绑定：

| Proto | 文件 | 暴露的 service / 关键 message | 由谁实现 | 由谁调用 |
|---|---|---|---|---|
| `agent/agent.proto` | 157 行 | `AgentService`（6 RPC，见 §4.2）、`ExecuteTaskRequest/Response`、`TaskUpdate`、`ToolCapability` | **Rust** agent-core | Python llm-service、Go orchestrator |
| `sandbox/sandbox.proto` | 167 行 | `SandboxService`（FileRead/Write/List/ExecuteCommand/FileSearch/FileEdit/FileDelete）| **Rust** agent-core | Python llm-service、Go orchestrator |
| `common/common.proto` | 94 行 | `ExecutionMetrics`、`ToolCall`、`ToolResult`、`ExecutionMode`、`TaskMetadata`、`StatusCode` | —（共享类型） | 所有三端 |
| `orchestrator/orchestrator.proto` | 402 行 | `OrchestratorService`、`SessionService`、`StreamingService` | **Go** orchestrator | Go gateway、其他 |
| `orchestrator/streaming.proto` | 28 行 | 流式相关类型 | Go | Go |
| `session/session.proto` | 175 行 | `SessionService` session 管理（与 orchestrator 中重复？） | Go | Go |
| `llm/llm.proto` | 48 行 | LLM 相关类型 | —（疑似为预留 Python gRPC 接口，但 Python 端未实现） | — |

**重要发现**：`protos/llm/llm.proto` 定义的 gRPC 服务**未在 Python 端落地**，所有 LLM 调用走 HTTP（`/agent/query`、`/agent/loop`）。这是历史演进的结果——项目从最初的"全部 gRPC" 退化为 "Python 服务作为 HTTP 后端"。

---

## 8. 数据流：典型请求生命周期

以 `POST /api/v1/tasks` 为例（同步任务）：

```
1. 客户端 POST → gateway:8080/api/v1/tasks
2. gateway 鉴权 + middleware (限流/幂等/校验)
   → taskHandler.SubmitTask (cmd/gateway/internal/handlers/task.go:510)
3. gateway gRPC 调用 orchestrator:50052 OrchestratorService.SubmitTask
   (internal/server/service.go:362)
4. orchestrator 启动 Temporal Workflow: OrchestratorWorkflow
   (internal/workflows/orchestrator_router.go:32)
5. Workflow 路由决策序列：
   ├─ template? → TemplateWorkflow
   ├─ skip_synthesis? → AgentWorkflow
   ├─ force_swarm? → SwarmWorkflow
   ├─ force_research? → ResearchWorkflow (+ HITL plan)
   ├─ learning router recommendStrategy
   ├─ DecayActivity: DecomposeTask
   │     └─ HTTP POST → llm-service:8000/complexity
   │        → 返回 ComplexityScore + Mode + CognitiveStrategy + 子任务
   ├─ BudgetPreflight（检查 token 预算）
   └─ 主 switch：
       ├─ SimpleTaskWorkflow（复杂度 < 0.3）
       │   └─ ExecuteSimpleTask → HTTP → llm-service /agent/query
       │       └─ Python 工具迭代循环（agent.py:1943 while True）
       │           ├─ LLM completion → providers.generate_completion
       │           │   └─ LLMManager._select_provider → provider.complete
       │           ├─ 工具执行 → tools/registry.execute
       │           │   ├─ 本地工具（calculator）
       │           │   ├─ PythonWasiExecutorTool → gRPC → rust agent-core ExecuteTask
       │           │   │   └─ WASI 沙箱执行 / 或 firecracker-executor → microVM
       │           │   ├─ FileOpsTool → gRPC → rust agent-core SandboxService.FileRead/Write
       │           │   ├─ BrowserTool → HTTP → playwright-service /browser/action
       │           │   └─ MCP Tool → HTTP → 外部 MCP server
       │           └─ 事件发射 → events.py → orchestrator:8081/events
       └─ DAGWorkflow（默认多任务 fan-out）
           ├─ 并行：每个 AgentTask → ExecuteAgent → Python /agent/loop
           │        └─ 一步决策 + 工具执行，由 Go 编排多轮
           └─ fan-in：SynthesizeResults → Python 合成
6. 流式事件：activity EmitTaskUpdate → Redis pubsub → streaming manager
   → gateway SSE → 客户端
7. 持久化：activity PersistAgentExecution/PersistToolExecution → db.QueueWrite
   → 异步批量写入 Postgres
8. Workflow 完成 → gateway 返回 TaskResponse（同步）或 SSE 关闭
```

---

## 9. 配置与运行时

### 9.1 配置文件全景

| 文件 | 路径 | 修改影响 |
|---|---|---|
| `.env` | 项目根 | 环境变量：API keys、`GATEWAY_SKIP_AUTH`、连接串、`ENABLE_*` feature flags |
| `.env.example` | 项目根 | 模板 |
| `config/shannon.yaml` | `config/` | Go orchestrator 配置：auth.skip_auth、阈值、workers 等 |
| `config/models.yaml` | `config/` | **单一真相源**：模型列表、provider、定价（修改时**必同步 4 处**，见 §5.4） |
| `config/features.yaml` | `config/` | feature flags |
| `config/templates/synthesis/` | `config/templates/` | 自定义答案合成模板（`.tmpl`） |
| `config/skills/` | 各路径 | YAML skills 定义（路径来自 `SKILLS_PATH` env） |

### 9.2 Skip Auth 双处配置（dev/test）

```bash
# 1. .env
GATEWAY_SKIP_AUTH=1  # 0=prod, 1=dev/test

# 2. config/shannon.yaml
auth:
  skip_auth: true    # false=prod, true=dev/test
```

- Gateway 处理 HTTP 鉴权（`.env` 控制）
- Orchestrator 处理 gRPC 鉴权（`shannon.yaml` 控制）
- 两者须同步，且对服务必须 `docker compose down && up -d`（**restart 不重读 env_file**）

### 9.3 必备命令（`Makefile`）

| 命令 | 用途 |
|---|---|
| `make setup` | `.env` 创建 + proto 本地生成 |
| `make proto` | buf 生成（首选） / 失败回退本地 |
| `make proto-local` | 强制本地 proto 生成 |
| `make dev` | 启动全栈（不含 browser profile） |
| `make dev-browser` | 带 Playwright 启动 |
| `make smoke` | E2E 冒烟测试 |
| `make ci` | Go build + Rust build + Rust test compile + Python lint |
| `make test` | Go + Rust + Python 单测 |
| `make down` | 停全部 + 删卷 |
| `make seed-api-key` | 注入测试 API key `sk_test_123456` |

---

## 10. 关键开发规则（CLAUDE.md 摘要）

### Temporal Workflows
- activity 完成必须 `.Get(ctx, &result)` 等待
- workflow 内 `workflow.Sleep()`，**禁止** `time.Sleep()`（破坏 replay）
- 任何新代码路径必须 `workflow.GetVersion()` 门控
- 保持 replay 确定性

### Agent Nicknames
- **唯一入口**：`agents.GetAgentName(workflowID, index)`（`agents/names.go:82`）
- 固定日本车站名池保证 Temporal replay 确定性

### Database
- 主表：`task_executions`
- 状态值大写：`"COMPLETED"` 而非 `"completed"`
- 空字符串 UUID 须转 NULL

### Proto/gRPC
- Rust 枚举：`ExecutionMode::Simple`（非 `ExecutionMode::ExecutionModeSimple`）
- 修改 `.proto` 后必须 `make proto` → `cd go/orchestrator && go mod tidy` → `cd rust/agent-core && cargo build`

### LLM Providers
- 单一真相源：`config/models.yaml`
- 修改需同步 4 处（Go pricing/detection、Python provider/registry）

---

## 11. 推荐学习路径

### Path A：从外到内（适合架构理解）

1. **`README.md`** + **本文件**：建立全景认知
2. **`Makefile`**：理解构建/启动流程
3. **`deploy/compose/docker-compose.yml`**：服务拓扑
4. **`protos/agent/agent.proto`** + **`protos/sandbox/sandbox.proto`** + **`protos/common/common.proto`**：契约层
5. **`go/orchestrator/cmd/gateway/main.go`**：HTTP 入口
6. **`go/orchestrator/main.go`**：Temporal worker + gRPC server
7. **`go/orchestrator/internal/workflows/orchestrator_router.go`**：路由器（项目大脑）
8. **`go/orchestrator/internal/activities/agent.go`**：执行 agent
9. **`python/llm-service/main.py`**：Python 入口
10. **`python/llm-service/llm_service/api/agent.py`**：Agent 推理循环
11. **`python/llm-service/llm_provider/manager.py`**：LLM 管理器
12. **`rust/agent-core/src/main.rs`** + **`grpc_server.rs`** + **`enforcement.rs`** + **`wasi_sandbox.rs`**：Rust 层
13. **`rust/firecracker-executor/src/main.rs`** + **`vm_pool.rs`**：高隔离沙箱
14. **`python/playwright-service/app.py`**：浏览器自动化

### Path B：按子系统纵向学习

- **编排子系统**：`go/orchestrator/internal/workflows/` + `internal/activities/` + `internal/registry/`
- **预算子系统**：`internal/budget/manager.go` + `internal/activities/budget.go`
- **LLM 接入**：`python/llm-service/llm_provider/{base,manager}.py` + 各 provider 文件 + `config/models.yaml`
- **工具系统**：`python/llm-service/llm_service/tools/{base,registry}.py` + `tools/builtin/*` + `tools/mcp.py` + `tools/openapi_tool.py`
- **安全网关**：`rust/agent-core/src/enforcement.rs` + `wasi_sandbox.rs` + `safe_commands.rs` + `grpc_server.rs`
- **沙箱执行**：`rust/agent-core/src/wasi_sandbox.rs` + `rust/firecracker-executor/src/{vm_pool,vm_runner}.rs` + `guest-agent/`
- **流式输出**：`go/orchestrator/internal/streaming/` + `internal/httpapi/streaming.go` + `python/llm-service/llm_service/events.py`
- **持久化**：`go/orchestrator/internal/db/` + `migrations/`
- **Skills 系统**：`go/orchestrator/internal/skills/` + `config/skills/`（详见 `docs/skills-system.md`）
- **Sessions**：`internal/session/` + `protos/session/session.proto`
- **Daemon/CLI**：`internal/daemon/`
- **浏览器自动化**：`python/playwright-service/app.py` + `python/llm-service/llm_service/tools/builtin/browser_use.py`

### Path C：按典型请求贯穿（适合理解全链路）

1. 启动 `make dev`
2. 用 `curl` 发同步任务：`POST /api/v1/tasks`（沿用 CLAUDE.md 中的 quick test 命令）
3. 跟踪 `cmd/gateway/internal/handlers/task.go:510` `SubmitTask`
4. 跟踪 gRPC `OrchestratorService.SubmitTask` → `OrchestratorWorkflow`
5. 加断点 `internal/workflows/orchestrator_router.go:32`
6. 看路由决策：`DecomposeTask` → switch case → 走 `SimpleTaskWorkflow` 还是 `DAGWorkflow`
7. `ExecuteAgent` activity → HTTP 调 Python `/agent/loop`
8. Python `agent.py:3901` `agent_loop_step` → LLM 调用 → 工具决策 → `tool_registry.execute`
9. 工具触发：如选 `python_wasi_executor` → gRPC 调 Rust `ExecuteTask` → WASI 沙箱
10. 事件回流：`EmitTaskUpdate` → Redis pubsub → SSE
11. 持久化：`PersistAgentExecution` → `db.QueueWrite`
12. 收到同步响应或 SSE 关闭

### 学习辅助工具

- `make smoke` 跑 E2E，体会全链路
- Temporal UI: `http://localhost:8088` 看 workflow 执行历史
- `make replay-export` + `make replay` 做 workflow 确定性 replay 测试
- `make coverage-go` / `coverage-python` 看测试覆盖率分布

---

## 12. 后续工作说明

本文档是分阶段注释方案的第一阶段产出（最外层总览）。后续阶段会按以下顺序继续：

- **Phase A**：深度注释 Go orchestrator 核心文件（入口/路由/workflow/budget/analyzer 等 ~25 个）
- **Phase B**：深度注释 Rust agent-core 核心文件（gRPC server/sandbox/enforcement 等 ~13 个）
- **Phase C**：深度注释 Python llm-service 核心文件（provider/agent loop/tools 等 ~18 个）
- **Phase D**：深度注释 Proto 定义和关键 YAML 配置
- **Phase E**：其余源文件顶部添加模块级中文说明
- **Phase F**：为 `docs/` 下 91 篇 Markdown 增加中文导读小节

每阶段会单独提交，避免一次性改动过大。深度注释会包括：语法细节、函数定义、参数含义、返回值、与其他模块的协作关系、在 AI Agent 体系中的目的。

---

## 13. 关键参考

- `CLAUDE.md`（项目法则）
- `README.md` / `ROADMAP.md` / `CHANGELOG.md` / `CONTRIBUTING.md`
- `docs/multi-agent-workflow-architecture.md`
- `docs/pattern-usage-guide.md`
- `docs/streaming-api.md`
- `docs/token-budget-tracking.md`
- `docs/extending-shannon.md`
- `docs/skills-system.md`
- `docs/session-workspaces.md`
- `docs/swarm-agents.md`

> 文档对应的中文导读小节将在 Phase F 添加。