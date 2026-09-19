# Release Process

## 📖 中文学习注解

### 本文核心摘要
本文档说明 Shannon 的版本发布流程。版本通过 Git Tag 触发 CI 自动构建——构建所有服务的 Docker 镜像（agent-core/orchestrator/llm-service/gateway/playwright-service）、构建桌面应用（macOS 通用/Linux AppImage+deb/Windows MSI+NSIS）、推送到 Docker Hub 并创建 GitHub Release。文档包含从 CHANGELOG 更新到 tag 推送的完整操作步骤，以及用户的一行安装脚本。

### 章节导航
- **Cutting a Release**: 更新 CHANGELOG → 更新版本号 → git tag + push → CI 自动构建
- **Docker Hub Images**: 各服务的 Docker 镜像命名和标签规范（version pin + latest）
- **User Installation**: 一行 curl 安装脚本和指定版本安装方式
- **Desktop Apps**: Tauri 构建的桌面端自动更新机制

### 与 AI Agent 体系的关联
- GitHub Actions CI 位于 `.github/workflows/`
- Docker 构建文件：各服务的 Dockerfile
- Tauri 配置：`src-tauri/` 目录
- 发布管理涉及所有三个代码模块（Go/Rust/Python）

### 阅读建议
运维和发布经理必读；普通开发者了解流程即可。

## Overview

Shannon releases are triggered by git tags. The CI workflow builds Docker images for all services, builds desktop apps for macOS/Windows/Linux, pushes images to Docker Hub, and creates a GitHub Release with desktop binaries attached.

## Cutting a Release

1. Update `CHANGELOG.md` with the release notes
2. Ensure version strings are updated where needed (see checklist below)
3. Tag the release:
   ```bash
   git tag v0.4.0
   git push origin v0.4.0
   ```
4. CI automatically:
   - Builds Docker images for agent-core, orchestrator, llm-service, gateway, playwright-service
   - Pushes to Docker Hub as `waylandzhang/<service>:v0.4.0` and `:latest`
   - Builds desktop apps (macOS universal, Windows MSI/NSIS, Linux AppImage/deb)
   - Creates a GitHub Release with desktop binaries and `latest.json` for Tauri auto-update

## Docker Hub Images

All images are published under the `waylandzhang` Docker Hub org:

```
waylandzhang/agent-core:<version>
waylandzhang/orchestrator:<version>
waylandzhang/llm-service:<version>
waylandzhang/gateway:<version>
waylandzhang/playwright-service:<version>
```

Tags: `v0.4.0` (pinned) and `latest` (rolling).

## User Installation

End users install via the one-liner:

```bash
curl -fsSL https://raw.githubusercontent.com/Kocoro-lab/Shannon/v0.4.0/scripts/install.sh | bash
```

Or with a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/Kocoro-lab/Shannon/main/scripts/install.sh | SHANNON_VERSION=v0.4.0 bash
```

This downloads `docker-compose.release.yml`, config files, migrations, the WASM interpreter, and starts all services.

## Manual Trigger

The release workflow can also be triggered manually from GitHub Actions:

1. Go to Actions > "Release - Build and Push Docker Images"
2. Click "Run workflow"
3. Enter the version tag (e.g., `v0.4.0`)

## Version Bump Checklist

Before tagging a new release:

- [ ] `CHANGELOG.md` updated with notable changes
- [ ] `scripts/install.sh` default version updated (`SHANNON_VERSION`)
- [ ] `desktop/src-tauri/tauri.conf.json` version field (desktop app version)
- [ ] `desktop/package.json` version field
- [ ] Any hardcoded version references in docs
- [ ] Run `make ci` to verify tests pass
- [ ] Verify `docker-compose.release.yml` matches current `docker-compose.yml` structure (env vars, volumes, healthchecks)

## Hotfix Process

For urgent fixes on a released version:

1. Create a branch from the release tag: `git checkout -b hotfix/v0.4.1 v0.4.0`
2. Apply the fix
3. Tag: `git tag v0.4.1`
4. Push both: `git push origin hotfix/v0.4.1 v0.4.1`
5. Merge the hotfix branch back to `main`
