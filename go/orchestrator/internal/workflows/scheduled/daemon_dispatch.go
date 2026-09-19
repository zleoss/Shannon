// =============================================================================
// 文件: go/orchestrator/internal/workflows/scheduled/daemon_dispatch.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   daemon 分发 activity，周期性扫描待执行任务并派发到对应 workflow。
// 【关键内容】
//   - DispatchActivity: 扫描调度表并触发 ScheduledTaskWorkflow
//   - 错误重试与去重处理
// 【协作关系】
//   作为 Temporal activity 被 daemon worker 周期调用。
// =============================================================================
package scheduled

import (
	"context"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/daemon"
	"go.temporal.io/sdk/activity"
	"go.uber.org/zap"
)

// SignalDaemonReply is the Temporal signal name used to deliver daemon replies
// back to the waiting ScheduledTaskWorkflow.
const SignalDaemonReply = "daemon-reply-v1"

// DaemonReplyTimeout is how long the workflow waits for a daemon reply before
// recording failure if no daemon responds.
const DaemonReplyTimeout = 10 * time.Minute

// DaemonDispatchInput is the input for the DaemonDispatchActivity.
type DaemonDispatchInput struct {
	TenantID      string
	UserID        string
	TaskQuery     string
	AgentName     string
	WorkflowID    string // Temporal workflow ID for signal routing
	WorkflowRunID string // Temporal run ID for signal routing
}

// DaemonDispatchResult is the output of the DaemonDispatchActivity.
type DaemonDispatchResult struct {
	Dispatched bool
	Error      string
}

// DaemonActivities holds shared state (the Hub) for daemon-related activities.
type DaemonActivities struct {
	hub *daemon.Hub
}

// NewDaemonActivities creates a DaemonActivities with access to the Hub.
func NewDaemonActivities(hub *daemon.Hub) *DaemonActivities {
	return &DaemonActivities{hub: hub}
}

// DaemonDispatchActivity publishes a task to the Daemon Hub.
// This is a fast activity (<1s) — it does not wait for the daemon response.
// The workflow uses a Temporal signal (SignalDaemonReply) to receive the reply.
func (da *DaemonActivities) DaemonDispatchActivity(ctx context.Context, input DaemonDispatchInput) (DaemonDispatchResult, error) {
	logger := activity.GetLogger(ctx)

	if da.hub == nil {
		logger.Info("daemon hub not configured, cannot dispatch")
		return DaemonDispatchResult{Dispatched: false, Error: "daemon hub not configured"}, nil
	}

	payload := daemon.MessagePayload{
		Channel:   daemon.ChannelSchedule,
		ThreadID:  input.WorkflowID, // Use workflow ID as thread for sticky routing
		Sender:    "scheduler",
		Text:      input.TaskQuery,
		AgentName: input.AgentName,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	meta := daemon.ClaimMetadata{
		ChannelType:   daemon.ChannelSchedule,
		ThreadID:      input.WorkflowID,
		WorkflowID:    input.WorkflowID,
		WorkflowRunID: input.WorkflowRunID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}

	// Publish to Redis stream — the gateway Hub subscribes and dispatches to local WS connections.
	// This bridges the orchestrator process (no WS connections) and gateway process (has connections).
	req := daemon.DispatchRequest{
		TenantID: input.TenantID,
		UserID:   input.UserID,
		Payload:  payload,
		Meta:     meta,
	}
	err := da.hub.PublishDispatch(ctx, req)
	if err != nil {
		logger.Info("daemon dispatch publish failed",
			zap.String("tenant_id", input.TenantID),
			zap.String("error", err.Error()),
		)
		return DaemonDispatchResult{
			Dispatched: false,
			Error:      err.Error(),
		}, nil
	}

	logger.Info("daemon dispatch published to stream",
		zap.String("tenant_id", input.TenantID),
		zap.String("workflow_id", input.WorkflowID),
	)

	return DaemonDispatchResult{
		Dispatched: true,
	}, nil
}
