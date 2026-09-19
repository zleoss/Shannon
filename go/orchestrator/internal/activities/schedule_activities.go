// =============================================================================
// 文件: go/orchestrator/internal/activities/schedule_activities.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   定时记录 activity —— 创建/更新/删除定时任务数据库记录。
// 【关键内容】
//   CreateScheduleRecord / UpdateScheduleRecord / DeleteScheduleRecord
// 【协作关系】
//   由定时任务的 Temporal workflow 调用，操作 schedule 相关数据库表。
// =============================================================================
package activities

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.temporal.io/sdk/client"
	"go.uber.org/zap"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/db"
)

// ScheduleActivities holds dependencies for schedule-related activities
type ScheduleActivities struct {
	DB             *sql.DB
	TemporalClient client.Client // optional, nil = skip Temporal schedule pause
	Logger         *zap.Logger
}

// NewScheduleActivities creates a new ScheduleActivities instance
func NewScheduleActivities(sqlDB *sql.DB, temporalClient client.Client, logger *zap.Logger) *ScheduleActivities {
	return &ScheduleActivities{
		DB:             sqlDB,
		TemporalClient: temporalClient,
		Logger:         logger,
	}
}

// RecordScheduleExecutionInput is the input for starting execution tracking
type RecordScheduleExecutionInput struct {
	ScheduleID uuid.UUID
	TaskID     string // workflow_id of the child workflow
	Query      string // task query for display
	UserID     string
	TenantID   string
}

// RecordScheduleExecutionStart logs the start of a scheduled execution
// and creates a task_executions record for unified task tracking
func (a *ScheduleActivities) RecordScheduleExecutionStart(ctx context.Context, input RecordScheduleExecutionInput) error {
	a.Logger.Debug("Recording schedule execution start",
		zap.String("schedule_id", input.ScheduleID.String()),
		zap.String("task_id", input.TaskID),
	)

	// Start transaction for atomic multi-table insert
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		a.Logger.Error("Failed to begin transaction", zap.Error(err))
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback if not committed

	// 1. Create link record in scheduled_task_executions
	_, err = tx.ExecContext(ctx, `
		INSERT INTO scheduled_task_executions (schedule_id, task_id, triggered_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (schedule_id, task_id) DO NOTHING
	`, input.ScheduleID, input.TaskID)
	if err != nil {
		a.Logger.Error("Failed to create scheduled_task_executions link", zap.Error(err))
		return fmt.Errorf("failed to create scheduled_task_executions link: %w", err)
	}

	// 2. Create task_executions record using shared persistence code
	dbClient := GetGlobalDBClient()
	if dbClient == nil {
		a.Logger.Warn("Global DB client not available, skipping task_executions persistence")
		// Commit the link record even if task_executions fails
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}
		return nil
	}

	// Parse user/tenant IDs
	var userID *uuid.UUID
	if input.UserID != "" {
		if uid, err := uuid.Parse(input.UserID); err == nil {
			userID = &uid
		}
	}

	// Use synthetic session ID for scheduled runs: "schedule:<scheduleID>:<taskID>"
	// Use full IDs to avoid collisions and panics from short IDs
	sessionID := fmt.Sprintf("schedule:%s:%s", input.ScheduleID.String(), input.TaskID)

	task := &db.TaskExecution{
		WorkflowID:  input.TaskID,
		UserID:      userID,
		SessionID:   sessionID,
		Query:       input.Query,
		Status:      "RUNNING",
		StartedAt:   time.Now(),
		TriggerType: "schedule",
		ScheduleID:  &input.ScheduleID,
	}

	if err := dbClient.SaveTaskExecution(ctx, task); err != nil {
		a.Logger.Error("Failed to create task_executions record for scheduled run",
			zap.String("task_id", input.TaskID),
			zap.Error(err))
		// Rollback transaction on task_executions failure
		return fmt.Errorf("failed to create task_executions record: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		a.Logger.Error("Failed to commit transaction", zap.Error(err))
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	a.Logger.Info("Scheduled execution started and task_executions record created",
		zap.String("schedule_id", input.ScheduleID.String()),
		zap.String("task_id", input.TaskID),
		zap.String("trigger_type", "schedule"),
	)

	return nil
}

// RecordScheduleExecutionCompleteInput is the input for completing execution tracking
type RecordScheduleExecutionCompleteInput struct {
	ScheduleID uuid.UUID
	TaskID     string
	Status     string // COMPLETED, FAILED, CANCELLED
	TotalCost  float64
	ErrorMsg   string
	Result     string // optional result text

	// Metadata from child workflow (for unified task_executions consistency)
	ModelUsed        string
	Provider         string
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int

	// Full metadata from child workflow result (screenshot_urls, pipeline, etc.)
	ResultMetadata map[string]interface{}
}

// RecordScheduleExecutionComplete logs the completion of a scheduled execution
// and updates the task_executions record
func (a *ScheduleActivities) RecordScheduleExecutionComplete(ctx context.Context, input RecordScheduleExecutionCompleteInput) error {
	a.Logger.Debug("Recording schedule execution completion",
		zap.String("schedule_id", input.ScheduleID.String()),
		zap.String("task_id", input.TaskID),
		zap.String("status", input.Status),
		zap.Float64("cost", input.TotalCost),
	)

	// 1. Update task_executions via shared persistence code
	dbClient := GetGlobalDBClient()
	if dbClient != nil {
		now := time.Now()
		var result *string
		if input.Result != "" {
			result = &input.Result
		}
		var errorMsg *string
		if input.ErrorMsg != "" {
			errorMsg = &input.ErrorMsg
		}

		// Get existing task to preserve fields, then update
		existingTask, err := dbClient.GetTaskExecution(ctx, input.TaskID)
		if err != nil {
			a.Logger.Warn("Failed to get existing task_executions record",
				zap.String("task_id", input.TaskID),
				zap.Error(err))
		}

		if existingTask != nil {
			// Update existing record with completion info
			existingTask.Status = input.Status
			existingTask.CompletedAt = &now
			existingTask.TotalCostUSD = input.TotalCost
			if result != nil {
				existingTask.Result = result
			}
			if errorMsg != nil {
				existingTask.ErrorMessage = errorMsg
			}

			// Populate metadata from child workflow result (Option A: unified model)
			if input.ModelUsed != "" {
				existingTask.ModelUsed = input.ModelUsed
			}
			if input.Provider != "" {
				existingTask.Provider = input.Provider
			}
			if input.TotalTokens > 0 {
				existingTask.TotalTokens = input.TotalTokens
			}
			if input.PromptTokens > 0 {
				existingTask.PromptTokens = input.PromptTokens
			}
			if input.CompletionTokens > 0 {
				existingTask.CompletionTokens = input.CompletionTokens
			}

			// Write full metadata from child workflow result (screenshot_urls, pipeline, etc.)
			if len(input.ResultMetadata) > 0 {
				existingTask.Metadata = db.JSONB(input.ResultMetadata)
			}

			// Calculate duration if we have start time
			if !existingTask.StartedAt.IsZero() {
				durationMs := int(now.Sub(existingTask.StartedAt).Milliseconds())
				existingTask.DurationMs = &durationMs
			}

			if err := dbClient.SaveTaskExecution(ctx, existingTask); err != nil {
				a.Logger.Warn("Failed to update task_executions record",
					zap.String("task_id", input.TaskID),
					zap.Error(err))
			} else {
				a.Logger.Debug("Updated task_executions record with completion status and metadata",
					zap.String("task_id", input.TaskID),
					zap.String("status", input.Status),
					zap.String("model", input.ModelUsed),
					zap.String("provider", input.Provider),
					zap.Int("total_tokens", input.TotalTokens))
			}
		}
	}

	// 2. Update schedule statistics with atomic SQL (prevents race conditions)
	if input.Status == "COMPLETED" {
		_, err := a.DB.ExecContext(ctx, `
			UPDATE scheduled_tasks
			SET total_runs = total_runs + 1,
				successful_runs = successful_runs + 1,
				last_run_at = NOW(),
				updated_at = NOW()
			WHERE id = $1
		`, input.ScheduleID)
		if err != nil {
			a.Logger.Error("Failed to update schedule statistics",
				zap.String("schedule_id", input.ScheduleID.String()),
				zap.Error(err))
		}
	} else if input.Status == "FAILED" {
		_, err := a.DB.ExecContext(ctx, `
			UPDATE scheduled_tasks
			SET total_runs = total_runs + 1,
				failed_runs = failed_runs + 1,
				last_run_at = NOW(),
				updated_at = NOW()
			WHERE id = $1
		`, input.ScheduleID)
		if err != nil {
			a.Logger.Error("Failed to update schedule statistics",
				zap.String("schedule_id", input.ScheduleID.String()),
				zap.Error(err))
		}
	}

	// 3. Update next_run_at
	a.updateNextRunAt(ctx, input.ScheduleID)

	a.Logger.Info("Scheduled execution completed",
		zap.String("schedule_id", input.ScheduleID.String()),
		zap.String("task_id", input.TaskID),
		zap.String("status", input.Status),
		zap.Float64("cost", input.TotalCost),
	)

	return nil
}

// PauseScheduleForQuotaInput is the input for PauseScheduleForQuota activity.
type PauseScheduleForQuotaInput struct {
	ScheduleID uuid.UUID
	Reason     string
}

// PauseScheduleForQuota pauses a Temporal schedule and updates DB status.
// Used by ScheduledTaskWorkflow to auto-pause free-tier schedules on quota exceeded.
func (a *ScheduleActivities) PauseScheduleForQuota(ctx context.Context, input PauseScheduleForQuotaInput) error {
	reason := input.Reason
	if reason == "" {
		reason = "Auto-paused: quota exceeded"
	}

	// 1. Get temporal_schedule_id from DB
	var temporalScheduleID, currentStatus string
	err := a.DB.QueryRowContext(ctx, `
		SELECT temporal_schedule_id, status FROM scheduled_tasks WHERE id = $1
	`, input.ScheduleID).Scan(&temporalScheduleID, &currentStatus)
	if err != nil {
		a.Logger.Error("Failed to get schedule for auto-pause",
			zap.String("schedule_id", input.ScheduleID.String()),
			zap.Error(err))
		return nil // non-fatal
	}

	if currentStatus == "PAUSED" {
		a.Logger.Debug("Schedule already paused, skipping",
			zap.String("schedule_id", input.ScheduleID.String()))
		return nil
	}

	// 2. Pause in Temporal (if client available)
	// Must succeed before updating DB to prevent desync (DB=PAUSED but Temporal still fires).
	if a.TemporalClient != nil && temporalScheduleID != "" {
		handle := a.TemporalClient.ScheduleClient().GetHandle(ctx, temporalScheduleID)
		if err := handle.Pause(ctx, client.SchedulePauseOptions{
			Note: reason,
		}); err != nil {
			a.Logger.Error("Failed to pause Temporal schedule, skipping DB update to avoid desync",
				zap.String("schedule_id", input.ScheduleID.String()),
				zap.String("temporal_id", temporalScheduleID),
				zap.Error(err))
			return nil // non-fatal, next execution will retry pause
		}
	}

	// 3. Update DB status (only if Temporal pause succeeded)
	_, err = a.DB.ExecContext(ctx, `
		UPDATE scheduled_tasks SET status = 'PAUSED', pause_reason = 'quota_exceeded', updated_at = NOW() WHERE id = $1
	`, input.ScheduleID)
	if err != nil {
		a.Logger.Error("Failed to update schedule status to PAUSED",
			zap.String("schedule_id", input.ScheduleID.String()),
			zap.Error(err))
		return nil // non-fatal — Temporal is paused, DB will catch up on next query
	}

	a.Logger.Info("Auto-paused schedule due to quota exceeded",
		zap.String("schedule_id", input.ScheduleID.String()),
		zap.String("reason", reason))

	return nil
}

// updateNextRunAt calculates and updates the next run time for a schedule.
// NOTE: This is a best-effort local calculation. The authoritative next_run_at
// is maintained by Temporal. Activities cannot access ScheduleClient.Describe()
// directly. For accurate next_run_at after schedule changes (cron/timezone),
// see manager.go which reads from Temporal after updates.
func (a *ScheduleActivities) updateNextRunAt(ctx context.Context, scheduleID uuid.UUID) {
	// Fetch schedule's cron expression and timezone
	var cronExpr, timezone string
	err := a.DB.QueryRowContext(ctx, `
		SELECT cron_expression, timezone
		FROM scheduled_tasks
		WHERE id = $1 AND status = 'ACTIVE'
	`, scheduleID).Scan(&cronExpr, &timezone)

	if err != nil {
		a.Logger.Warn("Failed to fetch schedule for next_run_at update",
			zap.String("schedule_id", scheduleID.String()),
			zap.Error(err))
		return
	}

	// Parse cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(cronExpr)
	if err != nil {
		a.Logger.Error("Failed to parse cron expression",
			zap.String("schedule_id", scheduleID.String()),
			zap.String("cron", cronExpr),
			zap.Error(err))
		return
	}

	// Load timezone
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		a.Logger.Warn("Invalid timezone, using UTC",
			zap.String("schedule_id", scheduleID.String()),
			zap.String("timezone", timezone))
		loc = time.UTC
	}

	// Calculate next run time
	nextRun := schedule.Next(time.Now().In(loc))

	// Update database
	_, err = a.DB.ExecContext(ctx, `
		UPDATE scheduled_tasks
		SET next_run_at = $1,
			updated_at = NOW()
		WHERE id = $2
	`, nextRun, scheduleID)

	if err != nil {
		a.Logger.Error("Failed to update next_run_at",
			zap.String("schedule_id", scheduleID.String()),
			zap.Error(err))
	} else {
		a.Logger.Debug("Updated next_run_at",
			zap.String("schedule_id", scheduleID.String()),
			zap.Time("next_run", nextRun))
	}
}
