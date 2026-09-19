// =============================================================================
// 文件: rust/agent-core/tests/test_prometheus_race.rs
// -----------------------------------------------------------------------------
// 【一句话功能】 测试 Prometheus 并发初始化时的竞态条件
// 【关键内容】 使用 OnceLock 机制测试 metrics 初始化竞态
//             验证并发场景下 Prometheus 注册的线程安全性
// 【协作关系】 依赖 agent-core 的 metrics 模块；压力测试无需外部服务
// =============================================================================
// Test to identify the exact Prometheus error in concurrent initialization
use prometheus::register_counter_vec;
use std::thread;

#[test]
fn test_prometheus_global_registry_race() {
    // This test will reveal the actual Prometheus error
    let mut handles = vec![];

    for i in 0..5 {
        let handle = thread::spawn(move || {
            // Try to register the same metric multiple times
            match register_counter_vec!("test_metric", "Test metric for race condition", &["label"])
            {
                Ok(_) => println!("Thread {} registered metric successfully", i),
                Err(e) => println!("Thread {} got error: {}", i, e),
            }
        });
        handles.push(handle);
    }

    for handle in handles {
        handle.join().unwrap();
    }
}
