# Environment Configuration Guide

## 📖 中文学习注解

### 本文核心摘要
本文档是 Shannon Docker Compose 部署的环境变量配置指南。说明 Docker Compose 的环境变量加载优先级（Shell 环境 > .env 文件 > env_file 指令 > environment 段）、.env 文件的创建与符号链接配置方法、所有必需的环境变量（API Keys、数据库连接、服务地址等）及其说明、常见问题排查与最佳实践。

### 章节导航
- **Overview**: Docker Compose 环境变量加载的 4 级优先级
- **Environment Variable Loading**: .env 文件的创建和符号链接配置
- **Required Configuration**: 所有必需的环境变量清单（API Keys、服务端点、特征开关）
- **Common Issues**: 常见配置错误及其解决方案
- **Best Practices**: 安全建议、Secret 管理、多环境配置策略

### 与 AI Agent 体系的关联
- 环境变量控制 Shannon 所有服务的运行配置
- 对应文件：根目录 .env + `deploy/compose/.env`
- 修改 .env 后需要 `docker compose down && up -d`（restart 不会重新读取）

### 阅读建议
所有部署 Shannon 的开发者必读；初次部署者按照文档步骤逐项配置即可。

This guide explains how to properly configure environment variables for Shannon's Docker Compose deployment.

## Table of Contents
- [Overview](#overview)
- [Environment Variable Loading](#environment-variable-loading)
- [Required Configuration](#required-configuration)
- [Common Issues and Solutions](#common-issues-and-solutions)
- [Best Practices](#best-practices)

## Overview

Shannon uses environment variables for sensitive configuration like API keys, service endpoints, and feature flags. Proper configuration is essential for the platform to function correctly.

## Environment Variable Loading

### How Docker Compose Loads Environment Variables

Docker Compose loads environment variables in this order of precedence:

1. **Shell environment variables** (highest priority)
2. **`.env` file in the docker-compose directory**
3. **`env_file` directive in docker-compose.yml**
4. **`environment` section in docker-compose.yml** (lowest priority)

### Critical Setup Steps

#### 1. Create the Root `.env` File

Create `.env` in the project root by copying the example:
```bash
cp .env.example .env
# Edit .env with your actual API keys
```

#### 2. Create Symlink for Docker Compose

Docker Compose looks for `.env` in the same directory as the `docker-compose.yml` file:
```bash
cd deploy/compose
ln -sf ../../.env .env
```

This symlink ensures Docker Compose can find your environment variables.

#### 3. Verify Configuration

Test that your environment variables are loaded correctly:
```bash
# From project root
docker compose -f deploy/compose/docker-compose.yml config | grep EXA_API_KEY

# Check inside running container
docker compose -f deploy/compose/docker-compose.yml exec llm-service env | grep EXA
```

## Required Configuration

### Essential API Keys

```bash
# LLM provider (set at least one)
# Note: OpenAI is REQUIRED for memory features (embeddings)
OPENAI_API_KEY=sk-...

# Additional LLM providers (optional)
# ANTHROPIC_API_KEY=...
# XAI_API_KEY=...
# ZAI_API_KEY=...

# Web search provider (optional but recommended)
WEB_SEARCH_PROVIDER=google
GOOGLE_SEARCH_API_KEY=...
GOOGLE_SEARCH_ENGINE_ID=...
# SERPER_API_KEY=...
# EXA_API_KEY=...
# FIRECRAWL_API_KEY=...

# Model routing overrides (optional)
COMPLEXITY_MODEL_ID=gpt-5-nano-2025-08-07
DECOMPOSITION_MODEL_ID=claude-sonnet-4-20250514
```

**Important**: OpenAI API key is required for memory features (semantic search, hierarchical memory, agent context). Without it:
- Workflows continue executing normally
- Memory retrieval silently degrades (returns empty results)
- Agents operate in "stateless" mode without historical context

See [Memory System Architecture](memory-system-architecture.md#dependencies) for details.

### Core Service Endpoints

```bash
TEMPORAL_HOST=temporal:7233
AGENT_CORE_ADDR=agent-core:50051
LLM_SERVICE_URL=http://llm-service:8000
ADMIN_SERVER=http://orchestrator:8081
```

### Optional Runtime Limits

```bash
WORKFLOW_SYNTH_BYPASS_SINGLE=true
ENFORCE_TIMEOUT_SECONDS=90
ENFORCE_MAX_TOKENS=32768
RATE_LIMIT_REQUESTS=100
RATE_LIMIT_WINDOW=60
```

### Scheduled Tasks Configuration

```bash
# Maximum number of schedules per user (default: 50)
SCHEDULE_MAX_PER_USER=50

# Minimum interval between schedule runs in minutes (default: 60)
SCHEDULE_MIN_INTERVAL_MINS=60

# Maximum budget per scheduled execution in USD (default: 10.0)
SCHEDULE_MAX_BUDGET_USD=10.0
```

See [Scheduled Tasks](scheduled-tasks.md) for complete scheduling documentation.

## Common Issues and Solutions

### Issue: "No web search provider configured"

**Symptom**: Error message in agent execution:
```
Error: No web search provider configured. Please set one of:
- EXA_API_KEY environment variable for Exa search
- FIRECRAWL_API_KEY environment variable for Firecrawl search
```

**Solution**:
1. Ensure `.env` file contains the API keys
2. Create the symlink: `cd deploy/compose && ln -sf ../../.env .env`
3. Restart services: `docker compose -f deploy/compose/docker-compose.yml up -d`

### Issue: Docker Compose warnings about missing variables

**Symptom**: Warnings like:
```
level=warning msg="The \"EXA_API_KEY\" variable is not set. Defaulting to a blank string."
```

**Solution**:
These warnings appear during the build phase when Docker Compose evaluates the docker-compose.yml file. They can be safely ignored if:
1. The symlink exists (`deploy/compose/.env -> ../../.env`)
2. The variables are correctly set inside the container (verify with `docker compose exec llm-service env`)

### Issue: Rate limit errors in multi-agent execution

**Symptom**: "Rate limit exceeded" errors when multiple agents run in parallel

**Solution**: This is now fixed in the codebase (rate limiting is per-session, not global). If you still see issues:
1. Check the rate limit configuration in your tools
2. Ensure you're using the latest code version

### Issue: WASI interpreter path is invalid

**Symptom**: Python tool execution fails with `No such file or directory` for the WASI runtime.

**Solution**: The containers mount interpreters at `/opt/wasm-interpreters`, while local runs usually use `./wasm-interpreters/...`.
Set `PYTHON_WASI_WASM_PATH` to the path that exists in your environment:

```bash
# Inside Docker (default)
PYTHON_WASI_WASM_PATH=/opt/wasm-interpreters/python-3.11.4.wasm

# Local host run
PYTHON_WASI_WASM_PATH=./wasm-interpreters/python-3.11.4.wasm
```

## Best Practices

### 1. Never Commit Secrets
- Keep `.env` in `.gitignore`
- Use `.env.example` as a template with placeholder values
- Store production secrets in a secure vault

### 2. Use Consistent Naming
- Use UPPER_CASE_WITH_UNDERSCORES for environment variables
- Prefix service-specific variables (e.g., `LLM_SERVICE_URL`)

### 3. Document All Variables
- Maintain `.env.example` with all required variables grouped by section
- Include short comments describing defaults/usage so contributors know what to change
- Specify which variables are optional vs required

### 4. Validate on Startup
Services should validate required environment variables on startup:
```python
# Example in Python service
import os
import sys

required_vars = ["OPENAI_API_KEY", "REDIS_HOST"]
missing = [var for var in required_vars if not os.getenv(var)]
if missing:
    print(f"Missing required environment variables: {missing}")
    sys.exit(1)
```

### 5. Use Environment-Specific Files
For different environments:
```bash
.env.development
.env.staging  
.env.production
```

Load the appropriate file:
```bash
# Development
ln -sf ../../.env.development deploy/compose/.env

# Production
ln -sf ../../.env.production deploy/compose/.env
```

## Makefile Integration

Add these helpful commands to your Makefile:

```makefile
# Setup environment
.PHONY: setup-env
setup-env:
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "Created .env file - please edit with your API keys"; \
	fi
	@cd deploy/compose && ln -sf ../../.env .env
	@echo "Environment setup complete"

# Validate environment
.PHONY: check-env
check-env:
	@echo "Checking environment configuration..."
	@docker compose -f deploy/compose/docker-compose.yml config > /dev/null 2>&1
	@docker compose -f deploy/compose/docker-compose.yml exec llm-service env | grep -q EXA_API_KEY || \
		echo "Warning: EXA_API_KEY not set in container"
	@echo "Environment check complete"
```

## Troubleshooting Commands

```bash
# Check if .env file exists
ls -la .env deploy/compose/.env

# View current environment in container
docker compose -f deploy/compose/docker-compose.yml exec llm-service env | sort

# Confirm WASI interpreter path inside the agent-core container
docker compose -f deploy/compose/docker-compose.yml exec agent-core ls /opt/wasm-interpreters

# Test with explicit env file
docker compose --env-file .env -f deploy/compose/docker-compose.yml up -d

# Export shell variables (temporary, for testing)
export $(grep -v '^#' .env | xargs)
docker compose -f deploy/compose/docker-compose.yml up -d
```

## Summary

1. **Always create the symlink**: `cd deploy/compose && ln -sf ../../.env .env`
2. **Verify variables are loaded**: Check inside the container, not just docker-compose output
3. **Keep secrets secure**: Never commit actual API keys to the repository
4. **Document requirements**: Maintain clear documentation of required variables

Following these guidelines will prevent environment configuration issues and ensure smooth deployment of the Shannon platform.
