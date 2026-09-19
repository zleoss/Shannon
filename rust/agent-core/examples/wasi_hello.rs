// =============================================================================
// 文件: rust/agent-core/examples/wasi_hello.rs
// -----------------------------------------------------------------------------
// 【一句话功能】 WASI 沙箱的 Hello World 示例程序
// 【关键内容】 演示在 WASI 沙箱中执行 Rust 代码的基本流程
//             使用 cfg(feature = "wasi") 条件编译
// 【协作关系】 被开发者用作 WASI 沙箱集成的参考示例
// =============================================================================
#[cfg(feature = "wasi")]
use std::env;
#[cfg(feature = "wasi")]
use std::fs;

#[cfg(not(feature = "wasi"))]
fn main() {
    eprintln!("This example requires the 'wasi' feature to be enabled");
    eprintln!("Run with: cargo run --example wasi_hello --features wasi -- <wasm_path>");
    std::process::exit(1);
}

#[cfg(feature = "wasi")]
#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args: Vec<String> = env::args().collect();
    if args.len() < 2 {
        eprintln!("Usage: cargo run --example wasi_hello -- <wasm_path> [stdin]");
        std::process::exit(2);
    }
    let wasm_path = &args[1];
    let stdin = if args.len() > 2 { &args[2] } else { "" };

    let bytes = fs::read(wasm_path)?;
    let sandbox = shannon_agent_core::wasi_sandbox::WasiSandbox::new()?;
    let out = sandbox.execute_wasm(&bytes, stdin).await?;
    print!("{}", out);
    Ok(())
}
