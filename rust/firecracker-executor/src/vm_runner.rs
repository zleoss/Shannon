// =============================================================================
// 文件: rust/firecracker-executor/src/vm_runner.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   VmRunner：单次执行编排——申请 VM、同步工作区、经 vsock 执行、释放并 post-sync。
// 【关键内容】
//   ExecuteError（PoolExhausted/VmBootFailed/ExecutionFailed）（vm_runner.rs:16）
//   VmRunner 结构体（:25）
//   translate_workspace_path（:39）
//   execute（:66）/ release+post-sync（:90-120）/ execute_inner（:176）
// 【协作关系】
//   被 main.rs /execute handler 调用执行单次请求。
//   依赖 vm_pool 取机、vsock_client 通信、workspace_sync 同步目录。
// =============================================================================
use crate::config::Settings;
use crate::models::{ExecuteRequest, ExecuteResponse, GuestRequest};
use crate::vm_pool::{VmInstance, VmPool};
use crate::vsock_client::execute_guest_via_uds;
use crate::workspace_sync;
use std::sync::Arc;
use std::time::Instant;
use tracing::{debug, warn};

/// Known EKS mount point prefix - when paths start with this, we need to translate
/// them to use the local FIRECRACKER_EFS_MOUNT which includes the PVC subdirectory.
const EKS_MOUNT_PREFIX: &str = "/mnt/shannon-sessions/";

/// Error types to distinguish between transient and permanent failures
#[derive(Debug)]
pub enum ExecuteError {
    /// Pool exhausted - client should retry later or fall back
    PoolExhausted(String),
    /// VM boot failed - transient, can retry
    VmBootFailed(String),
    /// Execution failed - not transient
    ExecutionFailed(String),
}

pub struct VmRunner {
    settings: Settings,
    pool: Arc<VmPool>,
}

impl VmRunner {
    pub fn new(settings: Settings, pool: Arc<VmPool>) -> Self {
        Self { settings, pool }
    }

    /// Translate workspace path from EKS format to EC2 format.
    /// EKS pods mount EFS via Access Point at /mnt/shannon-sessions which maps to
    /// a PVC subdirectory. EC2 mounts the EFS root at /mnt/shannon-sessions.
    /// This translates /mnt/shannon-sessions/{session} → {efs_mount_point}/{session}
    fn translate_workspace_path(&self, path: &str) -> String {
        let efs_mount = self.settings.efs_mount_point.to_string_lossy();
        let efs_mount_str = efs_mount.trim_end_matches('/');

        // Guard: if path already starts with our EFS mount point, don't double-translate
        if path.starts_with(efs_mount_str) {
            return path.to_string();
        }

        // Translate EKS Access Point path to EC2 full path
        if let Some(session_part) = path.strip_prefix(EKS_MOUNT_PREFIX) {
            let translated = format!("{}/{}", efs_mount_str, session_part.trim_start_matches('/'));
            debug!(
                original = %path,
                translated = %translated,
                "Translated EKS workspace path to EC2 path"
            );
            translated
        } else {
            // Path doesn't start with known EKS prefix, use as-is
            path.to_string()
        }
    }

    /// Execute code in a Firecracker VM
    /// Returns (response, is_transient_error)
    /// is_transient_error=true means the caller should return 503
    pub async fn execute(&self, req: ExecuteRequest) -> (ExecuteResponse, bool) {
        let start = Instant::now();
        let session_id = req.session_id.clone();
        let has_workspace = req.workspace_path.is_some();

        // Acquire workspace lock BEFORE any VM operations if workspace involved
        // This prevents concurrent python_executor calls for the same session from corrupting ext4
        let _workspace_guard = if has_workspace {
            if let Some(ref sid) = session_id {
                let lock = self.pool.get_workspace_lock(sid).await;
                Some(lock.lock_owned().await)
            } else {
                None
            }
        } else {
            None
        };

        // Lock held across: execute_inner (acquire + boot + execute) + release + post-sync
        match self.execute_inner(req).await {
            Ok((resp, vm, tainted, workspace_path)) => {
                // For workspace sessions, mark as tainted to force VM termination.
                // This releases the ext4 block device so we can sync files.
                // Session affinity is sacrificed for workspace file consistency.
                let force_terminate = workspace_path.is_some();
                self.pool.release(vm, session_id.clone(), tainted || force_terminate).await;

                // CRITICAL: Post-sync regardless of resp.success
                // Files must persist even if Python execution failed.
                // mark_clean depends on "post-sync succeeded", NOT resp.success.
                if let Some(wp) = workspace_path.as_ref() {
                    // Retry sync with increasing delays - block device may take time to release
                    let mut synced = false;
                    for delay_ms in [100, 200, 500, 1000] {
                        tokio::time::sleep(tokio::time::Duration::from_millis(delay_ms)).await;
                        match workspace_sync::sync_ext4_to_directory(wp) {
                            Ok(()) => {
                                synced = true;
                                break;
                            }
                            Err(e) if e.contains("cannot mount") => {
                                debug!(workspace = %wp, delay_ms, "Sync retry - block device not yet released");
                                continue;
                            }
                            Err(e) => {
                                warn!(workspace = %wp, error = %e, "Failed to post-sync workspace");
                                break;
                            }
                        }
                    }
                    if synced {
                        let _ = workspace_sync::mark_clean(&format!("{}.ext4", wp.trim_end_matches('/')));
                    } else {
                        warn!(workspace = %wp, "Post-sync failed after retries - files may not be visible to file_read");
                    }
                }
                // _workspace_guard drops here, releasing lock

                (
                    ExecuteResponse {
                        duration_ms: Some(start.elapsed().as_millis() as u64),
                        ..resp
                    },
                    false,
                )
            }
            Err(ExecuteError::PoolExhausted(msg)) => {
                // Return 503-worthy response (P1 fix: fallback trigger)
                (
                    ExecuteResponse {
                        success: false,
                        stdout: String::new(),
                        stderr: String::new(),
                        exit_code: 1,
                        error: Some(msg),
                        duration_ms: Some(start.elapsed().as_millis() as u64),
                    },
                    true, // Signal caller to return 503
                )
            }
            Err(ExecuteError::VmBootFailed(msg)) => {
                // Also transient
                (
                    ExecuteResponse {
                        success: false,
                        stdout: String::new(),
                        stderr: String::new(),
                        exit_code: 1,
                        error: Some(msg),
                        duration_ms: Some(start.elapsed().as_millis() as u64),
                    },
                    true,
                )
            }
            Err(ExecuteError::ExecutionFailed(msg)) => {
                (
                    ExecuteResponse {
                        success: false,
                        stdout: String::new(),
                        stderr: String::new(),
                        exit_code: 1,
                        error: Some(msg),
                        duration_ms: Some(start.elapsed().as_millis() as u64),
                    },
                    false,
                )
            }
        }
    }

    async fn execute_inner(
        &self,
        req: ExecuteRequest,
    ) -> Result<(ExecuteResponse, VmInstance, bool, Option<String>), ExecuteError> {
        let session_id = req.session_id.clone();

        // Translate workspace path from EKS format to EC2 format if needed
        let translated_path = req
            .workspace_path
            .as_ref()
            .map(|p| self.translate_workspace_path(p));
        let workspace_path = translated_path.as_deref();

        // Acquire VM with workspace path (P0 fix: EFS wiring)
        let vm = self
            .pool
            .acquire(session_id, workspace_path)
            .await
            .map_err(|e| {
                let msg = e.to_string();
                if msg.contains("pool exhausted") {
                    ExecuteError::PoolExhausted(msg)
                } else {
                    ExecuteError::VmBootFailed(msg)
                }
            })?;

        let timeout_seconds = req
            .timeout_seconds
            .unwrap_or(self.settings.executor_timeout_seconds)
            .min(self.settings.executor_timeout_seconds);

        let guest_req = GuestRequest {
            code: req.code,
            stdin: req.stdin,
            timeout_seconds,
        };

        // Connect via Firecracker's vsock UDS proxy
        let result = execute_guest_via_uds(
            &vm.vsock_uds,
            self.settings.vsock_port,
            guest_req,
            timeout_seconds,
        )
        .await;

        match result {
            Ok(guest_resp) => {
                let tainted = !guest_resp.success;
                // Return workspace_path so caller can sync AFTER VM release
                Ok((
                    ExecuteResponse {
                        success: guest_resp.success,
                        stdout: guest_resp.stdout,
                        stderr: guest_resp.stderr,
                        exit_code: guest_resp.exit_code,
                        error: guest_resp.error,
                        duration_ms: None,
                    },
                    vm,
                    tainted,
                    translated_path,
                ))
            }
            Err(e) => {
                warn!(vm_id = %vm.id, "Guest execution failed: {}", e);
                Ok((
                    ExecuteResponse {
                        success: false,
                        stdout: String::new(),
                        stderr: String::new(),
                        exit_code: 1,
                        error: Some(e.to_string()),
                        duration_ms: None,
                    },
                    vm,
                    true, // Mark as tainted on error
                    translated_path,
                ))
            }
        }
    }
}
