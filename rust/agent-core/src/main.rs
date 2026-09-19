// =============================================================================
// 文件: rust/agent-core/src/main.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   tonic gRPC 服务二进制入口，启动 AgentService + SandboxService + reflection + metrics。
// 【关键内容】
//   tokio::main 启动运行时（main.rs:11）
//   加载 Config（main.rs:22-23）
//   start_metrics_server Prometheus 端口（main.rs:26-30）
//   监听 :50051 并构造 Server builder（main.rs:32, 47-52）
//   AgentServiceImpl::new + SandboxServiceImpl::from_env 装配（main.rs:33-34）
//   gRPC reflection 注册（main.rs:38-50）
// 【协作关系】
//   由 docker-compose / make dev 启动；对外暴露 :50051 给 go/orchestrator 调用。
//   依赖 lib crate shannon_agent_core 暴露的 grpc_server / sandbox_service 模块。
// =============================================================================
use shannon_agent_core::tracing as trace_mod;

use anyhow::Result;
use tonic::transport::Server;
use tracing::info;

use shannon_agent_core::grpc_server::proto::agent::agent_service_server::AgentServiceServer;
use shannon_agent_core::grpc_server::AgentServiceImpl;
use shannon_agent_core::sandbox_service::SandboxServiceImpl;

#[tokio::main]
async fn main() -> Result<()> {
    // Initialize OpenTelemetry tracing
    if let Err(e) = trace_mod::init_tracing() {
        eprintln!("Failed to initialize tracing: {}", e);
        // Fall back to basic logging
    }

    info!("Starting Shannon Agent Core service");

    // Load configuration and get metrics port
    let config = shannon_agent_core::config::Config::global().unwrap_or_default();
    let metrics_port = config.metrics.port;

    // Start metrics server
    tokio::spawn(async move {
        if let Err(e) = shannon_agent_core::metrics::start_metrics_server(metrics_port).await {
            tracing::error!("Failed to start metrics server: {}", e);
        }
    });

    let addr = "0.0.0.0:50051".parse()?;
    let agent_service = AgentServiceImpl::new()?;
    let sandbox_service = SandboxServiceImpl::from_env();
    info!("SandboxService initialized from environment");

    // Build reflection service
    let reflection_service = tonic_reflection::server::Builder::configure()
        .register_encoded_file_descriptor_set(
            shannon_agent_core::grpc_server::proto::FILE_DESCRIPTOR_SET,
        )
        .build_v1()
        .unwrap();

    info!("Agent Core listening on {} with reflection enabled", addr);

    Server::builder()
        .add_service(AgentServiceServer::new(agent_service))
        .add_service(sandbox_service.into_service())
        .add_service(reflection_service)
        .serve(addr)
        .await?;

    Ok(())
}

// Removed legacy metrics port lookup - now using centralized config::Config
