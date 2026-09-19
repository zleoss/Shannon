// =============================================================================
// 文件: go/orchestrator/internal/activities/activities.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Activity 注册与类型定义 —— 集中管理所有 Temporal Activity 结构体与方法集。
// 【关键内容】
//   Activities 结构体定义、所有 activity 方法接收器、依赖注入
// 【协作关系】
//   作为 activity 的统一入口，被 worker 注册到 Temporal 并供 workflows 调用。
// =============================================================================
package activities

import (
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/session"
	"go.uber.org/zap"
)

// Activities struct holds dependencies for activities
type Activities struct {
	sessionManager *session.Manager
	logger         *zap.Logger
}

// NewActivities creates a new activities instance with dependencies
func NewActivities(sessionManager *session.Manager, logger *zap.Logger) *Activities {
	return &Activities{
		sessionManager: sessionManager,
		logger:         logger,
	}
}
