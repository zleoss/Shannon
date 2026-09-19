// =============================================================================
// 文件: go/orchestrator/internal/workflows/signals.go
// -----------------------------------------------------------------------------
// 【一句话功能】 工作流信号常量和类型的向后兼容性导出
// 【关键内容】 重新导出 control 包中的 SignalPause/Resume/Cancel 和 PauseRequest 等类型
// 【协作关系】 为旧代码提供无缝迁移路径，避免破坏性变更
// =============================================================================
package workflows

import (
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows/control"
)

// Re-export signal names for backward compatibility
const (
	SignalPause       = control.SignalPause
	SignalResume      = control.SignalResume
	SignalCancel      = control.SignalCancel
	QueryControlState = control.QueryControlState
)

// Re-export types for backward compatibility
type (
	PauseRequest         = control.PauseRequest
	ResumeRequest        = control.ResumeRequest
	CancelRequest        = control.CancelRequest
	WorkflowControlState = control.WorkflowControlState
)
