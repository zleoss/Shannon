#!/usr/bin/env bash
# =============================================================================
# 文件: scripts/generate_protos_local.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 本地生成 protobuf 文件，不依赖 BSR 依赖
# 【关键内容】 确保所有服务的版本兼容性；调用 protoc 编译 .proto 文件
#             输出到各语言对应的生成代码目录
# 【协作关系】 被 make proto 调用；依赖 install_buf.sh 安装的 protoc 工具链
# =============================================================================
# Generate protobuf files locally without BSR dependencies
# IMPORTANT: This script ensures version compatibility across all services

set -euo pipefail

cd "$(dirname "$0")/.."

# Add Go bin to PATH
export PATH="$HOME/go/bin:$PATH"

echo "Generating protobuf files locally..."
echo "Note: Using language-specific protoc to ensure compatibility"

# For Go - protoc is compatible across versions for Go
if command -v protoc &> /dev/null; then
    echo "System protoc found: $(protoc --version)"
else
    echo "Warning: protoc is not installed for Go generation."
    echo "On macOS: brew install protobuf"
    echo "On Ubuntu: apt-get install protobuf-compiler"
fi

# Ensure Go protoc plugins are installed
if ! command -v protoc-gen-go &> /dev/null; then
    echo "Installing Go protoc plugins..."
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
fi

# Use a venv to avoid PEP 668 "externally-managed-environment" errors
VENV_DIR=".venv-proto"
if [ ! -d "$VENV_DIR" ]; then
    echo "Creating proto generation venv..."
    python3 -m venv "$VENV_DIR"
fi
source "$VENV_DIR/bin/activate"

# Ensure correct protobuf 5.x + grpc tools in the venv
PROTOBUF_VERSION=$(python3 -c "import google.protobuf; print(google.protobuf.__version__)" 2>/dev/null || echo "none")
if [[ ! "$PROTOBUF_VERSION" =~ ^5\. ]]; then
    echo "Installing protobuf 5.29.2 + grpcio-tools in venv..."
    pip3 install --quiet protobuf==5.29.2 grpcio-tools==1.68.1
fi

cd protos

# Generate Go files (Go is flexible with protoc versions)
if command -v protoc &> /dev/null; then
    echo "Generating Go protobuf files with system protoc..."
    mkdir -p ../go/orchestrator/internal/pb
    for proto in $(find . -name "*.proto" -type f); do
        protoc \
            --go_out=../go/orchestrator/internal/pb \
            --go_opt=paths=source_relative \
            --go-grpc_out=../go/orchestrator/internal/pb \
            --go-grpc_opt=paths=source_relative \
            --go-grpc_opt=require_unimplemented_servers=false \
            -I . \
            "$proto"
    done
else
    echo "Skipping Go generation - protoc not installed"
fi

# Generate Python files using grpc_tools.protoc (ensures correct version)
echo "Generating Python protobuf files with grpc_tools.protoc (protobuf 5.x)..."
mkdir -p gen/python
# Use Python's bundled protoc to ensure version compatibility
python3 -m grpc_tools.protoc \
    --python_out=gen/python \
    --grpc_python_out=gen/python \
    --pyi_out=gen/python \
    -I . \
    $(find . -name "*.proto" -type f)

# Copy Python files to llm-service
echo "Copying Python protobuf files to llm-service..."
mkdir -p ../python/llm-service/llm_service/grpc_gen
cp -r gen/python/* ../python/llm-service/llm_service/grpc_gen/ 2>/dev/null || true

echo "Protobuf generation complete!"