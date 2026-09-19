// =============================================================================
// 文件: rust/firecracker-executor/src/config.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Settings::from_env：读取 firecracker_bin、vsock_cid_base、vsock_port、pool_warm_count、efs_mount_point 等。
// 【关键内容】
//   Settings 结构体（config.rs:3-4）
//   from_env 环境变量装配（config.rs:25）
//   字段含 firecracker_bin / vsock_cid_base / vsock_port / pool_warm_count / efs_mount_point
// 【协作关系】
//   被 main.rs、vm_pool、vm_runner、firecracker_api 读取统一运行参数。
//   由 .env 与 docker-compose 环境变量提供输入。
// =============================================================================
use std::path::PathBuf;

#[derive(Clone, Debug)]
pub struct Settings {
    pub firecracker_bin: PathBuf,
    pub jailer_bin: Option<PathBuf>,
    pub use_jailer: bool,
    pub kernel_image: PathBuf,
    pub rootfs_image: PathBuf,
    pub api_timeout_ms: u64,
    pub vcpu_count: u32,
    pub mem_size_mib: u32,
    pub boot_args: String,
    pub vsock_cid_base: u32,  // Per-VM CID = base + allocated index
    pub vsock_port: u32,
    pub efs_mount_point: PathBuf,  // EFS mount for session workspaces
    pub executor_timeout_seconds: u32,
    pub socket_dir: PathBuf,
    pub pool_warm_count: usize,
    pub pool_max_count: usize,
    pub session_idle_timeout_seconds: u64,
}

impl Settings {
    pub fn from_env() -> Self {
        let firecracker_bin = std::env::var("FIRECRACKER_BIN")
            .unwrap_or_else(|_| "/usr/local/bin/firecracker".to_string())
            .into();
        let jailer_bin = std::env::var("FIRECRACKER_JAILER_BIN").ok().map(Into::into);
        let use_jailer = std::env::var("FIRECRACKER_USE_JAILER")
            .map(|v| v == "1" || v.eq_ignore_ascii_case("true"))
            .unwrap_or(false);
        let kernel_image = std::env::var("FIRECRACKER_KERNEL_IMAGE")
            .unwrap_or_else(|_| "/var/lib/firecracker/vmlinux.bin".to_string())
            .into();
        let rootfs_image = std::env::var("FIRECRACKER_ROOTFS_IMAGE")
            .unwrap_or_else(|_| "/var/lib/firecracker/rootfs.ext4".to_string())
            .into();
        let api_timeout_ms = std::env::var("FIRECRACKER_API_TIMEOUT_MS")
            .ok()
            .and_then(|v| v.parse::<u64>().ok())
            .unwrap_or(3000);
        let vcpu_count = std::env::var("FIRECRACKER_VCPU_COUNT")
            .ok()
            .and_then(|v| v.parse::<u32>().ok())
            .unwrap_or(2);
        let mem_size_mib = std::env::var("FIRECRACKER_MEMORY_MB")
            .ok()
            .and_then(|v| v.parse::<u32>().ok())
            .unwrap_or(1024);
        // Boot args MUST include root device and init for the VM to boot
        let boot_args = std::env::var("FIRECRACKER_BOOT_ARGS").unwrap_or_else(|_| {
            "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/init.sh".to_string()
        });
        // vsock_cid is now per-VM, this is just the base (VMs get base + index)
        let vsock_cid_base = std::env::var("FIRECRACKER_VSOCK_CID_BASE")
            .ok()
            .and_then(|v| v.parse::<u32>().ok())
            .unwrap_or(100);
        let vsock_port = std::env::var("FIRECRACKER_VSOCK_PORT")
            .ok()
            .and_then(|v| v.parse::<u32>().ok())
            .unwrap_or(5005);
        let executor_timeout_seconds = std::env::var("FIRECRACKER_EXECUTOR_TIMEOUT_SECONDS")
            .ok()
            .and_then(|v| v.parse::<u32>().ok())
            .unwrap_or(300);
        let socket_dir = std::env::var("FIRECRACKER_SOCKET_DIR")
            .unwrap_or_else(|_| "/tmp/firecracker".to_string())
            .into();
        let pool_warm_count = std::env::var("FIRECRACKER_POOL_WARM_COUNT")
            .ok()
            .and_then(|v| v.parse::<usize>().ok())
            .unwrap_or(3);
        let pool_max_count = std::env::var("FIRECRACKER_POOL_MAX_COUNT")
            .ok()
            .and_then(|v| v.parse::<usize>().ok())
            .unwrap_or(20);
        let session_idle_timeout_seconds = std::env::var("FIRECRACKER_SESSION_IDLE_TIMEOUT_SECONDS")
            .ok()
            .and_then(|v| v.parse::<u64>().ok())
            .unwrap_or(300);

        let efs_mount_point = std::env::var("FIRECRACKER_EFS_MOUNT")
            .unwrap_or_else(|_| "/mnt/shannon-sessions".to_string())
            .into();

        Self {
            firecracker_bin,
            jailer_bin,
            use_jailer,
            kernel_image,
            rootfs_image,
            api_timeout_ms,
            vcpu_count,
            mem_size_mib,
            boot_args,
            vsock_cid_base,
            vsock_port,
            executor_timeout_seconds,
            socket_dir,
            pool_warm_count,
            pool_max_count,
            session_idle_timeout_seconds,
            efs_mount_point,
        }
    }
}
