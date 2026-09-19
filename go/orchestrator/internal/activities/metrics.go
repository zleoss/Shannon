// =============================================================================
// 文件: go/orchestrator/internal/activities/metrics.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Activity 级别 Prometheus 指标采集。
// 【关键内容】
//   执行计数、延迟、错误率、token 用量
// 【协作关系】
//   各 activity 在执行前后调用指标记录方法，数据上报至 Prometheus。
// =============================================================================
package activities

import (
	"context"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows/metrics"
)

// PatternMetricsInput contains pattern execution metrics
type PatternMetricsInput struct {
	Pattern      string
	Version      string
	AgentCount   int
	TokensUsed   int
	Duration     time.Duration
	Improved     bool // For reflection pattern
	WorkflowType string
}

// RecordPatternMetrics records pattern execution metrics
func RecordPatternMetrics(ctx context.Context, input PatternMetricsInput) error {
	// Record pattern execution
	metrics.RecordPatternExecution(input.Pattern, input.Version)

	// Record duration if provided
	if input.Duration > 0 {
		metrics.RecordPatternDuration(input.Pattern, input.Version, input.Duration.Seconds())
	}

	// Record agent executions
	if input.AgentCount > 0 {
		metrics.RecordAgentExecution(input.Pattern, input.Version, input.AgentCount)
	}

	// Record token usage
	if input.TokensUsed > 0 {
		metrics.RecordTokenUsage(input.Pattern, input.Version, input.TokensUsed)
	}

	// Record reflection improvement
	if input.Pattern == "reflection" && input.Improved {
		metrics.RecordReflectionImprovement(input.Version)
	}

	// Record workflow version
	if input.WorkflowType != "" {
		metrics.RecordWorkflowVersion(input.WorkflowType, input.Version)
	}

	return nil
}
