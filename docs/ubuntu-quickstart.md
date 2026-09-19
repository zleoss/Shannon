# Ubuntu Shannon Development Environment Quick Start Guide

## 📖 中文学习注解

### 本文核心摘要
本文档是 Ubuntu 系统上快速部署 Shannon 开发环境的指南。涵盖系统环境要求（Ubuntu 22.04）、依赖安装（Docker 26.1.3 / Docker Compose / Go / Python 3.10+ / Protoc）、项目部署（克隆/ .env 配置 / 构建/启动）、服务验证（健康检查/Temporal 页面/各服务端点）、运行第一个任务和 Web 界面访问。

### 章节导航
- **System Environment**: 验证的系统版本和推荐的 Docker / Go / Python 版本
- **Dependencies Installation**: 各依赖的安装命令和版本要求
- **Project Deployment**: make setup + vim .env + make dev 三步部署
- **Service Verification**: 各端口（8080 Gateway / 8000 LLM / 50051 Agent-Core / 8233 Temporal）验证
- **Running Your First Task**: 提交测试任务的 curl 示例
- **Web Interface Access**: 访问 Shannon Dashboard
- **Common Management Commands**: 日志查看、重启、重建等命令汇总
- **Troubleshooting**: 常见部署问题排查

### 与 AI Agent 体系的关联
- 部署脚本：`scripts/setup_python_wasi.sh`、Makefile
- Docker Compose：`deploy/compose/docker-compose.yml`
- 环境配置参考：`docs/environment-configuration.md`

### 阅读建议
Ubuntu 用户首次部署必读；其他发行版参考 rocky-linux-quickstart.md。

This guide is designed for Ubuntu system users to quickly deploy and configure the Shannon production-grade AI agent platform development environment.

## Table of Contents

- [System Environment](#system-environment)
- [Dependencies Installation](#dependencies-installation)
- [Project Deployment](#project-deployment)
- [Service Verification](#service-verification)
- [Running Your First Task](#running-your-first-task)
- [Web Interface Access](#web-interface-access)
- [Common Management Commands](#common-management-commands)
- [Troubleshooting](#troubleshooting)

## System Environment

### Tested Environment Specifications

```bash
# Check system version information
cat /etc/lsb-release
```

**Recommended Configuration**:

```bash
DISTRIB_ID=Ubuntu
DISTRIB_RELEASE=22.04
DISTRIB_CODENAME=jammy
DISTRIB_DESCRIPTION="Ubuntu 22.04.4 LTS"
```

### Container Environment Versions

```bash
# Docker version requirements
docker -v
# Recommended: Docker version 26.1.3, build b72abbb

# Docker Compose version requirements
docker compose -v
# Recommended: docker-compose version 1.29.1, build c34c88b2
```

### Recommended Installation Path

```bash
# Project deployment directory (customize as needed)
${SHANNON_BASE_DIR:-/data}

# Code path (relative to base directory)
${SHANNON_BASE_DIR:-/data}/Shannon/
```

## Dependencies Installation

### Development Environment One-Click Installation

```bash
# Complete development environment one-click installation
sudo apt-get update && sudo apt-get install -y \
  netcat-traditional curl wget tar unzip git \
  net-tools telnet nmap dnsutils tcpdump \
  htop iotop lsof strace tree jq \
  vim nano grep awk sed \
  build-essential make gcc g++
```

### gRPC Client Tool Installation

**grpcurl** is an essential debugging tool for the Shannon platform, used to test gRPC service interfaces:

```bash
# Project Information
# Repository: https://github.com/fullstorydev/grpcurl
# Language: Go
# Purpose: Command-line client for gRPC services (equivalent to curl for HTTP)

# Download latest version
wget https://github.com/fullstorydev/grpcurl/releases/download/v1.8.9/grpcurl_1.8.9_linux_x86_64.tar.gz

# Extract files
tar -xzf grpcurl_1.8.9_linux_x86_64.tar.gz

# Move to system path
sudo mv grpcurl /usr/local/bin/

# Verify installation
grpcurl --version
# Output: grpcurl v1.8.9
```

## Project Deployment

### 1. Get Source Code

```bash
# Create and enter project directory
export SHANNON_BASE_DIR=${SHANNON_BASE_DIR:-/data}
sudo mkdir -p $SHANNON_BASE_DIR
cd $SHANNON_BASE_DIR

# Clone Shannon project repository
git clone https://github.com/Kocoro-lab/Shannon.git
cd $SHANNON_BASE_DIR/Shannon/

# Check current directory
pwd
# Expected output: $SHANNON_BASE_DIR/Shannon/
```

### 2. Environment Configuration

```bash
# Copy environment variables template
cp .env.example .env

# Edit configuration file
vi .env
```

**Required Configuration Items**:

```bash
# LLM service provider API keys (configure at least one)
OPENAI_API_KEY=sk-your-openai-key-here
ANTHROPIC_API_KEY=your-anthropic-key-here

# Optional: Search service API key
EXA_API_KEY=your-exa-key-here

# Optional: Web search configuration
GOOGLE_SEARCH_API_KEY=your-google-key
GOOGLE_SEARCH_ENGINE_ID=your-search-engine-id
```

Save and exit the editor.

### 3. Platform Initialization

```bash
# Step 1: Initialize project environment and basic configuration
make setup-env

# Step 2: Remote server specific setup (install tools + generate proto)
./scripts/setup-remote.sh

# Step 3: Generate cross-language gRPC code
make proto

# Step 4: Start the complete Shannon platform
make dev
```

**Service Startup Notes**:

- First startup may take 3-5 minutes (downloading Docker images)
- Platform will automatically start 7 docker instances
- Access addresses will be displayed after all services start

## Service Verification

### Check Service Status

```bash
# View all running containers
make ps

# Check service health status
curl http://localhost:8081/health  # Orchestrator service

# Run end-to-end test verification
make smoke
```

### Service Port Description

| Service | Port | Description |
|---------|------|-------------|
| Orchestrator | 50052 (gRPC), 8081 (HTTP) | Workflow orchestration service |
| LLM Service | 8000 | LLM provider interface service |
| Agent Core | 50051 (gRPC) | Agent core execution service |
| Temporal UI | 8088 | Workflow management interface |
| PostgreSQL | 5432 | Main database |
| Redis | 6379 | Cache service |

Vector search is disabled by default in this repo copy. Re-enable it only if you plan to run a separate vector database such as Qdrant.

## Running Your First Task

### Submit Test Task

```bash
# Use task submission script
./scripts/submit_task.sh "Is the Earth round? How do you prove it? What evidence do you have?"
```

**Expected Output Example**:

```json
{
  "workflowId": "task-00000000-0000-0000-0000-000000000002-1758093116",
  "taskId": "task-00000000-0000-0000-0000-000000000002-1758093116",
  "status": "STATUS_CODE_OK",
  "decomposition": {
    "mode": "EXECUTION_MODE_STANDARD",
    "complexityScore": 0.5
  },
  "message": "Task submitted successfully. Session: 40d7e9b5-df1c-462b-9221-cd6df8e6318b"
}
```

### Task Status Monitoring

```bash
# Script will automatically poll task status
Polling status for task: task-00000000-0000-0000-0000-000000000002-1758093116
  attempt 1: status=TASK_STATUS_RUNNING
  attempt 2: status=TASK_STATUS_RUNNING
  ...
  attempt 10: status=TASK_STATUS_COMPLETED
Done.
```

Seeing `"Task submitted successfully"` indicates the task was submitted successfully and the system is processing it.

## Web Interface Access

### Temporal Workflow Management Interface

```bash
# Access URL
http://your-server-ip:8088
```

**Usage Instructions**:

1. After entering the homepage, click **"Workflows"**
2. View detailed execution flow and status of tasks
3. Supports workflow replay and debugging features

### Service Metrics Monitoring

```bash
# Orchestrator metrics
curl http://localhost:2112/metrics

# Agent core metrics
curl http://localhost:2113/metrics

# LLM service metrics
curl http://localhost:8000/metrics
```

## Common Management Commands

### Service Management

```bash
# Stop all services (preserve data)
make down

# Stop services and remove all data volumes
make clean

```

### Log Viewing

```bash
# View all service logs
make logs

```

## Troubleshooting

### Most Common Issues ⚠️

The following are the two most frequently encountered problems during deployment and their solutions:

### Common Issue Resolution

1. **Go Module Build Failure** ⚠️ Common

   **Error Symptoms**:

   ```bash
   => ERROR [orchestrator builder 7/7] RUN CGO_ENABLED=0 GOOS=linux go build...
   internal/server/session_service.go:14:5: module github.com/Kocoro-lab/Shannon@latest found 
   (v0.0.0-20250829105349-0a1cee10d781), but does not contain package 
   github.com/Kocoro-lab/Shannon/go/orchestrator/internal/pb/session
   failed to solve: process "/bin/sh -c CGO_ENABLED=0 GOOS=linux go build..." 
   did not complete successfully: exit code: 1
   make: *** [Makefile:34: dev] Error 17
   ```

   **Solution**:

   ```bash
   # Regenerate protobuf files
   make proto
   
   # Then restart services
   make dev
   ```

2. **Smoke Test Keeps Waiting**

   **Error Symptoms**:

   ```bash
   make smoke
   Temporal UI reachable
   Temporal UI
   Agent-Core gRPC health
   healthAgentCore
   ExecuteTaskagent
   Wait for orchestrator gRPC (50052) to be ready
   waiting
   waiting
   # Waits multiple times, then errors out
   ```

   **Root Cause**: System lacks the `netcat` tool, which the smoke test script uses to check port connectivity

   **Solution**:

   ```bash
   # Install netcat tool
   sudo apt-get update && sudo apt-get install -y netcat-traditional
   
   # Re-run test
   make smoke
   ```

   **Successful Output Example**:

   ```bash
   [OK]  Temporal UI
   [OK]  Agent-Core health
   [OK]  Agent-Core ExecuteTask
   [OK]  Orchestrator gRPC ready
   [OK]  Orchestrator SubmitTask (reflection)
   [OK]  Orchestrator reached terminal status
   [OK]  Orchestrator persistence verified
   [OK]  Metrics endpoints reachable
   [OK]  LLM ready
   [OK]  LLM health endpoints
   [OK]  MCP tool registered
   [OK]  MCP tool executed
   [OK]  Postgres query
   All smoke checks passed
   ```

3. **Service Startup Failure**

   ```bash
   # Check Docker status
   systemctl status docker
   
   # Restart Docker service (if needed)
   sudo systemctl restart docker
   ```

4. **Port Conflicts**

   ```bash
   # Check port usage
   netstat -tulpn | grep :8081
   
   # Stop conflicting process or modify configuration
   ```

5. **API Key Issues**

   ```bash
   # Verify environment variable loading
   docker compose config | grep -i api_key
   
   # Reconfigure .env file
   vi .env
   ```

6. **Insufficient Memory**

   ```bash
   # Check system resources
   free -h
   df -h
   
   # Clean Docker resources
   docker system prune -f
   ```

---

**Note**: Ensure your Ubuntu system has at least 8GB of memory and 20GB of available disk space for optimal performance.
