// =============================================================================
// 文件: go/orchestrator/internal/workflows/control/handler.go
// =============================================================================
//  工作流控制信号处理器 —— 在工作流内部监听 pause/resume/cancel 信号，
//  并提供检查点机制让工作流在安全位置响应控制操作。
//
//  核心能力：
//    1. 信号监听：后台 goroutine 监听 Temporal Signal，更新 WorkflowControlState
//    2. 检查点：业务代码在关键节点调用 CheckPausePoint()，在安全位置暂停/取消
//    3. 级联传播：暂停/取消信号自动传播到所有子工作流
//    4. 事件推送：状态变更时通过 SSE 推送通知给前端
//
//  设计背景：
//    Temporal 工作流是长期运行的（可能数小时甚至数天），需要支持外部人工干预。
//    管理员可能需要在工作流执行过程中暂停它（等待人工审批）、恢复它或取消它。
//    这些操作以 Temporal Signal 的形式异步发送给工作流实例。
//
//  使用模式：
//    // 在 workflow 初始化中设置
//    handler := &SignalHandler{WorkflowID: id, AgentID: agent, Logger: logger, EmitCtx: ctx}
//    handler.Setup(ctx)
//
//    // 在关键操作前后插入检查点
//    if err := handler.CheckPausePoint(ctx, "before_tool_call"); err != nil {
//        return err  // 被取消时返回 CanceledError
//    }
//
//    // 在返回前确保取消事件已发送
//    handler.EmitCancelledIfNeeded(ctx, "workflow done")
//
//  协作关系：
//    signals.go     —— 信号名称常量、请求类型、状态结构体定义
//    各策略 workflow —— 注入 SignalHandler 并在关键节点调用 CheckPausePoint
//    gateway API    —— 外部发送控制信号
//    streaming      —— 通过 SSE 推送状态变更事件给前端
// =============================================================================
package control

import (
	"fmt"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/activities"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SignalHandler 管理任意工作流的 pause/resume/cancel 信号生命周期。
//
// 每个使用控制信号的工作流应当创建一个 SignalHandler 实例，
// 在 Setup 中注册信号监听器，然后在关键节点调用 CheckPausePoint。
//
// 内部架构：
//   ┌──────────────────────────────────────────┐
//   │              SignalHandler               │
//   │  ┌────────────────────────────────────┐  │
//   │  │      Setup() 注册信号监听器        │  │
//   │  │  ┌──────────────────┐             │  │
//   │  │  │ workflow.Go()    │ 后台协程     │  │
//   │  │  │ ┌─ pauseCh ────▶ │handlePause │  │
//   │  │  │ ├─ resumeCh ───▶│handleResume│  │
//   │  │  │ └─ cancelCh ───▶│handleCancel│  │
//   │  │  └──────────────────┘             │  │
//   │  └────────────────────────────────────┘  │
//   │                                          │
//   │  ┌────────────────────────────────────┐  │
//   │  │  CheckPausePoint() 检查点          │  │
//   │  │  1. 检查 IsCancelled → 返回错误    │  │
//   │  │  2. 检查 IsPaused → 阻塞直到恢复   │  │
//   │  └────────────────────────────────────┘  │
//   │                                          │
//   │  ┌────────────────────────────────────┐  │
//   │  │  子工作流级联传播                   │  │
//   │  │  RegisterChildWorkflow             │  │
//   │  │  propagateSignalToChildren         │  │
//   │  └────────────────────────────────────┘  │
//   └──────────────────────────────────────────┘
//
// 关键设计决策：
//   1. 使用 workflow.Go 启动后台协程监听信号 ——
//      因为 Temporal 工作流是单线程协作式调度，Signal 不会中断正在执行的代码，
//      必须通过 Selector 主动轮询。后台协程确保信号随时被接收。
//   2. CheckPausePoint 中用 workflow.Sleep(ctx, 0) 让出执行权 ——
//      确保在检查状态前，待处理的 Signal 已经被 Selector 消费并更新 State。
//   3. workflow.Await 实现阻塞等待 ——
//      不会产生轮询开销（不像 for + Sleep 循环），只会在 Workflow 历史中产生
//      一条 Marker 记录。条件满足时 Temporal 自动唤醒工作流。
//   4. 子工作流级联传播 ——
//      暂停/取消父工作流时，自动向所有已注册的子工作流发送相同的信号。
//
// 线程安全：
//   Temporal 工作流运行在单一 goroutine 中（协作式调度），
//   所以 childWorkflowIDs 的读写不需要额外的同步原语。
type SignalHandler struct {
	// State —— 工作流控制状态的运行时指针，被 Setup 初始化和后台协程更新
	State *WorkflowControlState

	// WorkflowID —— 当前工作流的 ID，用于事件推送和信号传播
	WorkflowID string

	// AgentID —— 当前 Agent 标识，用于事件推送
	AgentID string

	// Logger —— Temporal 日志记录器
	Logger log.Logger

	// EmitCtx —— 用于执行 SSE 事件推送 Activity 的 Workflow Context
	// 通常使用根 context（非选择性 context），确保事件推送不受选择器限制
	EmitCtx workflow.Context

	// SkipSSEEmit —— 是否跳过 SSE 事件推送。
	// 子工作流将此设为 true，因为父工作流已经负责推送事件。
	// 避免一个用户操作导致多个重复的前端通知。
	SkipSSEEmit bool

	// childWorkflowIDs —— 已注册的子工作流 ID 列表。
	// 使用简单 slice 而不是 sync 容器，因为 Temporal workflow 无并发。
	childWorkflowIDs []string
}

// Setup 初始化信号通道和 Query 处理器。
//
// 必须在工作流代码的开头调用（在第一个 checkpoint 之前）。
//
// 初始化内容：
//   1. workflow.GetVersion() 检查版本兼容性 —— 支持未来不兼容变更
//   2. 创建 WorkflowControlState 实例
//   3. 注册 QueryHandler —— 让外部查询当前控制状态（"control_state_v1"）
//   4. 获取三个 Signal Channel —— pauseCh, resumeCh, cancelCh
//   5. 启动后台 goroutine 消费信号
//
// 版本兼容性示例：
//   v1 = 第一个版本，支持 pause/resume/cancel
//   未来增加 v2 时，可通过 workflow.GetVersion 做行为分支
func (h *SignalHandler) Setup(ctx workflow.Context) {
	version := workflow.GetVersion(ctx, "pause_resume_v1", workflow.DefaultVersion, 1)
	if version < 1 {
		return
	}

	h.State = &WorkflowControlState{}
	h.childWorkflowIDs = []string{}

	// 注册 QueryHandler —— 外部可通过 Temporal Query API 获取控制状态
	// Query 与 Signal 不同：
	//   Signal 改变状态（写操作）
	//   Query 读取状态（读操作，不修改 Workflow 历史）
	_ = workflow.SetQueryHandler(ctx, QueryControlState, func() (WorkflowControlState, error) {
		return *h.State, nil
	})

	// 获取三个控制信号的 Channel
	pauseCh := workflow.GetSignalChannel(ctx, SignalPause)
	resumeCh := workflow.GetSignalChannel(ctx, SignalResume)
	cancelCh := workflow.GetSignalChannel(ctx, SignalCancel)

	// 启动后台 goroutine 持续监听信号
	// 使用 workflow.Go 而非 go 关键字，确保 Temporal 可以追踪此协程
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			sel := workflow.NewSelector(gCtx)

			sel.AddReceive(pauseCh, func(c workflow.ReceiveChannel, more bool) {
				var req PauseRequest
				c.Receive(gCtx, &req)
				h.handlePause(gCtx, req)
			})

			sel.AddReceive(resumeCh, func(c workflow.ReceiveChannel, more bool) {
				var req ResumeRequest
				c.Receive(gCtx, &req)
				h.handleResume(gCtx, req)
			})

			sel.AddReceive(cancelCh, func(c workflow.ReceiveChannel, more bool) {
				var req CancelRequest
				c.Receive(gCtx, &req)
				h.handleCancel(gCtx, req)
			})

			sel.Select(gCtx)
		}
	})
}

// RegisterChildWorkflow 注册子工作流 ID 以实现信号级联传播。
//
// 当父工作流创建子工作流后，应调用此方法记录子工作流的 ID。
// 后续父工作流收到 pause/resume/cancel 信号时，会自动传播给所有子工作流。
//
// 调用时机：
//   通常在执行 ExecuteChildWorkflow 之后立即调用。
func (h *SignalHandler) RegisterChildWorkflow(childID string) {
	h.childWorkflowIDs = append(h.childWorkflowIDs, childID)
}

// UnregisterChildWorkflow 移除已完成的子工作流。
//
// 当子工作流完成后，父工作流应调用此方法取消注册。
// 这可以防止向已结束的工作流发送信号（Temporal 会忽略，但浪费一次网络请求）。
func (h *SignalHandler) UnregisterChildWorkflow(childID string) {
	for i, id := range h.childWorkflowIDs {
		if id == childID {
			h.childWorkflowIDs = append(h.childWorkflowIDs[:i], h.childWorkflowIDs[i+1:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
//  信号处理函数
// ---------------------------------------------------------------------------

// handlePause 处理暂停信号。
//
// 幂等性：如果已经是暂停状态，忽略重复的暂停信号（日志记录 Debug）。
//
// 副作用：
//   1. 更新 State（IsPaused=true, PausedAt, PauseReason, PausedBy）
//   2. 推送 SSE 事件（非子工作流）
//   3. 级联传播到子工作流
//
// 注意：此函数只更新状态和传播信号，真正的阻塞发生在 CheckPausePoint 中。
// 这样设计是因为工作流可能在两次 checkpoint 之间收到暂停信号，
// 如果在收到信号时立即阻塞，会阻塞整个后台协程。
func (h *SignalHandler) handlePause(ctx workflow.Context, req PauseRequest) {
	if h.State.IsPaused {
		h.Logger.Debug("Already paused, ignoring")
		return
	}

	h.State.IsPaused = true
	h.State.PausedAt = workflow.Now(ctx)
	h.State.PauseReason = req.Reason
	h.State.PausedBy = req.RequestedBy

	// 推送 SSE 事件（子工作流跳过，避免重复）
	if !h.SkipSSEEmit {
		_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: h.WorkflowID,
			EventType:  activities.StreamEventWorkflowPausing,
			AgentID:    h.AgentID,
			Message:    activities.MsgWorkflowPausing(req.Reason),
			Timestamp:  workflow.Now(ctx),
		}).Get(ctx, nil)
	}

	// 级联传播到所有子工作流
	h.propagateSignalToChildren(ctx, SignalPause, req)
}

// handleResume 处理恢复信号。
//
// 幂等性：如果不是暂停状态，忽略恢复信号。
func (h *SignalHandler) handleResume(ctx workflow.Context, req ResumeRequest) {
	if !h.State.IsPaused {
		h.Logger.Debug("Not paused, ignoring resume")
		return
	}

	h.State.IsPaused = false
	h.State.PausedAt = time.Time{}
	h.State.PauseReason = ""
	h.State.PausedBy = ""

	if !h.SkipSSEEmit {
		_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: h.WorkflowID,
			EventType:  activities.StreamEventWorkflowResumed,
			AgentID:    h.AgentID,
			Message:    activities.MsgWorkflowResumed(req.Reason),
			Timestamp:  workflow.Now(ctx),
		}).Get(ctx, nil)
	}

	h.propagateSignalToChildren(ctx, SignalResume, req)
}

// handleCancel 处理取消信号。
//
// 与暂停不同，取消是终态 —— 一旦取消就无法恢复。
//
// 取消的传播路径：
//   1. handleCancel 设置 IsCancelled=true
//   2. 传播给子工作流
//   3. 下次调用 CheckPausePoint 时，发现 IsCancelled=true
//   4. CheckPausePoint 发送 WORKFLOW_CANCELLED 事件
//   5. CheckPausePoint 返回 temporal.NewCanceledError
//   6. 工作流函数返回错误，Temporal 标记状态为 CANCELLED
//
// 为什么用 temporal.NewCanceledError 而不是 fmt.Errorf：
//   如果返回普通错误，Temporal 会将工作流标记为 FAILED
//   使用 CanceledError，Temporal 会标记为 CANCELLED（语义更准确）
func (h *SignalHandler) handleCancel(ctx workflow.Context, req CancelRequest) {
	h.State.IsCancelled = true
	h.State.CancelReason = req.Reason
	h.State.CancelledBy = req.RequestedBy

	if !h.SkipSSEEmit {
		_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: h.WorkflowID,
			EventType:  activities.StreamEventWorkflowCancelling,
			AgentID:    h.AgentID,
			Message:    activities.MsgWorkflowCancelling(req.Reason),
			Timestamp:  workflow.Now(ctx),
		}).Get(ctx, nil)
	}

	h.propagateSignalToChildren(ctx, SignalCancel, req)
}

// propagateSignalToChildren 将所有已注册子工作流发送指定的控制信号。
//
// 实现细节：
//   1. 先拷贝 childWorkflowIDs 列表（防止迭代过程中被修改）
//   2. 并行发送所有信号（使用 futures 并发）
//   3. 忽略子工作流传入的错误（子工作流可能已结束）
//
// 为什么忽略错误：
//   如果子工作流已经完成或不存在，Temporal 会返回错误。
//   但这些错误不应该阻止信号传播到其他仍在运行的子工作流。
//   "发送并忘记"是最安全的选择。
func (h *SignalHandler) propagateSignalToChildren(ctx workflow.Context, signalName string, payload interface{}) {
	if len(h.childWorkflowIDs) == 0 {
		return
	}

	children := make([]string, len(h.childWorkflowIDs))
	copy(children, h.childWorkflowIDs)

	futures := make([]workflow.Future, 0, len(children))
	for _, childID := range children {
		// SignalExternalWorkflow 发送给指定工作流（空运行空间名 = 当前命名空间）
		future := workflow.SignalExternalWorkflow(ctx, childID, "", signalName, payload)
		futures = append(futures, future)
	}

	for _, future := range futures {
		_ = future.Get(ctx, nil)
	}
}

// ---------------------------------------------------------------------------
//  检查点
// ---------------------------------------------------------------------------

// CheckPausePoint 在工作流的关键节点插入检查点。
//
// 调用约定：
//   在工作流的每个"可中断"操作之前调用：
//     - 长时间运行的计算之前
//     - 外部工具调用之前
//     - LLM 调用之前
//     - 递归或循环的每次迭代开始处
//
// 行为：
//   1. workflow.Sleep(ctx, 0) —— 让出执行权，确保待处理的 Signal 被处理
//   2. 检查 IsCancelled —— 如果已取消，推送 WORKFLOW_CANCELLED 事件并返回 CanceledError
//   3. 检查 IsPaused —— 如果已暂停，推送 WORKFLOW_PAUSED 事件
//   4. 阻塞等待恢复 —— 使用 workflow.Await 等待 IsPaused=false 或 IsCancelled=true
//   5. 如果在暂停中被取消，推送取消事件并返回错误
//   6. 一切正常，返回 nil 继续执行
//
// workflow.Await 的优势（vs for + Sleep 轮询）：
//   轮询方案：每次 Sleep 都会在 Workflow 历史中产生一条 Timer 事件
//   Await 方案：在历史中只产生一条 Marker，条件满足时自动恢复
//   两者效果相同，但 Await 更高效（更少的历史事件 = 更低的成本）
func (h *SignalHandler) CheckPausePoint(ctx workflow.Context, checkpoint string) error {
	if h.State == nil {
		return nil
	}

	// 让出执行权，使 Selector 有机会消费待处理的信号
	// 如果不让出，可能在两次 checkpoint 之间收到 pause 信号但 State 未更新
	_ = workflow.Sleep(ctx, 0)

	if h.State.IsCancelled {
		// 发送 WORKFLOW_CANCELLED 事件
		// 子工作流也需要发送此事件，因为父工作流不知道子工作流何时真正停止
		_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: h.WorkflowID,
			EventType:  activities.StreamEventWorkflowCancelled,
			AgentID:    h.AgentID,
			Message:    activities.MsgWorkflowCancelled(h.State.CancelReason),
			Timestamp:  workflow.Now(ctx),
			Payload:    map[string]interface{}{"checkpoint": checkpoint},
		}).Get(ctx, nil)
		// 返回 CanceledError 使 Temporal 标记状态为 CANCELLED 而非 FAILED
		return temporal.NewCanceledError(fmt.Sprintf("workflow cancelled: %s", h.State.CancelReason))
	}

	if h.State.IsPaused {
		// 发送 WORKFLOW_PAUSED 事件
		_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: h.WorkflowID,
			EventType:  activities.StreamEventWorkflowPaused,
			AgentID:    h.AgentID,
			Message:    activities.MsgWorkflowPaused(),
			Timestamp:  workflow.Now(ctx),
			Payload:    map[string]interface{}{"checkpoint": checkpoint},
		}).Get(ctx, nil)

		// 阻塞直到恢复或取消
		// Await 条件是闭包，每次 Await 都会重新检查条件
		// 条件满足时 Temporal 自动唤醒工作流，不会占用时间片
		_ = workflow.Await(ctx, func() bool {
			return !h.State.IsPaused || h.State.IsCancelled
		})

		if h.State.IsCancelled {
			// 在暂停中被取消了
			_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
				WorkflowID: h.WorkflowID,
				EventType:  activities.StreamEventWorkflowCancelled,
				AgentID:    h.AgentID,
				Message:    activities.MsgWorkflowCancelled(h.State.CancelReason),
				Timestamp:  workflow.Now(ctx),
				Payload:    map[string]interface{}{"checkpoint": checkpoint, "was_paused": true},
			}).Get(ctx, nil)
			return temporal.NewCanceledError(fmt.Sprintf("workflow cancelled while paused: %s", h.State.CancelReason))
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
//  状态查询辅助方法
// ---------------------------------------------------------------------------

// IsCancelled 快速检查工作流是否已被取消。
// 在不想用 CheckPausePoint 阻塞的场景下使用（如循环条件判断）。
func (h *SignalHandler) IsCancelled() bool {
	return h.State != nil && h.State.IsCancelled
}

// IsPaused 快速检查工作流是否处于暂停状态。
func (h *SignalHandler) IsPaused() bool {
	return h.State != nil && h.State.IsPaused
}

// EmitCancelledIfNeeded 在工作流结束前发送取消 SSE 事件（如果工作流已被取消）。
//
// 使用场景：
//   如果工作流在循环中多次检查 IsCancelled 来决定退出，
//   但在退出前没有经过 CheckPausePoint（所以没有发送取消事件），
//   则需要在工作流返回前调用此方法，确保前端收到取消通知。
//
// 与 CheckPausePoint 的关系：
//   CheckPausePoint 在取消时自动发送事件并返回错误。
//   EmitCancelledIfNeeded 是兜底方案 —— 确保没有被 CheckPausePoint 覆盖的路径也能发送事件。
//
// 参数：
//   reason —— 默认取消原因（如果 State.CancelReason 为空，使用此值）
func (h *SignalHandler) EmitCancelledIfNeeded(ctx workflow.Context, reason string) {
	if h.State == nil || !h.State.IsCancelled || h.SkipSSEEmit {
		return
	}

	cancelReason := h.State.CancelReason
	if cancelReason == "" {
		cancelReason = reason
	}

	_ = workflow.ExecuteActivity(h.EmitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
		WorkflowID: h.WorkflowID,
		EventType:  activities.StreamEventWorkflowCancelled,
		AgentID:    h.AgentID,
		Message:    activities.MsgWorkflowCancelled(cancelReason),
		Timestamp:  workflow.Now(ctx),
		Payload:    map[string]interface{}{"reason": cancelReason, "at_return": true},
	}).Get(ctx, nil)
}
