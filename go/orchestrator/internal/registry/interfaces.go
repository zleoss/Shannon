// =============================================================================
// 文件: go/orchestrator/internal/registry/interfaces.go
// -----------------------------------------------------------------------------
// 【一句话功能】 工作流/活动注册器接口定义
// 【关键内容】 WorkflowRegistrar / ActivityRegistrar / Registry 接口；RegistryConfig 配置
// 【协作关系】 被 main.go 用于统一注册所有工作流和活动到 Temporal Worker
// =============================================================================
package registry

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/daemon"
)

// WorkflowRegistrar defines the interface for registering workflows
type WorkflowRegistrar interface {
	RegisterWorkflows(w worker.Worker) error
}

// ActivityRegistrar defines the interface for registering activities
type ActivityRegistrar interface {
	RegisterActivities(w worker.Worker) error
}

// Registry combines both workflow and activity registration
type Registry interface {
	WorkflowRegistrar
	ActivityRegistrar
}

// RegistryConfig holds configuration for the registry
type RegistryConfig struct {
	// EnableBudgetedWorkflows controls whether budget-aware workflows are registered
	EnableBudgetedWorkflows bool
	// EnableStreamingWorkflows controls whether streaming workflows are registered
	EnableStreamingWorkflows bool
	// EnableApprovalWorkflows controls whether human approval workflows are registered
	EnableApprovalWorkflows bool
	// Optional typed-config defaults for budgets (tokens)
	DefaultTaskBudget    int
	DefaultSessionBudget int

	// Temporal client for schedule management (pause/resume from activities)
	TemporalClient client.Client

	// Daemon Hub for dispatching scheduled tasks to connected daemons (nil = skip registration)
	DaemonHub *daemon.Hub
}
