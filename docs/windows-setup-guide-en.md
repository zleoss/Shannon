# Shannon Project Windows Local Setup Guide

## 📖 中文学习注解

### 本文核心摘要
This document (English version) helps developers set up Shannon on Windows. Since native scripts target Linux/macOS, Windows requires WSL 2 for Docker, Git Bash as terminal, manual .env copying, and separate Go/Python/Protoc installation. Key warnings include Protoc include folder and Python app execution aliases. English content with Chinese annotation.

### 章节导航
- **Prerequisites**: Git + WSL 2 + Docker Desktop + Go + Python + Protoc installation with pitfalls
- **Project Initialization**: Clone repo, manual .env configuration (root + deploy/compose)
- **Build & Start**: Using Git Bash for make setup / make dev
- **Verification & Testing**: Service verification and first task submission
- **FAQ**: Protoc include path, Python aliases, WSL network issues

### 与 AI Agent 体系的关联
Same as linux quickstarts, adapted for Windows-specific environment.

### 阅读建议
Windows users deploying Shannon for the first time must read this; focus on Prerequisites and environment pitfalls.

This document aims to help developers successfully set up and run the Shannon agent platform in a Windows environment. Since the project's native scripts are primarily designed for Linux/macOS environments, specific adaptations and configurations are required when running on Windows.

## 1. Prerequisites

Before starting, please ensure the following software is installed. **It is strongly recommended to use Git Bash as your primary terminal tool.**

1.  **Git**: [Download Link](https://git-scm.com/download/win) (Check "Git Bash" during installation)
2.  **WSL 2 (Windows Subsystem for Linux)**:
    *   **Necessity**: Docker Desktop on Windows relies on WSL 2 as the backend engine to run Linux containers. Compared to the legacy Hyper-V backend, WSL 2 starts faster, uses fewer resources, and provides full Linux kernel compatibility, which is crucial for running complex microservice architectures.
    *   **Installation**: Open PowerShell as Administrator, run `wsl --install`, and then restart your computer.
    *   **Official Guide/Manual Download**: [Microsoft Official Documentation](https://learn.microsoft.com/en-us/windows/wsl/install)
3.  **Docker Desktop**: [Download Link](https://www.docker.com/products/docker-desktop/)
    *   After installation, ensure "Use the WSL 2 based engine" is checked in settings.
4.  **Go (Golang)**: [Download Link](https://go.dev/dl/) (Recommended 1.21+)
5.  **Python**: [Download Link](https://www.python.org/downloads/) (Recommended 3.10+)
    *   **Important**: Check "Add Python to PATH" during installation.
    *   **Pitfall**: Search for "App execution aliases" in Windows Settings, and **turn off** the toggles for `python.exe` and `python3.exe` to prevent the system from invoking the Microsoft Store placeholder.
6.  **Protoc (Protocol Buffers Compiler)**: [Download Link](https://github.com/protocolbuffers/protobuf/releases)
    *   **Note**: Download `protoc-xx.x-win64.zip` (do not download osx or linux versions).
    *   **Configuration**: Extract to a fixed directory (e.g., `D:\Tools\protoc`), and add the `bin` directory to your system environment variable `Path`.
    *   **Critical**: Ensure the extracted directory contains the `include` folder, otherwise code generation will fail with `google/protobuf/timestamp.proto not found` error.

---

## 2. Project Initialization and Configuration

### 2.1 Clone Project
```bash
git clone https://github.com/Kocoro-lab/Shannon.git
cd Shannon
```

### 2.2 Configure Environment Variables (.env)
The `ln -sf` command in `make setup` cannot be run directly on Windows, so manual copying is required.

**Applicable Scenarios for the two .env files:**
*   **Root `.env`**: Used by local development scripts (such as `scripts/setup_python_wasi.sh`).
*   **`deploy/compose/.env`**: **Core Configuration**. Loaded by Docker Compose when starting services. **You must fill in the API Key in this file, otherwise the service will not run.**

Execute in **Git Bash**:
```bash
# 1. Root directory configuration
cp .env.example .env

# 2. Docker Compose configuration (Manual copy instead of symlink)
cd deploy/compose
cp ../../.env .env
cd ../..
```

**Note**: Since these are two independent files created by copying, **please ensure you synchronize changes to both files when modifying configurations** (or use `mklink /H` to create a hard link).

Open the `.env` file and fill in the necessary Keys:
```properties
OPENAI_API_KEY=sk-xxxxxx
# If Google Search is needed, refer to Section 7 of this document for configuration
```

---

## 3. Core Script Adaptation (Proto Generation)

The original project script `scripts/generate_protos_local.sh` has compatibility issues on Windows (missing `python3`/`pip3` commands, path issues).

### 3.1 Modify Script
Please use an editor to open `scripts/generate_protos_local.sh` and make the following two key modifications:

**1. Add Python Command Detection Logic at the beginning of the file**
Insert the following code block after `set -euo pipefail` to automatically identify `python` or `python3` commands in the Windows environment:

```bash
# Windows compatibility: Determine Python and Pip commands
if command -v python3 &> /dev/null; then
    PYTHON_CMD="python3"
elif command -v python &> /dev/null; then
    PYTHON_CMD="python"
else
    echo "Error: Python not found"
    exit 1
fi

if command -v pip3 &> /dev/null; then
    PIP_CMD="pip3"
elif command -v pip &> /dev/null; then
    PIP_CMD="pip"
else
    # Fallback to python -m pip if pip command is not found
    PIP_CMD="$PYTHON_CMD -m pip"
fi
```

**2. Replace Hardcoded Commands**
Replace all instances of `python3` in the script with `"$PYTHON_CMD"`, and all instances of `pip3` with `"$PIP_CMD"`.

For example:
*   Original: `pip3 install grpcio-tools...`
*   Modified: `"$PIP_CMD" install grpcio-tools...`

*   Original: `python3 -m grpc_tools.protoc...`
*   Modified: `"$PYTHON_CMD" -m grpc_tools.protoc...`

### 3.2 Execute Generation
Run in Git Bash:
```bash
./scripts/generate_protos_local.sh
```
Success indicator: Output shows `Protobuf generation complete!` with no errors.

---

## 4. WASI Environment Configuration

Used for Python code sandbox execution.
```bash
./scripts/setup_python_wasi.sh
```
*   When prompted to download RustPython, select `n` (No).
*   When prompted to update .env, select `y` (Yes).

---

## 5. Start Services (Docker Compose)

On Windows, it is recommended to explicitly specify the project name and file path.

### 5.1 Start Backend Services
```bash
cd deploy/compose
docker compose -p shannon up -d
```

### 5.2 Common Issue: Temporal Not Ready
**Symptom**: Task submission fails with `Failed to submit task: Temporal not ready`, Orchestrator logs show `context deadline exceeded`.
**Cause**: Temporal starts slowly, and Orchestrator connection attempts time out before it is ready.

**Solution**:
1.  Ensure Temporal UI (`http://localhost:8088`) is accessible.
2.  **Register Namespace** (If `default` namespace is missing in UI):
    ```bash
    docker compose -p shannon exec temporal temporal operator namespace register default
    ```
3.  **Restart Orchestrator** (Critical Step):
    ```bash
    docker compose -p shannon restart orchestrator
    ```

---

## 6. Configure Google Web Search (Optional)

To enable the Agent's web search capability, you need to configure Google Custom Search.

1.  **Get API Key**: [Google Cloud Console](https://console.cloud.google.com/apis/credentials) -> Create Credentials -> API Key.
2.  **Get Engine ID**: [Programmable Search Engine](https://programmablesearchengine.google.com/) -> Create -> Select "Search the entire web".
3.  **Modify .env**:
    ```properties
    WEB_SEARCH_PROVIDER=google
    GOOGLE_SEARCH_API_KEY=Your_API_KEY
    GOOGLE_SEARCH_ENGINE_ID=Your_ENGINE_ID
    ```
4.  **Restart Services**:
    ```bash
    cd deploy/compose
    docker compose -p shannon up -d
    ```

---

## 7. Common Commands Cheat Sheet

| Operation | Command (Git Bash) |
| :--- | :--- |
| **Start Services** | `cd deploy/compose && docker compose -p shannon up -d` |
| **Stop Services** | `cd deploy/compose && docker compose -p shannon down` |
| **Check Status** | `cd deploy/compose && docker compose -p shannon ps` |
| **View Logs** | `cd deploy/compose && docker logs -f shannon-orchestrator-1` |
| **Restart Orchestrator** | `cd deploy/compose && docker compose -p shannon restart orchestrator` |
