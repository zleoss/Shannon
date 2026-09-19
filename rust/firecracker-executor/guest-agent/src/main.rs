// =============================================================================
// 文件: rust/firecracker-executor/guest-agent/src/main.rs
// -----------------------------------------------------------------------------
// 【一句话功能】
//   guest-agent vsock server 入口：监听 VMADDR_CID_ANY:5005，执行 Python、多线程 drain IO、超时 kill、写 ext4。
// 【关键内容】
//   //! 文档：vsock 5005 JSON 协议（main.rs:1-5）
//   listener VMADDR_CID_ANY:5005（:242）
//   execute_python（:58）
//   多线程 drain stdout/stderr（:101-120）
//   超时 kill 子进程（:136-155）
//   sync 写入 ext4（:207-209）
// 【协作关系】
//   在 Firecracker 微虚拟机 guest 内运行，被 host 端 vsock_client 经 vsock UDS 桥接调用。
//   通过 models::GuestRequest/GuestResponse 与 host 交换 JSON。
// =============================================================================
//! Guest agent for Firecracker microVMs.
//!
//! Listens on vsock port 5005 for JSON requests to execute Python code.
//! Protocol:
//! - Host sends: {"code": "...", "stdin": null, "timeout_seconds": 30}
//! - Guest responds: {"success": true, "stdout": "...", "stderr": "...", "exit_code": 0, "error": null}

use serde::{Deserialize, Serialize};
use std::io::{BufRead, BufReader, Read, Write};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::Duration;
use vsock::VsockListener;

const VSOCK_PORT: u32 = 5005;
const DEFAULT_TIMEOUT_SECONDS: u32 = 30;

#[derive(Debug, Deserialize)]
struct GuestRequest {
    code: String,
    stdin: Option<String>,
    timeout_seconds: u32,
}

#[derive(Debug, Serialize)]
struct GuestResponse {
    success: bool,
    stdout: String,
    stderr: String,
    exit_code: i32,
    error: Option<String>,
}

impl GuestResponse {
    fn error(msg: impl Into<String>) -> Self {
        Self {
            success: false,
            stdout: String::new(),
            stderr: String::new(),
            exit_code: -1,
            error: Some(msg.into()),
        }
    }

    fn from_output(stdout: String, stderr: String, exit_code: i32) -> Self {
        Self {
            success: exit_code == 0,
            stdout,
            stderr,
            exit_code,
            error: None,
        }
    }
}

fn execute_python(req: &GuestRequest) -> GuestResponse {
    let timeout = if req.timeout_seconds > 0 {
        req.timeout_seconds
    } else {
        DEFAULT_TIMEOUT_SECONDS
    };

    // Try multiple Python paths - in minimal init environments PATH may not be set
    let python_paths = [
        "/usr/local/bin/python3",
        "/usr/bin/python3",
        "python3",
    ];

    let python_path = python_paths.iter()
        .find(|p| std::path::Path::new(p).exists() || *p == &"python3")
        .unwrap_or(&"python3");

    let mut cmd = Command::new(python_path);
    cmd.arg("-c").arg(&req.code);
    cmd.stdin(if req.stdin.is_some() {
        Stdio::piped()
    } else {
        Stdio::null()
    });
    cmd.stdout(Stdio::piped());
    cmd.stderr(Stdio::piped());
    cmd.current_dir("/workspace");

    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => return GuestResponse::error(format!("failed to spawn python: {}", e)),
    };

    // Write stdin if provided
    if let Some(ref input) = req.stdin {
        if let Some(ref mut stdin) = child.stdin.take() {
            if let Err(e) = stdin.write_all(input.as_bytes()) {
                eprintln!("warning: failed to write stdin: {}", e);
            }
        }
    }

    // P1 fix: Drain stdout/stderr in separate threads to prevent deadlock
    // when output exceeds pipe buffer size (~64KB on Linux)
    let stdout_handle = child.stdout.take();
    let stderr_handle = child.stderr.take();

    let stdout_thread = thread::spawn(move || {
        let mut output = String::new();
        if let Some(mut handle) = stdout_handle {
            let _ = handle.read_to_string(&mut output);
        }
        output
    });

    let stderr_thread = thread::spawn(move || {
        let mut output = String::new();
        if let Some(mut handle) = stderr_handle {
            let _ = handle.read_to_string(&mut output);
        }
        output
    });

    let timeout_duration = Duration::from_secs(timeout as u64);
    let start = std::time::Instant::now();

    // Wait for process with timeout
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                // Process finished - collect output from threads
                let stdout = stdout_thread.join().unwrap_or_default();
                let stderr = stderr_thread.join().unwrap_or_default();
                let exit_code = status.code().unwrap_or(-1);
                return GuestResponse::from_output(stdout, stderr, exit_code);
            }
            Ok(None) => {
                if start.elapsed() >= timeout_duration {
                    // Timeout - kill process
                    let _ = child.kill();
                    let _ = child.wait(); // Reap zombie

                    // Collect whatever output we have
                    let stdout = stdout_thread.join().unwrap_or_default();
                    let stderr = stderr_thread.join().unwrap_or_default();

                    return GuestResponse {
                        success: false,
                        stdout,
                        stderr,
                        exit_code: -1,
                        error: Some(format!(
                            "execution timed out after {} seconds",
                            timeout
                        )),
                    };
                }
                thread::sleep(Duration::from_millis(10));
            }
            Err(e) => {
                return GuestResponse::error(format!("failed to wait for process: {}", e));
            }
        }
    }
}

fn handle_connection(stream: vsock::VsockStream) {
    let peer = stream.peer_addr().ok();
    eprintln!("connection from {:?}", peer);

    let mut reader = BufReader::new(&stream);
    let mut writer = &stream;

    let mut line = String::new();
    match reader.read_line(&mut line) {
        Ok(0) => {
            eprintln!("client disconnected without sending request");
            return;
        }
        Ok(_) => {}
        Err(e) => {
            eprintln!("failed to read request: {}", e);
            let resp = GuestResponse::error(format!("read error: {}", e));
            let _ = serde_json::to_writer(&mut writer, &resp);
            let _ = writer.write_all(b"\n");
            return;
        }
    }

    let req: GuestRequest = match serde_json::from_str(line.trim()) {
        Ok(r) => r,
        Err(e) => {
            eprintln!("invalid request JSON: {}", e);
            let resp = GuestResponse::error(format!("invalid JSON: {}", e));
            let _ = serde_json::to_writer(&mut writer, &resp);
            let _ = writer.write_all(b"\n");
            return;
        }
    };

    eprintln!(
        "executing python code ({} bytes, timeout={}s)",
        req.code.len(),
        req.timeout_seconds
    );

    let resp = execute_python(&req);

    // Sync /workspace to ensure writes are flushed to ext4 block device
    // before the VM is terminated. Without this, buffered writes may be lost.
    let _ = Command::new("sync").output();

    if let Err(e) = serde_json::to_writer(&mut writer, &resp) {
        eprintln!("failed to write response: {}", e);
        return;
    }
    if let Err(e) = writer.write_all(b"\n") {
        eprintln!("failed to flush response: {}", e);
        return;
    }

    eprintln!(
        "execution complete: success={}, exit_code={}",
        resp.success, resp.exit_code
    );
}

fn main() {
    eprintln!("guest-agent starting on vsock port {}", VSOCK_PORT);

    let shutdown = Arc::new(AtomicBool::new(false));

    // Setup SIGTERM handler for graceful shutdown
    {
        let shutdown = shutdown.clone();
        if let Err(e) = ctrlc_handler(move || {
            eprintln!("received shutdown signal");
            shutdown.store(true, Ordering::SeqCst);
        }) {
            eprintln!("warning: failed to setup signal handler: {}", e);
        }
    }

    let listener = match VsockListener::bind_with_cid_port(vsock::VMADDR_CID_ANY, VSOCK_PORT) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("failed to bind vsock listener: {}", e);
            std::process::exit(1);
        }
    };

    // Set non-blocking for graceful shutdown checks
    if let Err(e) = listener.set_nonblocking(true) {
        eprintln!("warning: failed to set non-blocking: {}", e);
    }

    eprintln!("listening for connections...");

    while !shutdown.load(Ordering::SeqCst) {
        match listener.accept() {
            Ok((stream, _addr)) => {
                // Set blocking for actual I/O
                let _ = stream.set_nonblocking(false);
                handle_connection(stream);
            }
            Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                thread::sleep(Duration::from_millis(100));
            }
            Err(e) => {
                eprintln!("accept error: {}", e);
                thread::sleep(Duration::from_millis(100));
            }
        }
    }

    eprintln!("guest-agent shutting down");
}

fn ctrlc_handler<F>(handler: F) -> Result<(), String>
where
    F: FnOnce() + Send + 'static,
{
    use nix::sys::signal::{sigaction, SaFlags, SigAction, SigHandler, SigSet, Signal};
    use std::sync::Mutex;

    static HANDLER: Mutex<Option<Box<dyn FnOnce() + Send>>> = Mutex::new(None);

    extern "C" fn signal_handler(_: i32) {
        if let Ok(mut guard) = HANDLER.lock() {
            if let Some(h) = guard.take() {
                h();
            }
        }
    }

    *HANDLER.lock().map_err(|e| e.to_string())? = Some(Box::new(handler));

    let sig_action = SigAction::new(
        SigHandler::Handler(signal_handler),
        SaFlags::empty(),
        SigSet::empty(),
    );

    unsafe {
        sigaction(Signal::SIGTERM, &sig_action).map_err(|e| e.to_string())?;
        sigaction(Signal::SIGINT, &sig_action).map_err(|e| e.to_string())?;
    }

    Ok(())
}
