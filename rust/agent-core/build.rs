// =============================================================================
// 文件: rust/agent-core/build.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   tonic-build 编译 common/agent/sandbox 三个 .proto，并产出 shannon_descriptor.bin 供 gRPC reflection。
// 【关键内容】
//   fn main() 入口（build.rs:4）
//   确保可用 protoc（内置 vendored fallback）
//   编译三个 proto 并生成 descriptor.bin 用于反射
// 【协作关系】
//   由 cargo build 阶段执行，生成代码供 lib crate 与 main.rs 使用。
//   产物被 grpc_server 反射注册使用。
// =============================================================================
use std::io::Result;
use std::path::Path;

fn main() -> Result<()> {
    // Ensure a usable `protoc` is available (vendored fallback)
    if std::env::var_os("PROTOC").is_none() {
        if let Ok(pb) = protoc_bin_vendored::protoc_bin_path() {
            std::env::set_var("PROTOC", pb);
        }
    }
    // Determine proto path - check if we're in Docker or local
    let proto_path = if Path::new("/protos").exists() {
        // Docker environment
        "/protos"
    } else {
        // Local development
        "../../protos"
    };

    let common_proto = format!("{}/common/common.proto", proto_path);
    let agent_proto = format!("{}/agent/agent.proto", proto_path);
    let sandbox_proto = format!("{}/sandbox/sandbox.proto", proto_path);

    // Compile protobuf files with reflection support
    tonic_build::configure()
        .build_server(true)
        .build_client(true)
        .file_descriptor_set_path(std::env::var("OUT_DIR").unwrap() + "/shannon_descriptor.bin")
        .compile_protos(
            &[&common_proto, &agent_proto, &sandbox_proto],
            &[proto_path],
        )?;
    Ok(())
}
