// =============================================================================
// 文件: go/orchestrator/internal/activities/db_client.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   数据库客户端单例 —— 提供 Postgres 连接池给所有 activity 使用。
// 【关键内容】
//   GetDB / 连接池配置与生命周期管理
// 【协作关系】
//   被所有需要数据库访问的 activity 复用，统一管理连接资源。
// =============================================================================
package activities

import (
	"sync"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/db"
)

var (
	globalDBClient *db.Client
	dbClientMutex  sync.RWMutex
)

// SetGlobalDBClient sets the global database client for use by activities
// This should be called once during application initialization
func SetGlobalDBClient(client *db.Client) {
	dbClientMutex.Lock()
	defer dbClientMutex.Unlock()
	globalDBClient = client
}

// GetGlobalDBClient returns the global database client
// Returns nil if not initialized
func GetGlobalDBClient() *db.Client {
	dbClientMutex.RLock()
	defer dbClientMutex.RUnlock()
	return globalDBClient
}
