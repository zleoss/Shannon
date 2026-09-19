// =============================================================================
// 文件: go/orchestrator/internal/workflows/control_signals.go
// -----------------------------------------------------------------------------
// 【一句话功能】 控制信号处理器别名，转发到 internal/workflows/control
// 【关键内容】 ControlSignalHandler 类型别名
// 【协作关系】 简化外部引用路径，实际实现在 control 子包
// =============================================================================
package workflows

import (
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows/control"
)

// ControlSignalHandler is an alias for control.SignalHandler
type ControlSignalHandler = control.SignalHandler
