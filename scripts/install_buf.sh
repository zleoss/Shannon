#!/bin/bash
# =============================================================================
# 文件: scripts/install_buf.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 安装 buf 工具用于 protobuf 管理
# 【关键内容】 下载并安装 buf CLI；配置 protobuf 编译环境
#             确保版本兼容性
# 【协作关系】 被 generate_protos_local.sh 依赖；提供 protobuf 编译工具链
# =============================================================================
# Install buf for protobuf management

set -e

echo "Installing buf..."

# Detect OS and architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

if [ "$ARCH" = "x86_64" ]; then
    ARCH="x86_64"
elif [ "$ARCH" = "arm64" ] || [ "$ARCH" = "aarch64" ]; then
    ARCH="arm64"
fi

# Download buf
BUF_VERSION="1.28.1"
BUF_URL="https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-${OS}-${ARCH}"

echo "Downloading buf from ${BUF_URL}..."
curl -sSL "${BUF_URL}" -o /tmp/buf
chmod +x /tmp/buf

# Check if user has permission to install to /usr/local/bin
if [ -w /usr/local/bin ]; then
    mv /tmp/buf /usr/local/bin/buf
    echo "buf installed to /usr/local/bin/buf"
else
    mkdir -p "$HOME/.local/bin"
    mv /tmp/buf "$HOME/.local/bin/buf"
    echo "buf installed to $HOME/.local/bin/buf"
    echo "Make sure $HOME/.local/bin is in your PATH"
fi

echo "buf installation complete!"
export PATH="$HOME/.local/bin:$PATH"
"$HOME/.local/bin/buf" --version || true
