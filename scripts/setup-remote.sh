#!/bin/bash
# =============================================================================
# 文件: scripts/setup-remote.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 在远程 Ubuntu 服务器上安装 Shannon 全栈的自动化脚本
# 【关键内容】 安装 Docker / Docker Compose / Temporal / Qdrant 等依赖
#             配置环境变量与网络；支持自定义安装路径与版本
# 【协作关系】 被运维人员用于新服务器初始化；依赖 docker-compose 部署
# =============================================================================
# Setup script for remote Ubuntu server installation of Shannon stack

set -e

echo "=== Shannon Remote Setup Script ==="
echo ""

# Check if buf is installed
if ! command -v buf &> /dev/null; then
    echo "Installing buf..."
    # Install buf
    curl -sSL https://github.com/bufbuild/buf/releases/latest/download/buf-Linux-x86_64 -o /tmp/buf
    sudo mv /tmp/buf /usr/local/bin/buf
    sudo chmod +x /usr/local/bin/buf
fi

# Check if protoc-gen-go is installed
if ! command -v protoc-gen-go &> /dev/null; then
    echo "Installing protoc-gen-go..."
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
fi

# Add Go bin to PATH if not already there
if [[ ":$PATH:" != *":$HOME/go/bin:"* ]]; then
    echo "Adding Go bin to PATH..."
    echo 'export PATH=$PATH:$HOME/go/bin' >> ~/.bashrc
    export PATH=$PATH:$HOME/go/bin
fi

# Generate proto files
echo "Generating proto files..."
cd protos
/usr/local/bin/buf generate
cd ..

echo "Proto files generated successfully!"

# Check if proto files were generated
if [ ! -d "go/orchestrator/internal/pb" ]; then
    echo "ERROR: Proto generation failed - go/orchestrator/internal/pb not found"
    exit 1
fi

# Python proto files are generated in protos/gen/python
if [ ! -d "protos/gen/python" ]; then
    echo "ERROR: Proto generation failed - protos/gen/python not found"
    exit 1
fi

# Note: Rust protobuf generation is handled separately by the Rust build process

echo ""
echo "=== Setup Complete ==="
echo ""
echo "Proto files have been generated successfully."
echo "You can now run 'make dev' to start the Shannon stack."
echo ""
echo "If you haven't set up your environment variables yet:"
echo "1. Run: make setup-env"
echo "2. Edit .env file with your API keys"
echo ""