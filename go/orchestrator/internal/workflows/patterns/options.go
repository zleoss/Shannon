// =============================================================================
// 文件: go/orchestrator/internal/workflows/patterns/options.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   定义各认知模式共享的公共配置选项结构体 Options。
// 【关键内容】
//   - Options: 预算、并发、迭代上限等模式通用参数
// 【协作关系】
//   被各模式实现（react/tot/debate 等）作为执行参数透传与读取。
// =============================================================================
package patterns

// Options provides common configuration for pattern execution
type Options struct {
	BudgetAgentMax int                    // Per-agent token budget
	SessionID      string                 // Session identifier
	UserID         string                 // User identifier for budget/recording
	EmitEvents     bool                   // Whether to emit streaming events
	ModelTier      string                 // Model tier (small/medium/large)
	Context        map[string]interface{} // Additional context
}

// ReflectionConfig controls reflection behavior
type ReflectionConfig struct {
	Enabled             bool     // Whether reflection is enabled
	MaxRetries          int      // Maximum reflection iterations
	ConfidenceThreshold float64  // Minimum acceptable quality score
	Criteria            []string // Evaluation criteria
	TimeoutMs           int      // Timeout per reflection attempt
}
