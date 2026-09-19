// =============================================================================
// 文件: rust/firecracker-executor/src/workspace_sync.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   ext4 ↔ 目录 rsync 双向同步、e2fsck 一致性校验、dirty/clean 状态文件管理。
// 【关键内容】
//   基于 std::process::Command 调用 rsync（workspace_sync.rs:1-4）
//   Serialize/Deserialize 状态文件（dirty/clean）记录同步状态
//   e2fsck 一致性校验
// 【协作关系】
//   被 vm_pool 在 VM acquire/release 时调用维护工作区一致性。
//   与 vm_runner 协作在执行前后做双向同步。
// =============================================================================
use chrono::Utc;
use serde::{Deserialize, Serialize};
use std::path::Path;
use std::process::Command;
use tracing::{debug, info, warn};
use uuid::Uuid;

/// Workspace state file for crash recovery.
/// Written before VM start (dirty) and after successful post-sync (clean).
#[derive(Serialize, Deserialize)]
struct WorkspaceState {
    status: String,   // "clean" or "dirty"
    last_sync: String, // RFC 3339 timestamp
}

/// Check ext4 consistency. Run fsck if state is missing or dirty.
/// Call this BEFORE writing dirty marker.
/// Returns Ok(true) if ext4 is consistent, Ok(false) if fsck found errors.
pub fn ensure_ext4_consistent(ext4_path: &str) -> Result<bool, String> {
    let state_path = format!("{}.state", ext4_path);

    let needs_fsck = match std::fs::read_to_string(&state_path) {
        Ok(content) => {
            match serde_json::from_str::<WorkspaceState>(&content) {
                Ok(state) => state.status != "clean",
                Err(_) => true, // Corrupted state file
            }
        }
        Err(_) => true, // No state file = first use or crash
    };

    if needs_fsck && Path::new(ext4_path).exists() {
        info!(ext4_path = %ext4_path, "Running e2fsck (unclean/missing state)");
        let output = Command::new("e2fsck")
            .args(["-p", "-f", ext4_path])
            .output()
            .map_err(|e| format!("Failed to run e2fsck: {}", e))?;
        // e2fsck returns 0=clean, 1=fixed, 2+=error
        let code = output.status.code().unwrap_or(255);
        if code > 1 {
            warn!(ext4_path = %ext4_path, exit_code = code, "e2fsck found uncorrectable errors");
        }
        Ok(code <= 1)
    } else {
        Ok(true)
    }
}

/// Mark workspace as dirty BEFORE VM starts.
pub fn mark_dirty(ext4_path: &str) -> Result<(), String> {
    let state = WorkspaceState {
        status: "dirty".to_string(),
        last_sync: Utc::now().to_rfc3339(),
    };
    let state_path = format!("{}.state", ext4_path);
    let content = serde_json::to_string(&state)
        .map_err(|e| format!("Failed to serialize state: {}", e))?;
    std::fs::write(&state_path, content)
        .map_err(|e| format!("Failed to write state file: {}", e))?;
    debug!(ext4_path = %ext4_path, "Marked workspace as dirty");
    Ok(())
}

/// Mark workspace as clean AFTER successful post-sync.
pub fn mark_clean(ext4_path: &str) -> Result<(), String> {
    let state = WorkspaceState {
        status: "clean".to_string(),
        last_sync: Utc::now().to_rfc3339(),
    };
    let state_path = format!("{}.state", ext4_path);
    let content = serde_json::to_string(&state)
        .map_err(|e| format!("Failed to serialize state: {}", e))?;
    std::fs::write(&state_path, content)
        .map_err(|e| format!("Failed to write state file: {}", e))?;
    debug!(ext4_path = %ext4_path, "Marked workspace as clean");
    Ok(())
}

fn temp_mount_point(prefix: &str) -> Result<String, String> {
    let mount_point = format!("/tmp/shannon-{}-{}", prefix, Uuid::new_v4());
    std::fs::create_dir_all(&mount_point)
        .map_err(|e| format!("Failed to create mount point: {}", e))?;
    Ok(mount_point)
}

fn cleanup_mount(mount_point: &str) {
    let _ = Command::new("umount").arg(mount_point).output();
    let _ = std::fs::remove_dir(mount_point);
}

/// Sync files from session directory to ext4 block device BEFORE a VM starts.
/// This enables the VM (which mounts the ext4 at /workspace) to access files created by tools
/// that write directly to the session directory.
pub(crate) fn sync_directory_to_ext4(workspace_path: &str) -> Result<(), String> {
    let ext4_path = format!("{}.ext4", workspace_path.trim_end_matches('/'));
    let dir_path = workspace_path;

    if !Path::new(dir_path).exists() {
        debug!(dir_path = %dir_path, "No directory to sync from");
        return Ok(());
    }

    if !Path::new(&ext4_path).exists() {
        debug!(ext4_path = %ext4_path, "No ext4 file to sync to");
        return Ok(());
    }

    let has_files = std::fs::read_dir(dir_path)
        .map(|mut d| d.next().is_some())
        .unwrap_or(false);
    if !has_files {
        debug!(dir_path = %dir_path, "Directory is empty, nothing to sync");
        return Ok(());
    }

    let mount_point = temp_mount_point("presync")?;

    let mount_result = Command::new("mount")
        .args(["-o", "loop", &ext4_path, &mount_point])
        .output();

    match mount_result {
        Ok(output) if !output.status.success() => {
            cleanup_mount(&mount_point);
            return Err(format!(
                "Mount failed: {}",
                String::from_utf8_lossy(&output.stderr)
            ));
        }
        Err(e) => {
            cleanup_mount(&mount_point);
            return Err(format!("Mount command failed: {}", e));
        }
        _ => {}
    }

    let rsync_result = Command::new("rsync")
        .args([
            "-a",
            "--checksum",
            "--exclude=lost+found",
            &format!("{}/", dir_path),
            &format!("{}/", mount_point),
        ])
        .output();

    cleanup_mount(&mount_point);

    match rsync_result {
        Ok(output) if output.status.success() => {
            info!(
                dir_path = %dir_path,
                ext4_path = %ext4_path,
                "Pre-synced workspace from directory to ext4"
            );
            Ok(())
        }
        Ok(output) => Err(format!(
            "Rsync failed: {}",
            String::from_utf8_lossy(&output.stderr)
        )),
        Err(e) => Err(format!("Rsync command failed: {}", e)),
    }
}

/// Sync files from ext4 block device to session directory AFTER a VM terminates.
/// This enables tools that read from the session directory to access files created by code
/// executed inside the VM.
pub(crate) fn sync_ext4_to_directory(workspace_path: &str) -> Result<(), String> {
    let ext4_path = format!("{}.ext4", workspace_path.trim_end_matches('/'));
    let dir_path = workspace_path;

    if !Path::new(&ext4_path).exists() {
        debug!(ext4_path = %ext4_path, "No ext4 file to sync");
        return Ok(());
    }

    let mount_point = temp_mount_point("sync")?;

    // Use read-write mount - read-only fails on Amazon Linux 2 with "cannot mount read-only"
    let mount_result = Command::new("mount")
        .args(["-o", "loop", &ext4_path, &mount_point])
        .output();

    match mount_result {
        Ok(output) if !output.status.success() => {
            cleanup_mount(&mount_point);
            return Err(format!(
                "Mount failed: {}",
                String::from_utf8_lossy(&output.stderr)
            ));
        }
        Err(e) => {
            cleanup_mount(&mount_point);
            return Err(format!("Mount command failed: {}", e));
        }
        _ => {}
    }

    if let Err(e) = std::fs::create_dir_all(dir_path) {
        cleanup_mount(&mount_point);
        return Err(format!("Failed to create target directory: {}", e));
    }

    let rsync_result = Command::new("rsync")
        .args([
            "-a",
            "--checksum",
            "--exclude=lost+found",
            &format!("{}/", mount_point),
            &format!("{}/", dir_path),
        ])
        .output();

    cleanup_mount(&mount_point);

    match rsync_result {
        Ok(output) if output.status.success() => {
            info!(
                ext4_path = %ext4_path,
                dir_path = %dir_path,
                "Synced workspace from ext4 to directory"
            );
            Ok(())
        }
        Ok(output) => Err(format!(
            "Rsync failed: {}",
            String::from_utf8_lossy(&output.stderr)
        )),
        Err(e) => Err(format!("Rsync command failed: {}", e)),
    }
}

