// =============================================================================
// 文件: rust/firecracker-executor/src/vm_pool.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   VmPool：warm pool + session 亲和 + ext4 + 清理的微虚拟机池管理。
// 【关键内容】
//   VmInstance（vm_pool.rs:17）/ VmPool（:38）
//   allocate_cid（:69）/ acquire（:77）/ release（:145）
//   maintain_warm_pool（:200）/ evict_idle_sessions（:248）
//   stats / PoolStats（vm_pool.rs:281, 580）
//   spawn_firecracker_process（:368）；ext4 挂载逻辑（:457+）
// 【协作关系】
//   被 vm_runner acquire/release 调用以获取/归还微虚拟机实例。
//   依赖 firecracker_api 启动 VM、workspace_sync 维护文件系统。
// =============================================================================
use crate::config::Settings;
use crate::firecracker_api::FirecrackerApi;
use crate::workspace_sync;
use anyhow::{anyhow, Result};
use std::collections::HashMap;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU32, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Instant;
use tokio::sync::RwLock;
use tokio::time::{interval, Duration};
use tracing::{debug, info, warn};
use uuid::Uuid;

/// A single Firecracker VM instance
pub struct VmInstance {
    pub id: String,
    pub process: Child,
    pub api_socket: PathBuf,
    pub vsock_uds: PathBuf,
    pub vsock_cid: u32,  // Unique CID for this VM
    pub temp_dir: PathBuf,
    pub rootfs_copy: PathBuf,  // Per-VM rootfs copy
    pub workspace_dir: Option<PathBuf>,  // Mounted session workspace
    pub created_at: Instant,
    pub last_used: Instant,
    pub api: FirecrackerApi,
}

impl VmInstance {
    pub fn update_last_used(&mut self) {
        self.last_used = Instant::now();
    }
}

/// Thread-safe VM pool with warm pool and session affinity
pub struct VmPool {
    settings: Settings,
    warm_pool: RwLock<Vec<VmInstance>>,
    session_map: RwLock<HashMap<String, VmInstance>>,
    active_count: AtomicUsize,
    cid_counter: AtomicU32,  // For unique CID allocation
    workspace_locks: RwLock<HashMap<String, Arc<tokio::sync::Mutex<()>>>>,
}

impl VmPool {
    pub fn new(settings: Settings) -> Arc<Self> {
        Arc::new(Self {
            cid_counter: AtomicU32::new(settings.vsock_cid_base),
            settings,
            warm_pool: RwLock::new(Vec::new()),
            session_map: RwLock::new(HashMap::new()),
            active_count: AtomicUsize::new(0),
            workspace_locks: RwLock::new(HashMap::new()),
        })
    }

    /// Get or create a workspace lock for a session.
    /// Caller is responsible for holding the guard across the full execution.
    pub async fn get_workspace_lock(&self, session_id: &str) -> Arc<tokio::sync::Mutex<()>> {
        let mut locks = self.workspace_locks.write().await;
        locks.entry(session_id.to_string())
            .or_insert_with(|| Arc::new(tokio::sync::Mutex::new(())))
            .clone()
    }

    /// Allocate a unique vsock CID for a new VM
    fn allocate_cid(&self) -> u32 {
        // CIDs 0-2 are reserved (host, local, hypervisor)
        // We start from cid_base (default 100) and increment
        self.cid_counter.fetch_add(1, Ordering::SeqCst)
    }

    /// Acquire a VM for execution. If session_id is provided, returns the same VM
    /// for that session (session affinity).
    pub async fn acquire(
        &self,
        session_id: Option<String>,
        workspace_path: Option<&str>,
    ) -> Result<VmInstance> {
        // Session affinity is only valid when no workspace ext4 is involved.
        // Workspace VMs must be spawned fresh so we can safely sync directory ↔ ext4.
        if workspace_path.is_none() {
            if let Some(ref sid) = session_id {
                let mut sessions = self.session_map.write().await;
                if let Some(mut vm) = sessions.remove(sid) {
                    vm.update_last_used();
                    debug!(session_id = %sid, vm_id = %vm.id, "Returning session-affinity VM");
                    return Ok(vm);
                }
            }
        }

        // Try to get from warm pool - but only if no workspace required
        // Warm pool VMs don't have workspace ext4 drives configured, so they can't
        // mount session workspaces. When workspace_path is provided, we must spawn fresh.
        if workspace_path.is_none() {
            let mut pool = self.warm_pool.write().await;
            if let Some(mut vm) = pool.pop() {
                vm.update_last_used();
                debug!(vm_id = %vm.id, "Acquired VM from warm pool (no workspace needed)");
                return Ok(vm);
            }
        } else {
            debug!(workspace_path = ?workspace_path, "Bypassing warm pool - workspace requires fresh VM with ext4 mount");
        }

        // Atomic check-and-increment to prevent race condition (P1 fix)
        loop {
            let current = self.active_count.load(Ordering::SeqCst);
            if current >= self.settings.pool_max_count {
                return Err(anyhow!(
                    "VM pool exhausted: {} active VMs (max {})",
                    current,
                    self.settings.pool_max_count
                ));
            }

            // Compare-and-swap to prevent race
            if self
                .active_count
                .compare_exchange(current, current + 1, Ordering::SeqCst, Ordering::SeqCst)
                .is_ok()
            {
                break;
            }
            // If CAS failed, another thread got there first - loop and retry
        }

        // Spawn a new VM
        match self.spawn_vm(workspace_path).await {
            Ok(vm) => {
                info!(vm_id = %vm.id, cid = vm.vsock_cid, "Spawned new VM");
                Ok(vm)
            }
            Err(e) => {
                self.active_count.fetch_sub(1, Ordering::SeqCst);
                Err(e)
            }
        }
    }

    /// Release a VM back to the pool
    pub async fn release(&self, mut vm: VmInstance, session_id: Option<String>, tainted: bool) {
        if tainted {
            info!(vm_id = %vm.id, "Terminating tainted VM");
            self.terminate_vm(&mut vm);
            self.active_count.fetch_sub(1, Ordering::SeqCst);
            return;
        }

        // If session_id provided, keep in session map for affinity
        if let Some(sid) = session_id {
            vm.update_last_used();
            let mut sessions = self.session_map.write().await;
            debug!(session_id = %sid, vm_id = %vm.id, "Storing VM for session affinity");
            sessions.insert(sid, vm);
            return;
        }

        // Session isolation: With ext4 block device approach, workspace state is in the
        // ext4 file on EFS, not in the rootfs. VMs can be safely returned to warm pool
        // because /workspace is unmounted when VM stops, and each session gets its own ext4.
        // Clear workspace_dir reference so next session gets fresh mount.
        vm.workspace_dir = None;

        // Return to warm pool if not at capacity
        let mut pool = self.warm_pool.write().await;
        if pool.len() < self.settings.pool_warm_count {
            vm.update_last_used();
            debug!(vm_id = %vm.id, warm_pool_size = pool.len() + 1, "Returning VM to warm pool");
            pool.push(vm);
        } else {
            drop(pool);
            info!(vm_id = %vm.id, "Warm pool full, terminating VM");
            self.terminate_vm(&mut vm);
            self.active_count.fetch_sub(1, Ordering::SeqCst);
        }
    }

    /// Clean workspace directory to prevent session data leakage
    fn clean_workspace(&self, workspace: &PathBuf) -> Result<()> {
        // Remove all files in workspace but keep the directory
        if workspace.exists() {
            for entry in std::fs::read_dir(workspace)? {
                let entry = entry?;
                let path = entry.path();
                if path.is_dir() {
                    std::fs::remove_dir_all(&path)?;
                } else {
                    std::fs::remove_file(&path)?;
                }
            }
        }
        Ok(())
    }

    /// Background task to maintain the warm pool at the target size
    pub async fn maintain_warm_pool(self: Arc<Self>) {
        let mut ticker = interval(Duration::from_secs(5));
        loop {
            ticker.tick().await;

            // Clean up idle session VMs
            self.evict_idle_sessions().await;

            // Spawn VMs to maintain warm pool
            let current_warm = self.warm_pool.read().await.len();
            let current_active = self.active_count.load(Ordering::SeqCst);

            if current_warm < self.settings.pool_warm_count
                && current_active < self.settings.pool_max_count
            {
                let to_spawn = (self.settings.pool_warm_count - current_warm)
                    .min(self.settings.pool_max_count - current_active);

                for _ in 0..to_spawn {
                    // Use atomic CAS here too for safety
                    let current = self.active_count.load(Ordering::SeqCst);
                    if current >= self.settings.pool_max_count {
                        break;
                    }
                    if self
                        .active_count
                        .compare_exchange(current, current + 1, Ordering::SeqCst, Ordering::SeqCst)
                        .is_err()
                    {
                        continue;
                    }

                    match self.spawn_vm(None).await {
                        Ok(vm) => {
                            info!(vm_id = %vm.id, cid = vm.vsock_cid, "Pre-warmed VM for pool");
                            self.warm_pool.write().await.push(vm);
                        }
                        Err(e) => {
                            warn!("Failed to pre-warm VM: {}", e);
                            self.active_count.fetch_sub(1, Ordering::SeqCst);
                        }
                    }
                }
            }
        }
    }

    /// Evict session VMs that have been idle for too long
    async fn evict_idle_sessions(&self) {
        let timeout = Duration::from_secs(self.settings.session_idle_timeout_seconds);
        let mut sessions = self.session_map.write().await;
        let mut to_remove = Vec::new();

        for (sid, vm) in sessions.iter() {
            if vm.last_used.elapsed() > timeout {
                to_remove.push(sid.clone());
            }
        }

        for sid in to_remove {
            if let Some(mut vm) = sessions.remove(&sid) {
                info!(session_id = %sid, vm_id = %vm.id, "Evicting idle session VM");

                // Clear workspace reference - ext4 file persists on EFS independently
                vm.workspace_dir = None;

                // Try to return to warm pool instead of terminating
                let mut pool = self.warm_pool.write().await;
                if pool.len() < self.settings.pool_warm_count {
                    vm.update_last_used();
                    pool.push(vm);
                } else {
                    drop(pool);
                    self.terminate_vm(&mut vm);
                    self.active_count.fetch_sub(1, Ordering::SeqCst);
                }
            }
        }
    }

    /// Get current pool statistics
    pub async fn stats(&self) -> PoolStats {
        let warm_count = self.warm_pool.read().await.len();
        let session_count = self.session_map.read().await.len();
        let active_count = self.active_count.load(Ordering::SeqCst);

        PoolStats {
            warm_count,
            session_count,
            active_count,
            max_count: self.settings.pool_max_count,
        }
    }

    async fn spawn_vm(&self, workspace_path: Option<&str>) -> Result<VmInstance> {
        let instance_id = Uuid::new_v4().to_string();
        let temp_dir = PathBuf::from(&self.settings.socket_dir).join(&instance_id);
        std::fs::create_dir_all(&temp_dir)?;

        let api_socket = temp_dir.join("firecracker.sock");
        let vsock_uds = temp_dir.join("vsock.sock");

        // Allocate unique CID for this VM (P0 fix)
        let vsock_cid = self.allocate_cid();

        // Create per-VM rootfs copy using copy-on-write if supported
        // Reflinks are instant and only allocate disk space for changed blocks
        let rootfs_copy = temp_dir.join("rootfs.ext4");
        Self::copy_rootfs_cow(&self.settings.rootfs_image, &rootfs_copy)?;

        let process = self.spawn_firecracker_process(&api_socket)?;
        self.wait_for_socket(&api_socket)?;

        let api = FirecrackerApi::new(
            api_socket.to_string_lossy().to_string(),
            self.settings.api_timeout_ms,
        );

        // Configure the VM with unique CID and per-VM rootfs
        self.configure_vm(&api, &vsock_uds, vsock_cid, &rootfs_copy, workspace_path).await?;

        // Pre-sync directory → ext4 before VM boot to avoid mounting a filesystem that's
        // already in use by the guest (guest init mounts /dev/vdb at boot).
        if let Some(wp) = workspace_path {
            let ext4_path = format!("{}.ext4", wp.trim_end_matches('/'));

            // 1. Check consistency - fsck if state is missing/dirty
            if let Err(e) = workspace_sync::ensure_ext4_consistent(&ext4_path) {
                warn!(workspace = %wp, error = %e, "Failed to ensure ext4 consistency");
            }

            // 2. Mark dirty BEFORE VM boot (crash recovery marker)
            if let Err(e) = workspace_sync::mark_dirty(&ext4_path) {
                warn!(workspace = %wp, error = %e, "Failed to mark workspace dirty");
            }

            // 3. Pre-sync directory → ext4
            if let Err(e) = workspace_sync::sync_directory_to_ext4(wp) {
                warn!(workspace = %wp, error = %e, "Failed to pre-sync workspace to ext4");
            }
        }

        // THEN InstanceStart happens
        self.start_instance(&api).await?;

        // Minimal boot wait - the vsock_client now implements retry with exponential backoff
        // to handle the case where guest-agent isn't ready yet (per Firecracker issue #1253).
        // We just give the kernel a moment to start, actual guest readiness is detected dynamically.
        tokio::time::sleep(Duration::from_millis(300)).await;

        // Determine workspace directory
        let workspace_dir = workspace_path.map(|p| PathBuf::from(p));

        Ok(VmInstance {
            id: instance_id,
            process,
            api_socket,
            vsock_uds,
            vsock_cid,
            temp_dir,
            rootfs_copy,
            workspace_dir,
            created_at: Instant::now(),
            last_used: Instant::now(),
            api,
        })
    }

    fn spawn_firecracker_process(&self, api_socket: &PathBuf) -> Result<Child> {
        std::fs::create_dir_all(&self.settings.socket_dir).ok();

        let cmd = if self.settings.use_jailer {
            let jailer = self
                .settings
                .jailer_bin
                .clone()
                .ok_or_else(|| anyhow!("FIRECRACKER_JAILER_BIN not set"))?;
            let chroot = self.settings.socket_dir.join("jailer");
            Command::new(jailer)
                .arg("--id")
                .arg(Uuid::new_v4().to_string())
                .arg("--exec-file")
                .arg(self.settings.firecracker_bin.clone())
                .arg("--uid")
                .arg("0")
                .arg("--gid")
                .arg("0")
                .arg("--chroot-base-dir")
                .arg(chroot)
                .arg("--")
                .arg("--api-sock")
                .arg(api_socket)
                .stdout(Stdio::null())
                .stderr(Stdio::inherit())
                .spawn()?
        } else {
            Command::new(self.settings.firecracker_bin.clone())
                .arg("--api-sock")
                .arg(api_socket)
                .stdout(Stdio::null())
                .stderr(Stdio::inherit())
                .spawn()?
        };

        Ok(cmd)
    }

    fn wait_for_socket(&self, api_socket: &PathBuf) -> Result<()> {
        let start = Instant::now();
        let timeout = std::time::Duration::from_millis(self.settings.api_timeout_ms);
        while !api_socket.exists() {
            if start.elapsed() > timeout {
                return Err(anyhow!("firecracker API socket not ready"));
            }
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
        Ok(())
    }

    async fn configure_vm(
        &self,
        api: &FirecrackerApi,
        vsock_uds: &PathBuf,
        vsock_cid: u32,
        rootfs_path: &PathBuf,
        workspace_path: Option<&str>,
    ) -> Result<()> {
        api.put_json(
            "/machine-config",
            &serde_json::json!({
                "vcpu_count": self.settings.vcpu_count,
                "mem_size_mib": self.settings.mem_size_mib
            }),
        )
        .await?;

        api.put_json(
            "/boot-source",
            &serde_json::json!({
                "kernel_image_path": self.settings.kernel_image.to_string_lossy(),
                "boot_args": self.settings.boot_args
            }),
        )
        .await?;

        // Per-VM rootfs copy, read-write but isolated (P0 fix)
        api.put_json(
            "/drives/rootfs",
            &serde_json::json!({
                "drive_id": "rootfs",
                "path_on_host": rootfs_path.to_string_lossy(),
                "is_root_device": true,
                "is_read_only": false  // rw needed for /tmp, /var, etc.
            }),
        )
        .await?;

        // Workspace persistence via per-session ext4 block device
        // Creates a sparse ext4 file on EFS, mounted as /dev/vdb in VM
        // Guest init mounts /dev/vdb to /workspace
        if let Some(wp) = workspace_path {
            let workspace_ext4 = format!("{}.ext4", wp.trim_end_matches('/'));

            // Create sparse ext4 file if it doesn't exist (first execution for this session)
            if !std::path::Path::new(&workspace_ext4).exists() {
                Self::create_workspace_ext4(&workspace_ext4, 1024)?; // 1GB sparse file
            }

            // Mount workspace ext4 as secondary drive
            info!(workspace_ext4 = %workspace_ext4, "Configuring workspace drive for VM");
            api.put_json(
                "/drives/workspace",
                &serde_json::json!({
                    "drive_id": "workspace",
                    "path_on_host": workspace_ext4,
                    "is_root_device": false,
                    "is_read_only": false
                }),
            )
            .await?;

            info!(workspace_ext4 = %workspace_ext4, "Workspace drive configured successfully");
        }

        // Unique CID per VM (P0 fix)
        api.put_json(
            "/vsock",
            &serde_json::json!({
                "guest_cid": vsock_cid,
                "uds_path": vsock_uds.to_string_lossy()
            }),
        )
        .await?;

        Ok(())
    }

    async fn start_instance(&self, api: &FirecrackerApi) -> Result<()> {
        api.put_json(
            "/actions",
            &serde_json::json!({"action_type":"InstanceStart"}),
        )
        .await
    }

    /// Create a sparse ext4 file for workspace persistence
    fn create_workspace_ext4(path: &str, size_mb: u64) -> Result<()> {
        use std::process::Command;

        // Ensure parent directory exists
        if let Some(parent) = std::path::Path::new(path).parent() {
            std::fs::create_dir_all(parent)?;
        }

        // Create sparse file (doesn't allocate disk space until written)
        let file = std::fs::File::create(path)?;
        file.set_len(size_mb * 1024 * 1024)?;
        drop(file);

        // Format as ext4
        let output = Command::new("mkfs.ext4")
            .args(["-F", "-q", path])
            .output()?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(anyhow!("mkfs.ext4 failed: {}", stderr));
        }

        info!(path = %path, size_mb = size_mb, "Created workspace ext4 file");
        Ok(())
    }

    /// Copy rootfs using copy-on-write (reflink) if filesystem supports it.
    /// Falls back to regular copy if reflinks aren't available.
    /// Reflinks are instant and only allocate disk for changed blocks.
    fn copy_rootfs_cow(src: &PathBuf, dst: &PathBuf) -> Result<()> {
        use std::process::Command;

        // Try reflink first (XFS, btrfs, APFS support this)
        let output = Command::new("cp")
            .args(["--reflink=auto", "-f"])
            .arg(src)
            .arg(dst)
            .output();

        match output {
            Ok(result) if result.status.success() => {
                debug!(src = %src.display(), dst = %dst.display(), "Copied rootfs (reflink if supported)");
                Ok(())
            }
            Ok(result) => {
                // cp failed, try regular copy
                let stderr = String::from_utf8_lossy(&result.stderr);
                debug!("cp --reflink failed ({}), falling back to std::fs::copy", stderr.trim());
                std::fs::copy(src, dst)?;
                Ok(())
            }
            Err(_) => {
                // cp command not available, use Rust copy
                std::fs::copy(src, dst)?;
                Ok(())
            }
        }
    }

    fn terminate_vm(&self, vm: &mut VmInstance) {
        if let Err(e) = vm.process.kill() {
            debug!(vm_id = %vm.id, "Failed to kill firecracker process: {}", e);
        }
        let _ = vm.process.wait();

        // Clean up temp directory including rootfs copy
        if let Err(e) = std::fs::remove_dir_all(&vm.temp_dir) {
            debug!(vm_id = %vm.id, "Failed to clean up temp dir: {}", e);
        }
    }
}

#[derive(Debug, Clone)]
pub struct PoolStats {
    pub warm_count: usize,
    pub session_count: usize,
    pub active_count: usize,
    pub max_count: usize,
}
