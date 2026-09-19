// =============================================================================
// 文件: go/orchestrator/internal/workflows/scheduled/scheduled_task_workflow.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   定时任务执行包装 workflow，按计划触发并包装实际任务执行流程。
// 【关键内容】
//   - ScheduledTaskWorkflow: Temporal workflow 入口
//   - 调度触发、上下文构造与子 workflow 调用编排
// 【协作关系】
//   由 daemon_dispatch activity 触发，进而调用实际业务 workflow。
// =============================================================================
package scheduled

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/activities"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/constants"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/daemon"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/schedules"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows"
)

// ScheduledTaskWorkflow wraps existing workflows for scheduled execution
func ScheduledTaskWorkflow(ctx workflow.Context, input schedules.ScheduledTaskInput) error {
	logger := workflow.GetLogger(ctx)

	// Validate and parse UUIDs
	scheduleID, err := uuid.Parse(input.ScheduleID)
	if err != nil {
		return fmt.Errorf("invalid schedule_id: %w", err)
	}
	userID, err := uuid.Parse(input.UserID)
	if err != nil {
		return fmt.Errorf("invalid user_id: %w", err)
	}
	tenantID, err := uuid.Parse(input.TenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant_id: %w", err)
	}

	logger.Info("Scheduled task execution started",
		"schedule_id", input.ScheduleID,
		"query", input.TaskQuery,
	)

	// Activity context for recording
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	})

	// Generate unique child workflow ID - this is the task_id for unified tracking
	parentWorkflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	childWorkflowID := fmt.Sprintf("%s-exec", parentWorkflowID)

	// 1. Record execution start with task_executions persistence
	err = workflow.ExecuteActivity(activityCtx, "RecordScheduleExecutionStart",
		activities.RecordScheduleExecutionInput{
			ScheduleID: scheduleID,
			TaskID:     childWorkflowID, // Use child workflow ID for task_executions
			Query:      input.TaskQuery,
			UserID:     input.UserID,
			TenantID:   input.TenantID,
		},
	).Get(ctx, nil)
	if err != nil {
		logger.Error("Failed to record execution start", "error", err)
		// Continue anyway - don't block execution
	}

	// 1b. Daemon dispatch path — if execution_target is "daemon", dispatch to
	// a connected daemon instead of running the cloud OrchestratorWorkflow.
	// Version-gated for Temporal replay compatibility.
	vDaemon := workflow.GetVersion(ctx, "scheduled_daemon_dispatch_v1", workflow.DefaultVersion, 1)
	if vDaemon == 1 {
		execTarget, _ := input.TaskContext["execution_target"].(string)
		if execTarget == "daemon" {
			return executeDaemonPath(ctx, activityCtx, input, scheduleID, childWorkflowID)
		}
	}

	// 2. Prepare task input for existing workflow
	taskInput := workflows.TaskInput{
		Query:    input.TaskQuery,
		Context:  input.TaskContext,
		UserID:   userID.String(),
		TenantID: tenantID.String(),
	}

	// Ensure context map exists for scheduled task metadata
	if taskInput.Context == nil {
		taskInput.Context = make(map[string]interface{})
	}

	// Mark as scheduled execution so downstream workflows know this is an
	// automated run — not a user asking to "set up" a schedule.
	taskInput.Context["trigger_type"] = "schedule"
	taskInput.Context["schedule_id"] = input.ScheduleID

	// Add budget to context if specified
	if input.MaxBudgetPerRunUSD > 0 {
		taskInput.Context["max_budget_usd"] = input.MaxBudgetPerRunUSD
	}

	// 3. Execute main workflow (use orchestrator router to select appropriate workflow type)
	childWorkflowOptions := workflow.ChildWorkflowOptions{
		WorkflowID:          childWorkflowID,
		TaskQueue:           "shannon-tasks",
		WorkflowRunTimeout:  workflow.GetInfo(ctx).WorkflowRunTimeout, // Inherit timeout
		WorkflowTaskTimeout: 10 * time.Second,
		Memo: map[string]interface{}{
			"schedule_id":  input.ScheduleID,
			"user_id":      input.UserID,
			"tenant_id":    input.TenantID,
			"trigger_type": "schedule",
		},
	}

	childCtx := workflow.WithChildOptions(ctx, childWorkflowOptions)

	var result workflows.TaskResult
	err = workflow.ExecuteChildWorkflow(childCtx, workflows.OrchestratorWorkflow, taskInput).Get(ctx, &result)

	// 4. Record execution result with task_executions persistence
	status := "COMPLETED"
	errorMsg := ""
	totalCost := 0.0
	resultText := ""

	// Metadata fields to extract from child workflow (Option A: unified model)
	var modelUsed, provider string
	var totalTokens, promptTokens, completionTokens int

	if err != nil {
		status = "FAILED"
		errorMsg = err.Error()
		logger.Error("Scheduled task failed", "error", err)
	} else if !result.Success {
		status = "FAILED"
		errorMsg = result.ErrorMessage
		logger.Warn("Scheduled task completed with failure", "error", errorMsg)
	} else {
		logger.Info("Scheduled task completed successfully")
		resultText = result.Result

		// Extract all metadata from child workflow result for unified task_executions
		if result.Metadata != nil {
			// Cost: try total_cost_usd first, then cost_usd
			if cost, ok := result.Metadata["total_cost_usd"].(float64); ok {
				totalCost = cost
			}
			if cost, ok := result.Metadata["cost_usd"].(float64); ok && totalCost == 0 {
				totalCost = cost
			}

			// Model: try model_used first, then model
			if m, ok := result.Metadata["model_used"].(string); ok && m != "" {
				modelUsed = m
			} else if m, ok := result.Metadata["model"].(string); ok && m != "" {
				modelUsed = m
			}

			// Provider
			if p, ok := result.Metadata["provider"].(string); ok {
				provider = p
			}

			// Tokens: handle both int and float64 (JSON unmarshals numbers as float64)
			if t, ok := result.Metadata["total_tokens"].(int); ok {
				totalTokens = t
			} else if t, ok := result.Metadata["total_tokens"].(float64); ok {
				totalTokens = int(t)
			}

			if t, ok := result.Metadata["input_tokens"].(int); ok {
				promptTokens = t
			} else if t, ok := result.Metadata["input_tokens"].(float64); ok {
				promptTokens = int(t)
			}

			if t, ok := result.Metadata["output_tokens"].(int); ok {
				completionTokens = t
			} else if t, ok := result.Metadata["output_tokens"].(float64); ok {
				completionTokens = int(t)
			}

			logger.Info("Extracted metadata from child workflow",
				"model", modelUsed,
				"provider", provider,
				"total_tokens", totalTokens,
				"cost", totalCost,
			)
		}
	}

	// Fallback: if metadata didn't contain total_tokens, use result.TokensUsed
	// (the canonical field on TaskResult). Covers cases where metadata is nil
	// or doesn't include token counts, and also FAILED runs that consumed tokens.
	if totalTokens == 0 && result.TokensUsed > 0 {
		totalTokens = result.TokensUsed
	}

	err = workflow.ExecuteActivity(activityCtx, "RecordScheduleExecutionComplete",
		activities.RecordScheduleExecutionCompleteInput{
			ScheduleID:       scheduleID,
			TaskID:           childWorkflowID,
			Status:           status,
			TotalCost:        totalCost,
			ErrorMsg:         errorMsg,
			Result:           resultText,
			ModelUsed:        modelUsed,
			Provider:         provider,
			TotalTokens:      totalTokens,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			ResultMetadata:   result.Metadata,
		},
	).Get(ctx, nil)
	if err != nil {
		logger.Error("Failed to record execution completion", "error", err)
	}

	// 5. Return error if task failed (for Temporal's built-in retry/failure handling)
	if status == "FAILED" {
		return fmt.Errorf("scheduled task failed: %s", errorMsg)
	}

	return nil
}

// executeDaemonPath dispatches a scheduled task to a connected daemon and waits
// for a reply via Temporal signal. If no daemon is connected or the reply times
// out, the run is recorded as FAILED (no cloud fallback).
func executeDaemonPath(
	ctx workflow.Context,
	activityCtx workflow.Context,
	input schedules.ScheduledTaskInput,
	scheduleID uuid.UUID,
	childWorkflowID string,
) error {
	logger := workflow.GetLogger(ctx)
	wfInfo := workflow.GetInfo(ctx)

	// Extract agent_name from task context if present.
	agentName, _ := input.TaskContext["agent_name"].(string)

	// Dispatch to daemon (fast activity, <1s).
	dispatchInput := DaemonDispatchInput{
		TenantID:      input.TenantID,
		UserID:        input.UserID,
		TaskQuery:     input.TaskQuery,
		AgentName:     agentName,
		WorkflowID:    wfInfo.WorkflowExecution.ID,
		WorkflowRunID: wfInfo.WorkflowExecution.RunID,
	}

	var dispatchResult DaemonDispatchResult
	err := workflow.ExecuteActivity(activityCtx, constants.DaemonDispatchActivity, dispatchInput).Get(ctx, &dispatchResult)
	if err != nil {
		logger.Error("Daemon dispatch activity failed", "error", err)
		return recordDaemonFailure(ctx, activityCtx, scheduleID, childWorkflowID, "daemon dispatch activity error: "+err.Error())
	}

	if !dispatchResult.Dispatched {
		logger.Info("No daemon connected, recording failure",
			"error", dispatchResult.Error,
		)
		return recordDaemonFailure(ctx, activityCtx, scheduleID, childWorkflowID, "no daemon connected: "+dispatchResult.Error)
	}

	// Wait for daemon reply via Temporal signal, with timeout.
	signalCh := workflow.GetSignalChannel(ctx, SignalDaemonReply)

	timerCtx, timerCancel := workflow.WithCancel(ctx)
	timer := workflow.NewTimer(timerCtx, DaemonReplyTimeout)

	var reply daemon.ReplyPayload
	var timedOut bool

	sel := workflow.NewSelector(ctx)
	sel.AddReceive(signalCh, func(ch workflow.ReceiveChannel, more bool) {
		ch.Receive(ctx, &reply)
		timerCancel()
	})
	sel.AddFuture(timer, func(f workflow.Future) {
		timedOut = true
	})
	sel.Select(ctx)

	if timedOut {
		logger.Warn("Daemon reply timed out",
			"timeout", DaemonReplyTimeout,
			"workflow_id", wfInfo.WorkflowExecution.ID,
		)
		return recordDaemonFailure(ctx, activityCtx, scheduleID, childWorkflowID, fmt.Sprintf("daemon reply timed out after %s", DaemonReplyTimeout))
	}

	logger.Info("Daemon reply received",
		"thread_id", reply.ThreadID,
		"text_length", len(reply.Text),
	)

	// Record successful daemon execution.
	_ = workflow.ExecuteActivity(activityCtx, "RecordScheduleExecutionComplete",
		activities.RecordScheduleExecutionCompleteInput{
			ScheduleID: scheduleID,
			TaskID:     childWorkflowID,
			Status:     "COMPLETED",
			Result:     reply.Text,
			Provider:   "daemon",
		},
	).Get(ctx, nil)

	return nil
}

// recordDaemonFailure records a FAILED execution for the daemon path.
func recordDaemonFailure(
	ctx workflow.Context,
	activityCtx workflow.Context,
	scheduleID uuid.UUID,
	childWorkflowID string,
	errorMsg string,
) error {
	_ = workflow.ExecuteActivity(activityCtx, "RecordScheduleExecutionComplete",
		activities.RecordScheduleExecutionCompleteInput{
			ScheduleID: scheduleID,
			TaskID:     childWorkflowID,
			Status:     "FAILED",
			ErrorMsg:   errorMsg,
		},
	).Get(ctx, nil)

	return fmt.Errorf("scheduled task failed: %s", errorMsg)
}
