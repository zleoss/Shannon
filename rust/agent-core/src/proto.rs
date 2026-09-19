// =============================================================================
// 文件: rust/agent-core/src/proto.rs
// -----------------------------------------------------------------------------
// 【一句话功能】 包含 tonic 编译生成的 protobuf 代码模块
// 【关键内容】 声明 common / agent / sandbox 三个 proto 模块
//             通过 tonic::include_proto! 宏引入生成代码
// 【协作关系】 被 grpc_server 模块引用；由 build.rs 编译阶段生成
// =============================================================================
// Proto module for including generated code
pub mod common {
    tonic::include_proto!("shannon.common");
}
