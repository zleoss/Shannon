// =============================================================================
// 文件: rust/firecracker-executor/src/models.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   请求/响应 Serialize 结构定义：ExecuteRequest/Response、GuestRequest/Response、Download/List 等。
// 【关键内容】
//   ExecuteRequest（models.rs:4）/ ExecuteResponse（:13）
//   GuestRequest（:23）/ GuestResponse（:30）
//   DownloadRequest（:40）/ ListFilesRequest（:58）等
// 【协作关系】
//   被 main.rs / vm_runner / vsock_client / guest-agent 共享作为通信契约。
//   serde 处理 host↔guest JSON 序列化。
// =============================================================================
use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub struct ExecuteRequest {
    pub code: String,
    pub session_id: Option<String>,
    pub timeout_seconds: Option<u32>,
    pub workspace_path: Option<String>,
    pub stdin: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct ExecuteResponse {
    pub success: bool,
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
    pub error: Option<String>,
    pub duration_ms: Option<u64>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct GuestRequest {
    pub code: String,
    pub stdin: Option<String>,
    pub timeout_seconds: u32,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct GuestResponse {
    pub success: bool,
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
    pub error: Option<String>,
}

/// Request to download a file from session workspace
#[derive(Debug, Deserialize)]
pub struct DownloadRequest {
    pub session_id: String,
    pub file_path: String,
    pub workspace_path: String,
}

/// Response containing file content (base64 for binary safety)
#[derive(Debug, Serialize)]
pub struct DownloadResponse {
    pub success: bool,
    pub content: Option<String>,      // base64-encoded file content
    pub content_type: Option<String>, // MIME type hint
    pub size_bytes: Option<u64>,
    pub error: Option<String>,
}

/// Request to list files in session workspace
#[derive(Debug, Deserialize)]
pub struct ListFilesRequest {
    pub session_id: String,
    pub workspace_path: String,
    pub path: Option<String>, // subdirectory, defaults to /workspace
}

/// File metadata for listing
#[derive(Debug, Serialize, Deserialize)]
pub struct FileInfo {
    pub name: String,
    pub path: String,
    pub is_dir: bool,
    pub size_bytes: u64,
}

/// Response for file listing
#[derive(Debug, Serialize)]
pub struct ListFilesResponse {
    pub success: bool,
    pub files: Vec<FileInfo>,
    pub error: Option<String>,
}

/// Request to clean up a session workspace
#[derive(Debug, Deserialize)]
pub struct CleanupRequest {
    pub session_id: String,
    pub workspace_path: String,
}

/// Response for workspace cleanup
#[derive(Debug, Serialize)]
pub struct CleanupResponse {
    pub deleted_ext4: bool,
    pub deleted_directory: bool,
    pub deleted_state_file: bool,
    pub freed_bytes: u64,
}
