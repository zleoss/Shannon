# P2P Agent Coordination in Shannon

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Shannon 的点对点（P2P）Agent 协调机制——允许自治 Agent 基于数据依赖关系等待所需数据后再执行。系统通过分解服务自动检测子任务间的数据依赖关系（producer 产出 → consumer 消费），使用基于 Topic 的发布-订阅模式进行协调，并通过 SupervisorWorkflow 实现。文档包含配置方法（config/features.yaml）、使用示例和监控说明。

### 章节导航
- **How It Works**: 自动依赖检测→Topic 发布-订阅→工作流路由
- **Automatic Dependency Detection**: 分解服务自动识别每个子任务产出/消费的数据
- **Coordination Mechanism**: Producer 发布到 Topic，Consumer 等待所需 Topic 就绪后执行
- **Workflow Routing**: 无依赖→SimpleTaskWorkflow/DAGWorkflow，有依赖→SupervisorWorkflow
- **Configuration**: config/features.yaml 中的 p2p 配置段（enabled + timeout_seconds）
- **Use Cases**: 多阶段分析、报告生成、研究流程等场景
- **Monitoring**: 查看 P2P 协调状态和依赖等待情况

### 与 AI Agent 体系的关联
- SupervisorWorkflow：`go/orchestrator/internal/workflows/supervisor_workflow.go`
- 依赖检测：分解活动 `go/orchestrator/internal/activities/decompose.go`
- 数据交换通过 Session Workspace 的 Redis 存储实现
- 特征开关：`config/features.yaml`

### 阅读建议
处理多步骤依赖任务的开发者必读；重点了解自动依赖检测和 Topic 发布-订阅模式。

## Overview

Shannon now supports **Peer-to-Peer (P2P) Agent Coordination**, enabling autonomous agents to coordinate task execution based on data dependencies. This feature allows agents to wait for required data from other agents before proceeding, creating efficient pipelines without manual orchestration.

## How It Works

### 1. Automatic Dependency Detection
When you submit a query with sequential or dependent steps, Shannon's decomposition service automatically:
- Identifies what data each subtask **produces**
- Determines what data each subtask **consumes** (needs from other tasks)
- Routes to SupervisorWorkflow when dependencies are detected

### 2. Coordination Mechanism
Agents use a topic-based publish-subscribe pattern:
- **Producer agents** publish results to semantic topics (e.g., "analysis-results", "metrics")
- **Consumer agents** wait for required topics before starting execution
- Workspace storage (Redis-based) facilitates data exchange between agents

### 3. Workflow Routing
The system automatically selects the appropriate workflow:
- **No dependencies** → SimpleTaskWorkflow or DAGWorkflow (parallel)
- **With dependencies** → SupervisorWorkflow with P2P coordination
- **Forced P2P** → Always use SupervisorWorkflow

## Configuration

### Enable P2P Coordination

Configure via `config/features.yaml`:

```yaml
# config/features.yaml
workflows:
  p2p:
    enabled: true           # Master switch for P2P coordination
    timeout_seconds: 360    # Maximum wait time for dependencies
```

Note: P2P is not toggled via environment variables; the orchestrator reads these YAML settings at runtime.

## Usage Examples

### Example 1: Sequential Pipeline
```python
# Query: "Analyze sales data and then create a report based on the analysis"

# Shannon automatically detects:
# - Task 1: Analyze → produces: ["sales-analysis", "insights"]
# - Task 2: Report → consumes: ["sales-analysis", "insights"]
#
# Task 2 waits for Task 1 to complete before starting
```

### Example 2: Complex Data Pipeline
```python
# Query: "Load CSV, process the data, create visualizations, and generate PDF report"

# Dependency chain detected:
# - Load CSV → produces: ["raw-data"]
# - Process → consumes: ["raw-data"], produces: ["processed-data", "statistics"]
# - Visualize → consumes: ["statistics"], produces: ["charts"]
# - PDF → consumes: ["processed-data", "charts"]
```

### Example 3: Force P2P Mode
```python
# Force P2P coordination even for simple tasks
grpcurl -d '{
  "query": "What is 2+2?",
  "context": {"force_p2p": true}
}' localhost:50052 shannon.orchestrator.OrchestratorService/SubmitTask
```

## API Usage

### Via gRPC
```protobuf
// Normal usage - P2P activates automatically when needed
SubmitTaskRequest {
  query: "Analyze data then create report"
  context: {}
}

// Force P2P mode
SubmitTaskRequest {
  query: "Simple calculation"
  context: {
    "force_p2p": true
  }
}
```

### Via Python Client
```python
from shannon import ShannonClient

client = ShannonClient(base_url="http://localhost:8080")

# Automatic P2P for dependent tasks
response = client.submit_task(
    "Research the topic, validate findings, and write article",
    session_id="p2p-demo"
)

# Force P2P mode
response = client.submit_task(
    "Simple query",
    session_id="p2p-demo",
    context={"force_p2p": True}
)
```

## Dependency Detection Rules

The system detects dependencies based on:

1. **Sequential indicators**: "then", "after", "based on", "using the results"
2. **Data flow analysis**: What each task produces and what it needs
3. **Tool outputs**: Tasks using tools automatically produce the tool's output type
4. **Semantic understanding**: LLM analyzes the logical flow of tasks

## Benefits

1. **Automatic Orchestration**: No need to manually specify task order
2. **Efficient Execution**: Tasks run as soon as dependencies are satisfied
3. **Parallel When Possible**: Independent tasks still run in parallel
4. **Robust Coordination**: Timeout protection and error handling built-in
5. **Transparent**: Logs show P2P coordination decisions

## Monitoring

Check P2P coordination in logs:

```bash
# View P2P detection in decomposition
docker compose logs llm-service | grep "P2P coordination detected"

# View workflow routing decisions
docker compose logs orchestrator | grep "SupervisorWorkflow"

# View dependency waiting
docker compose logs orchestrator | grep "Dependency wait"
```

## Architecture Details

### Components Involved

1. **LLM Service**: Detects and populates `produces`/`consumes` fields during decomposition
2. **Orchestrator Router**: Routes tasks with dependencies to SupervisorWorkflow
3. **SupervisorWorkflow**: Manages P2P coordination and dependency waiting
4. **Workspace Activities**: Handle data exchange via Redis

### Data Flow

```
User Query → Decomposition (detects dependencies) → Router (selects workflow)
    ↓                                                   ↓
If dependencies exist                            SupervisorWorkflow
    ↓                                                   ↓
Agent 1 executes → Publishes to workspace → Agent 2 waits → Agent 2 executes
```

## Comparison with Other Frameworks

| Framework | Coordination Method | Shannon's Advantage |
|-----------|-------------------|-------------------|
| LangGraph | Static graph edges | Dynamic dependency detection |
| CrewAI | Role-based sequence | Automatic data flow analysis |
| AutoGen | Conversation-based | Structured P2P with timeouts |
| OpenAI SDK | No built-in P2P | Native P2P infrastructure |

## Limitations

- Maximum timeout is configurable (default 6 minutes)
- Circular dependencies are not currently detected (planned for future)
- P2P adds overhead for simple tasks (use force flag judiciously)

## Future Enhancements

Planned improvements:
- Circular dependency detection
- Cross-session data sharing
- Priority-based task scheduling
- Dynamic timeout adjustment
- Visual dependency graph generation

## Testing

Run the P2P coordination test suite:

```bash
./tests/e2e/p2p_coordination_test.sh
```

This tests:
1. Automatic P2P activation for dependent tasks
2. Force P2P mode functionality
3. Complex pipeline coordination

## Troubleshooting

### P2P Not Activating
- Check if `produces`/`consumes` fields are populated in decomposition
- Verify P2P is enabled in configuration
- Ensure SupervisorWorkflow is being selected

### Tasks Not Waiting for Dependencies
- Check workspace connectivity (Redis)
- Verify topic names match between producer/consumer
- Check timeout settings

### Force P2P Not Working
- Ensure context contains `"force_p2p": true`
- Check orchestrator logs for "forced" message
- Verify latest code is deployed
