// =============================================================================
// 文件: go/orchestrator/internal/workflows/control/signals.go
// =============================================================================
//  工作流控制信号 —— 定义所有支持的外部控制操作。
//
//  本文件是 "控制平面" 的协议定义层，负责声明：
//    1. 信号名称常量（Temporal Signal 的路由 key）
//    2. 信号载荷结构体（传递额外参数）
//    3. 工作流状态结构体（对外暴露的查询结果）
//
//  设计原则：
//    - 信号名称使用版本化后缀（_v1），支持 future 的不兼容变更
//    - 所有请求都有 Reason + RequestedBy，实现操作审计
//    - 状态结构体用于 QueryHandler，让外部随时查看当前工作流状态
//
//  协作关系：
//    handler.go —— 消费这些信号并更新 WorkflowControlState
//    gateway    —— 通过 Temporal Client API 发送这些信号
//    前端       —— 用户点击暂停/恢复/取消按钮触发
// =============================================================================
package control

import "time"

// ---------------------------------------------------------------------------
//  信号名称常量
// ---------------------------------------------------------------------------

// 这些常量是 Temporal Signal 的信道名称，通过 SignalWithStartWorkflow
// 或 SignalWorkflow 发送给运行中的工作流实例。
//
// 命名惯例：
//   - 使用 _v1 后缀，未来通过 workflow.GetVersion() 支持不兼容变更
//   - 全小写 + 下划线，匹配 Temporal 社区常见命名风格
const (
	SignalPause       = "pause_v1"        // 暂停工作流
	SignalResume      = "resume_v1"       // 恢复已暂停的工作流
	SignalCancel      = "cancel_v1"       // 取消工作流（优雅取消）
	QueryControlState = "control_state_v1" // 查询当前控制状态（非信号，是 Query）
)

// ---------------------------------------------------------------------------
//  信号载荷类型
// ---------------------------------------------------------------------------

// PauseRequest 暂停工作流的请求载荷。
//
// 字段说明：
//   Reason      —— 暂停原因（如 "等待人工审核"、"预算不足"），将展示给最终用户
//   RequestedBy —— 请求者标识（如用户名、系统组件名），用于审计日志
//
// 发送途径：
//   gateway REST API → Temporal Client.SignalWorkflow(ctx, workflowID, "pause_v1", req)
type PauseRequest struct {
	Reason      string `json:"reason"`
	RequestedBy string `json:"requested_by"`
}

// ResumeRequest 恢复暂停工作流的请求载荷。
type ResumeRequest struct {
	Reason      string `json:"reason"`
	RequestedBy string `json:"requested_by"`
}

// CancelRequest 优雅取消工作流的请求载荷。
//
// 与 Temporal 原生取消的区别：
//   Temporal 自带 workflow.Cancel() 机制，但那是强制性的。
//   这里使用 Signal 实现"优雅取消"：工作流在自己的检查点响应信号，
//   可以执行清理操作（如记录日志、释放资源）后再退出。
type CancelRequest struct {
	Reason      string `json:"reason"`
	RequestedBy string `json:"requested_by"`
}

// WorkflowControlState 工作流控制状态的对外快照。
//
// 用途：
//   通过 Temporal QueryHandler 暴露给外部调用方（如 gateway API），
//   让客户端在不修改工作流状态的前提下查询当前控制状态。
//
// 典型使用场景：
//   1. 前端定时轮询此状态，更新暂停/取消按钮的显示
//   2. 运维系统查询运行中的工作流是否被暂停
//   3. 自动化系统在发送取消信号前检查当前状态
//
// 状态机：
//   正常 (IsPaused=false, IsCancelled=false)
//     │
//     ├── 收到 pause 信号 ──▶ 暂停 (IsPaused=true)
//     │                          │
//     │                          ├── 收到 resume 信号 ──▶ 正常
//     │                          └── 收到 cancel 信号 ──▶ 已取消 (IsCancelled=true)
//     │
//     └── 收到 cancel 信号 ──▶ 已取消 (IsCancelled=true)
type WorkflowControlState struct {
	IsPaused     bool      `json:"is_paused"`               // 是否处于暂停状态
	IsCancelled  bool      `json:"is_cancelled"`            // 是否已被取消
	PausedAt     time.Time `json:"paused_at,omitempty"`     // 暂停时间戳
	PauseReason  string    `json:"pause_reason,omitempty"`  // 暂停原因
	PausedBy     string    `json:"paused_by,omitempty"`     // 暂停操作人
	CancelReason string    `json:"cancel_reason,omitempty"` // 取消原因
	CancelledBy  string    `json:"cancelled_by,omitempty"`  // 取消操作人
}
