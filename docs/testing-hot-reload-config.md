# Dynamic Configuration Integration Test

## 📖 中文学习注解

### 本文核心摘要
本文档展示 Shannon 热重载配置功能的集成测试。通过切换 shannon.yaml 和 shannon-test.yaml 验证配置修改无需重启服务即可生效。测试覆盖：健康检查配置变更（Port/Interval/Timeout/Checker 启用/禁用）、服务配置变更（超时/并发数/端点）、熔断器参数变更和 Agent 端点变更。文档详细列出每个测试步骤的预期行为。

### 章节导航
- **Test Overview**: 配置解析增强、逐项健康检查配置、热重载回调
- **Test Configuration Files**: shannon.yaml（生产）vs shannon-test.yaml（测试）
- **Key Changes Demonstrated**: 健康检查/服务/Agent 端点/熔断器各项参数的变化对比
- **Testing Steps**: 各步骤的详细操作和验证方法

### 与 AI Agent 体系的关联
- 热重载实现：Go orchestrator 的 config manager
- 配置文件：`config/shannon.yaml`（默认）和 `config/shannon-test.yaml`（测试）
- 影响所有服务的运行配置变更而不中断服务

### 阅读建议
运维和平台开发者必读；了解热重载的能力范围即可。

This document demonstrates the hot-reload configuration functionality implemented in Shannon.

## Test Overview

The system has been enhanced with:

1. **Comprehensive Config Parsing**: All Shannon configuration sections (service, circuit breakers, degradation, health, agents, temporal, logging) are now parsed from `shannon.yaml`

2. **Per-Check Health Configuration**: Individual health checkers can be enabled/disabled and have custom intervals/timeouts

3. **Hot-Reload Callbacks**: Configuration changes trigger automatic updates to running services without restart

## Test Configuration Files

- `config/shannon.yaml` - Default production configuration
- `config/shannon-test.yaml` - Test configuration with different values

## Key Changes Demonstrated

### Health Check Configuration Changes:
- **Global health port**: 8081 → 8082 (health.port)
- **Check intervals**: 30s → 45s (health.check_interval)
- **Timeouts**: 5s → 8s (health.timeout)
- **Agent Core checker**: enabled → **DISABLED** (health.checks.agent_core.enabled)
- **LLM Service interval**: 60s → 90s (health.checks.llm_service.interval)

### Service Configuration Changes:
- **Health port**: 8081 → 8082
- **Graceful timeout**: 30s → 45s
- **Read/Write timeouts**: 10s → 15s

### Agent Endpoint Changes:
- **Agent Core**: agent-core:50051 → localhost:50051
- **LLM Service**: http://llm-service:8000 → http://localhost:8000
- **Max concurrent**: 5 → 8

### Circuit Breaker Changes:
- **Redis max requests**: 5 → 8
- **Database max requests**: 3 → 5
- **Intervals**: 30s → 45s

## Testing Steps

1. **Start Shannon** with default config (shannon.yaml)
2. **Verify health endpoints** are running on port 8081
3. **Check that all 4 health checkers** (redis, database, agent_core, llm_service) are enabled
4. **Replace config file**:
   ```bash
   cp config/shannon-test.yaml config/shannon.yaml
   ```
5. **Observe hot-reload logs**:
   - Health port change detected
   - Agent endpoints updated
   - Agent Core health checker disabled
   - Check intervals updated

6. **Verify changes applied**:
   - Health endpoints now on port 8082
   - Agent Core health checker disabled (not included in health responses)
   - Other checkers have updated intervals (45s instead of 30s)

## Expected Log Output (Example)

**Note:** Actual log messages may vary depending on the implementation. This shows the type of changes that should be logged:

```
Shannon configuration changed file=shannon.yaml action=modify
Agent Core endpoint changed old=agent-core:50051 new=localhost:50051
LLM Service endpoint changed old=http://llm-service:8000 new=http://localhost:8000
Health server port changed old=8081 new=8082
Health check global settings changed
Updated health checker configuration checker=agent_core enabled=false
Updated health checker configuration checker=redis interval=45s
Health manager configuration updated enabled=true global_check_interval=45s
```

## API Testing

Health endpoints will reflect the new configuration:

```bash
# Before config change (port 8081, all checkers enabled)
curl http://localhost:8081/health/detailed
# Returns: redis, database, agent_core, llm_service

# After config change (port 8082, agent_core disabled)  
curl http://localhost:8082/health/detailed
# Returns: redis, database, llm_service (agent_core excluded)
```

## Verification Commands

```bash
# Check if health manager received new config
curl http://localhost:8082/health/detailed | jq '.summary.total'
# Should return 3 (not 4) because agent_core is disabled

# Verify per-check intervals by monitoring logs
# Should see redis/database checks every 45s (not 30s)
# Should see llm_service checks every 90s (not 60s)
```

This demonstrates **zero-downtime configuration updates** for:
- Health check configuration (intervals, timeouts, enable/disable)
- Service endpoints (agent addresses)
- Circuit breaker thresholds
- All other Shannon configuration sections

The system maintains **operational continuity** while applying configuration changes in real-time.

**Note:** Hot-reload functionality requires the configuration file watcher to be enabled in the service.