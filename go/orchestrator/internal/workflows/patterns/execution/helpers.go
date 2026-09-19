// =============================================================================
// 文件: go/orchestrator/internal/workflows/patterns/execution/helpers.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   执行模式公共 helper，提供各 executor 复用的辅助函数。
// 【关键内容】
//   - 结果合并/格式化/超时控制等通用工具
// 【协作关系】
//   被 sequential/parallel/hybrid 三种 executor 共同引用以减少重复代码。
// =============================================================================
package execution

import (
	"strings"
	"time"
)

// agentStartToCloseTimeout extracts timeout from context with fallback to default
func agentStartToCloseTimeout(ctx map[string]interface{}, defaultTimeout time.Duration) time.Duration {
	if ctx == nil {
		return defaultTimeout
	}
	if v, ok := ctx["human_in_loop"]; ok {
		switch t := v.(type) {
		case bool:
			if t {
				return 48 * time.Hour
			}
		case string:
			if strings.EqualFold(t, "true") {
				return 48 * time.Hour
			}
		}
	}
	return defaultTimeout
}
