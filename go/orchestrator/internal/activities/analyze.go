// =============================================================================
// 文件: go/orchestrator/internal/activities/analyze.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   分析 activity —— 对输入数据进行简要分析与分类。
// 【关键内容】
//   AnalyzeInput / 任务类型识别
// 【协作关系】
//   在分解阶段前被调用，协助确定任务的复杂度与类型。
// =============================================================================
package activities

import (
	"context"
)

// AnalyzeComplexity is a legacy compatibility shim used by older workflow histories.
// It returns a DecompositionResult that includes mode/complexity and optional subtasks.
// Implementation delegates to DecomposeTask to avoid duplicating logic.
func (a *Activities) AnalyzeComplexity(ctx context.Context, in DecompositionInput) (DecompositionResult, error) {
	// Delegate to DecomposeTask to keep behavior consistent.
	return a.DecomposeTask(ctx, in)
}
