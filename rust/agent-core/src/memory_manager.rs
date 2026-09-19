// =============================================================================
// 文件: rust/agent-core/src/memory_manager.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   MemoryManager：按 user_id 隔离的用户级持久记忆目录，跨会话保留。
// 【关键内容】
//   //! 模块文档：每 user 一个 /memory 目录，持久化跨会话存储（memory_manager.rs:1-4）
//   按 user_id 创建/解析记忆目录路径
// 【协作关系】
//   被 sandbox_service、wasi_sandbox 调用以挂载用户记忆目录到 /memory。
//   与 workspace::WorkspaceManager 配合提供会话级 + 用户级双目录。
// =============================================================================
//! User memory directory management for persistent cross-session storage.
//!
//! Each user gets an isolated memory directory that persists across sessions.
//! This can be mounted into WASI sandboxes at `/memory` with read-write permissions.

use anyhow::{anyhow, Result};
use std::path::{Path, PathBuf};
use tracing::{debug, info};

/// Manages per-user memory directories.
pub struct MemoryManager {
    base_dir: PathBuf,
}

impl MemoryManager {
    /// Create a new memory manager with the given base directory.
    pub fn new(base_dir: PathBuf) -> Self {
        Self { base_dir }
    }

    /// Create from environment variable or default.
    pub fn from_env() -> Self {
        let base = std::env::var("SHANNON_USER_MEMORY_DIR")
            .unwrap_or_else(|_| "/tmp/shannon-users".to_string());
        Self::new(PathBuf::from(base))
    }

    /// Get the base directory.
    pub fn base_dir(&self) -> &Path {
        &self.base_dir
    }

    /// Validate user ID format (alphanumeric + hyphen + underscore only).
    fn validate_user_id(user_id: &str) -> Result<()> {
        if user_id.is_empty() {
            return Err(anyhow!("User ID cannot be empty"));
        }
        if user_id.len() > 128 {
            return Err(anyhow!("User ID too long (max 128 chars)"));
        }
        if !user_id
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
        {
            return Err(anyhow!(
                "User ID must contain only alphanumeric, hyphen, or underscore"
            ));
        }
        // Reject path traversal attempts
        if user_id.contains("..") || user_id.starts_with('.') {
            return Err(anyhow!("User ID cannot contain path traversal"));
        }
        Ok(())
    }

    /// Get or create memory directory for a user.
    ///
    /// Returns `{base_dir}/{user_id}/memory/`.
    ///
    /// Security: Validates that the memory path doesn't escape the base directory
    /// by checking the canonical path BEFORE and AFTER creation to prevent TOCTOU attacks.
    pub fn get_memory_dir(&self, user_id: &str) -> Result<PathBuf> {
        Self::validate_user_id(user_id)?;

        // Canonicalize base_dir first to establish trusted root
        let canonical_base = if self.base_dir.exists() {
            self.base_dir.canonicalize()?
        } else {
            std::fs::create_dir_all(&self.base_dir)?;
            self.base_dir.canonicalize()?
        };

        // Construct memory path: {base}/{user_id}/memory/
        let user_dir = canonical_base.join(user_id);
        let memory_dir = user_dir.join("memory");

        // Pre-creation check: Verify the path we're about to create is within base
        if !memory_dir.starts_with(&canonical_base) {
            return Err(anyhow!("Memory path escapes base directory"));
        }

        // Create the full directory hierarchy if needed
        if memory_dir.exists() {
            // Verify it's a directory, not a symlink to outside
            let metadata = std::fs::symlink_metadata(&memory_dir)?;
            if metadata.file_type().is_symlink() {
                return Err(anyhow!("Memory directory is a symlink (potential attack)"));
            }
            if !metadata.is_dir() {
                return Err(anyhow!("Memory path exists but is not a directory"));
            }
        } else {
            // Create user_dir and memory subdirectory
            match std::fs::create_dir_all(&memory_dir) {
                Ok(_) => info!("Created memory directory for user: {}", user_id),
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {
                    // Race condition: another request created it
                    let metadata = std::fs::symlink_metadata(&memory_dir)?;
                    if metadata.file_type().is_symlink() {
                        return Err(anyhow!("Memory directory is a symlink (potential attack)"));
                    }
                    if !metadata.is_dir() {
                        return Err(anyhow!("Memory path exists but is not a directory"));
                    }
                }
                Err(e) => return Err(e.into()),
            }
        }

        // Post-creation verification (defense in depth)
        let canonical = memory_dir.canonicalize()?;
        if !canonical.starts_with(&canonical_base) {
            std::fs::remove_dir_all(&memory_dir).ok(); // Cleanup
            return Err(anyhow!("Memory path escapes base directory after creation"));
        }

        debug!("Memory path for {}: {:?}", user_id, canonical);
        Ok(canonical)
    }

    /// Get size of a user's memory directory in bytes.
    pub fn get_memory_size(&self, user_id: &str) -> Result<u64> {
        let memory_dir = self.get_memory_dir(user_id)?;
        dir_size(&memory_dir)
    }

    /// Check if a path is within a user's memory directory.
    pub fn is_within_memory(&self, user_id: &str, path: &Path) -> Result<bool> {
        let memory_dir = self.get_memory_dir(user_id)?;

        // Handle both existing and non-existing paths
        let check_path = if path.exists() {
            path.canonicalize()?
        } else {
            // For non-existing paths, canonicalize the parent and join
            let parent = path.parent().ok_or_else(|| anyhow!("Invalid path"))?;
            if parent.exists() {
                parent
                    .canonicalize()?
                    .join(path.file_name().unwrap_or_default())
            } else {
                return Ok(false);
            }
        };

        Ok(check_path.starts_with(&memory_dir))
    }
}

/// Maximum number of entries to visit during a size scan to prevent DoS
/// via deeply nested or symlink-looped directory trees.
const MAX_DIR_WALK_ENTRIES: usize = 50_000;

/// Calculate total size of a directory recursively.
///
/// Skips symlinks entirely to avoid escape/infinite-loop risks.
fn dir_size(path: &Path) -> Result<u64> {
    let mut size = 0u64;
    let mut remaining = MAX_DIR_WALK_ENTRIES;
    dir_size_inner(path, &mut size, &mut remaining)?;
    Ok(size)
}

fn dir_size_inner(path: &Path, size: &mut u64, remaining: &mut usize) -> Result<()> {
    if !path.exists() {
        return Ok(());
    }

    for entry in std::fs::read_dir(path)? {
        if *remaining == 0 {
            return Err(anyhow!(
                "Memory scan exceeded {} entries",
                MAX_DIR_WALK_ENTRIES
            ));
        }
        *remaining = remaining.saturating_sub(1);

        let entry = entry?;
        let symlink_meta = std::fs::symlink_metadata(entry.path()).ok();

        // Skip symlinks entirely (prevents escape + infinite loops)
        if symlink_meta.is_some_and(|m| m.file_type().is_symlink()) {
            continue;
        }

        let metadata = entry.metadata().ok();
        if let Some(meta) = metadata {
            if meta.is_file() {
                *size += meta.len();
            } else if meta.is_dir() {
                dir_size_inner(&entry.path(), size, remaining)?;
            }
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn test_memory_dir_creation() {
        let temp = TempDir::new().unwrap();
        let mgr = MemoryManager::new(temp.path().to_path_buf());

        let dir = mgr.get_memory_dir("user-123").unwrap();
        assert!(dir.exists());
        assert!(dir.is_dir());
        assert!(dir.ends_with("memory"));
        // Verify the full path structure: {base}/user-123/memory
        assert!(dir.parent().unwrap().ends_with("user-123"));
    }

    #[test]
    fn test_invalid_user_id_rejected() {
        let temp = TempDir::new().unwrap();
        let mgr = MemoryManager::new(temp.path().to_path_buf());

        assert!(mgr.get_memory_dir("../escape").is_err());
        assert!(mgr.get_memory_dir("user;rm -rf").is_err());
        assert!(mgr.get_memory_dir("").is_err());
        assert!(mgr.get_memory_dir(".hidden").is_err());
    }

    #[test]
    fn test_user_isolation() {
        let temp = TempDir::new().unwrap();
        let mgr = MemoryManager::new(temp.path().to_path_buf());

        let dir_a = mgr.get_memory_dir("user-a").unwrap();
        let dir_b = mgr.get_memory_dir("user-b").unwrap();

        assert_ne!(dir_a, dir_b);
        assert!(dir_a.parent().unwrap().ends_with("user-a"));
        assert!(dir_b.parent().unwrap().ends_with("user-b"));
    }

    #[test]
    fn test_path_within_memory() {
        let temp = TempDir::new().unwrap();
        let mgr = MemoryManager::new(temp.path().to_path_buf());

        // Create memory dir and a file
        let dir = mgr.get_memory_dir("test-user").unwrap();
        let test_file = dir.join("notes.md");
        std::fs::write(&test_file, "hello").unwrap();

        assert!(mgr.is_within_memory("test-user", &test_file).unwrap());
        assert!(!mgr
            .is_within_memory("test-user", Path::new("/etc/passwd"))
            .unwrap());
    }

    #[test]
    fn test_idempotent_creation() {
        let temp = TempDir::new().unwrap();
        let mgr = MemoryManager::new(temp.path().to_path_buf());

        let dir1 = mgr.get_memory_dir("user-x").unwrap();
        let dir2 = mgr.get_memory_dir("user-x").unwrap();
        assert_eq!(dir1, dir2);
    }

    #[test]
    fn test_from_env_default() {
        // Without env var set, should use default
        std::env::remove_var("SHANNON_USER_MEMORY_DIR");
        let mgr = MemoryManager::from_env();
        assert_eq!(mgr.base_dir(), Path::new("/tmp/shannon-users"));
    }
}
