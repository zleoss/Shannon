// =============================================================================
// 文件: go/orchestrator/internal/workflows/cognitive_wrappers.go
// -----------------------------------------------------------------------------
// 【一句话功能】 认知策略工作流的封装桥接，保持 TaskInput/TaskResult 接口兼容
// 【关键内容】 ExploratoryWorkflow / ScientificWorkflow 委托子工作流执行
// 【协作关系】 桥接 workflows 包和 strategies 子包的类型系统
// =============================================================================
package workflows

import (
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/workflows/strategies"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"
)

// ExploratoryWorkflow is a wrapper for strategies.ExploratoryWorkflow
func ExploratoryWorkflow(ctx workflow.Context, input TaskInput) (TaskResult, error) {
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		ParentClosePolicy: enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	})
	strategiesInput := convertToStrategiesInput(input)
	var strategiesResult strategies.TaskResult
	err := workflow.ExecuteChildWorkflow(childCtx, strategies.ExploratoryWorkflow, strategiesInput).Get(childCtx, &strategiesResult)
	if err != nil {
		return TaskResult{}, err
	}
	return convertFromStrategiesResult(strategiesResult), nil
}

// ScientificWorkflow is a wrapper for strategies.ScientificWorkflow to maintain test compatibility
func ScientificWorkflow(ctx workflow.Context, input TaskInput) (TaskResult, error) {
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		ParentClosePolicy: enums.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	})
	strategiesInput := convertToStrategiesInput(input)
	var strategiesResult strategies.TaskResult
	err := workflow.ExecuteChildWorkflow(childCtx, strategies.ScientificWorkflow, strategiesInput).Get(childCtx, &strategiesResult)
	if err != nil {
		return TaskResult{}, err
	}
	return convertFromStrategiesResult(strategiesResult), nil
}

