// =============================================================================
// 文件: rust/firecracker-executor/guest-agent/src/lib.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   guest-agent 库：定义 GuestRequest/GuestResponse 结构并附单元测试（from_output 解析）。
// 【关键内容】
//   //! Guest agent library for unit testing（lib.rs:1）
//   GuestRequest（:6）/ GuestResponse（:13）
//   from_output 辅助构造（lib.rs:22-40）
// 【协作关系】
//   被 guest-agent main.rs 复用通信结构。
//   提供可独立测试的纯逻辑。
// =============================================================================
//! Guest agent library for unit testing.

use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub struct GuestRequest {
    pub code: String,
    pub stdin: Option<String>,
    pub timeout_seconds: u32,
}

#[derive(Debug, Serialize, PartialEq)]
pub struct GuestResponse {
    pub success: bool,
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
    pub error: Option<String>,
}

impl GuestResponse {
    pub fn error(msg: impl Into<String>) -> Self {
        Self {
            success: false,
            stdout: String::new(),
            stderr: String::new(),
            exit_code: -1,
            error: Some(msg.into()),
        }
    }

    pub fn from_output(stdout: String, stderr: String, exit_code: i32) -> Self {
        Self {
            success: exit_code == 0,
            stdout,
            stderr,
            exit_code,
            error: None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_guest_request_deserialize() {
        let json = r#"{"code": "print(1+1)", "stdin": null, "timeout_seconds": 30}"#;
        let req: GuestRequest = serde_json::from_str(json).unwrap();
        assert_eq!(req.code, "print(1+1)");
        assert_eq!(req.stdin, None);
        assert_eq!(req.timeout_seconds, 30);
    }

    #[test]
    fn test_guest_request_with_stdin() {
        let json = r#"{"code": "import sys; print(sys.stdin.read())", "stdin": "hello", "timeout_seconds": 10}"#;
        let req: GuestRequest = serde_json::from_str(json).unwrap();
        assert_eq!(req.stdin, Some("hello".to_string()));
    }

    #[test]
    fn test_guest_response_success() {
        let resp = GuestResponse::from_output("2\n".to_string(), String::new(), 0);
        assert!(resp.success);
        assert_eq!(resp.exit_code, 0);
        assert_eq!(resp.stdout, "2\n");
        assert!(resp.error.is_none());
    }

    #[test]
    fn test_guest_response_failure() {
        let resp = GuestResponse::from_output(String::new(), "error".to_string(), 1);
        assert!(!resp.success);
        assert_eq!(resp.exit_code, 1);
    }

    #[test]
    fn test_guest_response_error() {
        let resp = GuestResponse::error("timeout");
        assert!(!resp.success);
        assert_eq!(resp.exit_code, -1);
        assert_eq!(resp.error, Some("timeout".to_string()));
    }

    #[test]
    fn test_guest_response_serialize() {
        let resp = GuestResponse::from_output("hello".to_string(), String::new(), 0);
        let json = serde_json::to_string(&resp).unwrap();
        assert!(json.contains("\"success\":true"));
        assert!(json.contains("\"stdout\":\"hello\""));
        assert!(json.contains("\"exit_code\":0"));
    }
}
