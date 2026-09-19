// =============================================================================
// 文件: rust/agent-core/src/error.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   agent enforcement gateway 的核心错误类型定义（基于 thiserror）。
// 【关键内容】
//   use thiserror::Error（error.rs:1）
//   AgentError 枚举（error.rs:4 起）
//   为各模块提供统一错误向 anyhow::Result 的转换基础
// 【协作关系】
//   被 enforcement / grpc_server / sandbox_service 等模块复用错误类型。
//   可转换为 tonic::Status 用于 gRPC 返回。
// =============================================================================
use thiserror::Error;

/// Core error type for Shannon agent enforcement gateway
#[derive(Error, Debug)]
pub enum AgentError {
    /// Tool execution errors
    #[error("Tool '{name}' execution failed: {reason}")]
    #[allow(dead_code)]
    ToolExecutionFailed { name: String, reason: String },

    /// LLM service errors
    #[error("Failed to parse LLM response: {0}")]
    LlmResponseParseError(String),

    /// Configuration errors
    #[error("Configuration error: {0}")]
    ConfigurationError(String),

    /// Network/HTTP errors
    #[error("Network request failed: {0}")]
    NetworkError(String),

    #[error("HTTP error {status}: {message}")]
    HttpError { status: u16, message: String },

    /// Concurrency errors
    #[error("Mutex poisoned: {0}")]
    MutexPoisoned(String),

    #[error("Task timeout after {seconds} seconds")]
    TaskTimeout { seconds: u64 },

    /// Generic errors for compatibility
    #[error("Internal error: {0}")]
    InternalError(String),

    #[error(transparent)]
    Other(#[from] anyhow::Error),
}

/// Result type alias for agent operations
pub type AgentResult<T> = Result<T, AgentError>;

// Conversion implementations for common error types
impl From<std::io::Error> for AgentError {
    fn from(err: std::io::Error) -> Self {
        AgentError::InternalError(err.to_string())
    }
}

impl From<serde_json::Error> for AgentError {
    fn from(err: serde_json::Error) -> Self {
        AgentError::LlmResponseParseError(err.to_string())
    }
}

impl From<reqwest::Error> for AgentError {
    fn from(err: reqwest::Error) -> Self {
        if err.is_timeout() {
            AgentError::TaskTimeout { seconds: 30 } // Default timeout
        } else if err.is_connect() {
            AgentError::NetworkError(format!("Connection failed: {}", err))
        } else if let Some(status) = err.status() {
            AgentError::HttpError {
                status: status.as_u16(),
                message: err.to_string(),
            }
        } else {
            AgentError::NetworkError(err.to_string())
        }
    }
}

impl<T> From<std::sync::PoisonError<T>> for AgentError {
    fn from(err: std::sync::PoisonError<T>) -> Self {
        AgentError::MutexPoisoned(err.to_string())
    }
}

// Helper functions for creating common errors
impl AgentError {
    /// Create a tool execution error with context
    #[allow(dead_code)]
    pub fn tool_failed(name: impl Into<String>, reason: impl Into<String>) -> Self {
        AgentError::ToolExecutionFailed {
            name: name.into(),
            reason: reason.into(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_error_display() {
        let err = AgentError::tool_failed("calculator", "division by zero");
        assert_eq!(
            err.to_string(),
            "Tool 'calculator' execution failed: division by zero"
        );
    }

    #[test]
    fn test_network_errors() {
        let err = AgentError::NetworkError("timeout".to_string());
        assert_eq!(err.to_string(), "Network request failed: timeout");

        let err = AgentError::HttpError {
            status: 503,
            message: "Service Unavailable".to_string(),
        };
        assert_eq!(err.to_string(), "HTTP error 503: Service Unavailable");
    }
}
