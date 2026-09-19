// =============================================================================
// 文件: rust/agent-core/src/lib.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   agent-core 库 crate 根，通过 pub mod 聚合各子模块，wasi feature 门控沙箱实现。
// 【关键内容】
//   #![allow(dead_code)] 与 clippy::enum_variant_names 全局放宽（lib.rs:1-2）
//   pub mod 声明 config / enforcement / grpc_server / sandbox_service 等模块
//   wasi feature 门控 sandbox / wasi_sandbox 模块（lib.rs:15-16, 24-25）
// 【协作关系】
//   作为 crate 入口被 main.rs 与外部依赖引用。
//   feature wasi 决定是否编译 WASI/wasmtime 沙箱路径。
// =============================================================================
#![allow(dead_code)]
#![allow(clippy::enum_variant_names)]

pub mod config;
pub mod enforcement;
pub mod error;
pub mod firecracker_client;
pub mod grpc_server;
pub mod llm_client;
pub mod memory;
pub mod memory_manager;
pub mod metrics;
pub mod proto;
pub mod safe_commands;
#[cfg(feature = "wasi")]
pub mod sandbox;
pub mod sandbox_service;
pub mod tool_cache;
pub mod tool_registry;
pub mod tools;
pub mod tracing;
pub mod workspace;

#[cfg(feature = "wasi")]
pub mod wasi_sandbox;
