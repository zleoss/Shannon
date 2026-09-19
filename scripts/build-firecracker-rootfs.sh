#!/bin/bash
# =============================================================================
# 文件: scripts/build-firecracker-rootfs.sh
# -----------------------------------------------------------------------------
# 【一句话功能】 构建 Firecracker microVM 根文件系统镜像与客户机 agent
# 【关键内容】 使用 Docker buildx 构建 ARM64 交叉编译镜像
#             生成 rootfs 镜像供 Firecracker 微虚拟机使用
# 【协作关系】 被 Firecracker 沙箱部署流程调用；依赖 Docker buildx
# =============================================================================
# Build Firecracker microVM rootfs image and guest agent
#
# Prerequisites:
# - Docker with buildx (for ARM64 cross-build)
# - cross (cargo install cross) for Rust cross-compilation
# - Root access for mounting ext4 image (or use fakeroot)
#
# Output:
# - rust/firecracker-executor/rootfs/rootfs.ext4 - Root filesystem image
# - rust/firecracker-executor/rootfs/vmlinux.bin - Linux kernel

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
EXECUTOR_DIR="${PROJECT_ROOT}/rust/firecracker-executor"
GUEST_AGENT_DIR="${EXECUTOR_DIR}/guest-agent"
ROOTFS_DIR="${EXECUTOR_DIR}/rootfs"
OUTPUT_DIR="${ROOTFS_DIR}"

# Configuration
ROOTFS_SIZE_MB="${ROOTFS_SIZE_MB:-2048}"
FIRECRACKER_VERSION="${FIRECRACKER_VERSION:-v1.5.0}"
KERNEL_VERSION="${KERNEL_VERSION:-5.10.198}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

check_dependencies() {
    log_info "Checking dependencies..."

    local missing=()

    if ! command -v docker &> /dev/null; then
        missing+=("docker")
    fi

    if ! command -v cross &> /dev/null; then
        log_warn "cross not found. Install with: cargo install cross"
        log_warn "Falling back to cargo build (requires musl toolchain)"
    fi

    if [[ ${#missing[@]} -gt 0 ]]; then
        log_error "Missing dependencies: ${missing[*]}"
        exit 1
    fi
}

build_guest_agent() {
    log_info "Building guest-agent for aarch64-unknown-linux-musl..."

    cd "${GUEST_AGENT_DIR}"

    if command -v cross &> /dev/null; then
        # Use cross for easier cross-compilation
        cross build --release --target aarch64-unknown-linux-musl
        cp target/aarch64-unknown-linux-musl/release/guest-agent "${ROOTFS_DIR}/guest-agent"
    else
        # Fallback to cargo with musl toolchain
        # Requires: rustup target add aarch64-unknown-linux-musl
        # And musl cross-compiler: aarch64-linux-musl-gcc
        cargo build --release --target aarch64-unknown-linux-musl
        cp target/aarch64-unknown-linux-musl/release/guest-agent "${ROOTFS_DIR}/guest-agent"
    fi

    log_info "Guest agent built: ${ROOTFS_DIR}/guest-agent"
    ls -lh "${ROOTFS_DIR}/guest-agent"
}

build_docker_image() {
    log_info "Building Docker image for ARM64..."

    cd "${ROOTFS_DIR}"

    # Enable buildx for cross-platform builds
    docker buildx create --name firecracker-builder --use 2>/dev/null || docker buildx use firecracker-builder

    docker buildx build \
        --platform linux/arm64 \
        --load \
        -t firecracker-rootfs:latest \
        .

    log_info "Docker image built: firecracker-rootfs:latest"
}

export_rootfs() {
    log_info "Exporting rootfs to ext4 image..."

    local container_id
    local tar_file="${OUTPUT_DIR}/rootfs.tar"
    local ext4_file="${OUTPUT_DIR}/rootfs.ext4"
    local mount_point="${OUTPUT_DIR}/mnt"

    # Create container and export filesystem
    container_id=$(docker create --platform linux/arm64 firecracker-rootfs:latest)
    docker export "${container_id}" -o "${tar_file}"
    docker rm "${container_id}"

    log_info "Container exported to tarball: ${tar_file}"

    # Create ext4 image
    log_info "Creating ext4 filesystem (${ROOTFS_SIZE_MB}MB)..."
    dd if=/dev/zero of="${ext4_file}" bs=1M count="${ROOTFS_SIZE_MB}" status=progress
    mkfs.ext4 -F "${ext4_file}"

    # Mount and extract
    mkdir -p "${mount_point}"

    if [[ $EUID -eq 0 ]]; then
        # Running as root - mount directly
        mount -o loop "${ext4_file}" "${mount_point}"
        tar -xf "${tar_file}" -C "${mount_point}"

        # Create essential directories
        mkdir -p "${mount_point}/dev" "${mount_point}/proc" "${mount_point}/sys" "${mount_point}/run"

        umount "${mount_point}"
    else
        # Not root - try with sudo or fakeroot
        if command -v fakeroot &> /dev/null && command -v fakechroot &> /dev/null; then
            log_warn "Using fakeroot/fakechroot (limited functionality)"
            # This won't work well for device nodes, but basic extraction will work
            fakeroot -- bash -c "
                mount -o loop '${ext4_file}' '${mount_point}' 2>/dev/null || {
                    echo 'Mount failed - using debugfs'
                    exit 1
                }
                tar -xf '${tar_file}' -C '${mount_point}'
                umount '${mount_point}'
            " || {
                log_warn "Fakeroot mount failed, using debugfs method"
                extract_with_debugfs "${tar_file}" "${ext4_file}"
            }
        else
            log_info "Requesting sudo for mounting ext4 image..."
            sudo mount -o loop "${ext4_file}" "${mount_point}"
            sudo tar -xf "${tar_file}" -C "${mount_point}"
            sudo mkdir -p "${mount_point}/dev" "${mount_point}/proc" "${mount_point}/sys" "${mount_point}/run"
            sudo umount "${mount_point}"
        fi
    fi

    # Cleanup
    rm -f "${tar_file}"
    rmdir "${mount_point}" 2>/dev/null || true

    log_info "Rootfs created: ${ext4_file}"
    ls -lh "${ext4_file}"
}

extract_with_debugfs() {
    local tar_file="$1"
    local ext4_file="$2"

    log_warn "debugfs extraction not fully implemented - use sudo or run as root"
    log_error "Cannot create rootfs without root access for loop mounting"
    exit 1
}

download_kernel() {
    log_info "Downloading ARM64 kernel..."

    local kernel_url="https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/vmlinux-${KERNEL_VERSION}-aarch64.bin"
    local kernel_file="${OUTPUT_DIR}/vmlinux.bin"

    if [[ -f "${kernel_file}" ]]; then
        log_info "Kernel already exists: ${kernel_file}"
        return
    fi

    curl -L -o "${kernel_file}" "${kernel_url}"

    log_info "Kernel downloaded: ${kernel_file}"
    ls -lh "${kernel_file}"
}

verify_outputs() {
    log_info "Verifying outputs..."

    local missing=()

    [[ ! -f "${OUTPUT_DIR}/rootfs.ext4" ]] && missing+=("rootfs.ext4")
    [[ ! -f "${OUTPUT_DIR}/vmlinux.bin" ]] && missing+=("vmlinux.bin")

    if [[ ${#missing[@]} -gt 0 ]]; then
        log_error "Missing outputs: ${missing[*]}"
        exit 1
    fi

    log_info "Build complete!"
    echo ""
    echo "Outputs:"
    echo "  Rootfs:  ${OUTPUT_DIR}/rootfs.ext4"
    echo "  Kernel:  ${OUTPUT_DIR}/vmlinux.bin"
    echo ""
    echo "To use with firecracker-executor, set:"
    echo "  FIRECRACKER_KERNEL_IMAGE=${OUTPUT_DIR}/vmlinux.bin"
    echo "  FIRECRACKER_ROOTFS_IMAGE=${OUTPUT_DIR}/rootfs.ext4"
}

usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Build Firecracker microVM rootfs and guest agent.

Options:
  --agent-only    Only build the guest agent (skip rootfs)
  --rootfs-only   Only build the rootfs (assumes guest agent exists)
  --kernel-only   Only download the kernel
  --help          Show this help message

Environment Variables:
  ROOTFS_SIZE_MB       Size of rootfs image in MB (default: 2048)
  FIRECRACKER_VERSION  Firecracker release version (default: v1.5.0)
  KERNEL_VERSION       Kernel version to download (default: 5.10.198)
EOF
}

main() {
    local agent_only=false
    local rootfs_only=false
    local kernel_only=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            --agent-only)
                agent_only=true
                shift
                ;;
            --rootfs-only)
                rootfs_only=true
                shift
                ;;
            --kernel-only)
                kernel_only=true
                shift
                ;;
            --help)
                usage
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                exit 1
                ;;
        esac
    done

    check_dependencies

    if [[ "${kernel_only}" == "true" ]]; then
        download_kernel
        exit 0
    fi

    if [[ "${rootfs_only}" != "true" ]]; then
        build_guest_agent
    fi

    if [[ "${agent_only}" == "true" ]]; then
        exit 0
    fi

    build_docker_image
    export_rootfs
    download_kernel
    verify_outputs
}

main "$@"
