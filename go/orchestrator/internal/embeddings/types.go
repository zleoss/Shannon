// =============================================================================
// 文件: go/orchestrator/internal/embeddings/types.go
// -----------------------------------------------------------------------------
// 【一句话功能】 Embedding 服务的全局配置类型定义
// 【关键内容】 Config 包含 BaseURL、DefaultModel、缓存/Redis/分块等参数
// 【协作关系】 被 service.go、cache.go 和 chunking.go 引用
// =============================================================================
package embeddings

import "time"

// Config controls the embedding service behavior
type Config struct {
	// BaseURL points to the LLM service providing /embeddings
	BaseURL string
	// DefaultModel is the default embedding model (e.g., text-embedding-3-small)
	DefaultModel string
	// Timeout for outbound HTTP calls
	Timeout time.Duration
	// EnableRedis enables Redis-backed cache (optional)
	EnableRedis bool
	// RedisAddr in host:port form when EnableRedis is true
	RedisAddr string
	// CacheTTL sets TTL for embedding cache entries
	CacheTTL time.Duration
	// MaxLRU controls in-process LRU size
	MaxLRU int
	// Chunking configuration for long texts
	Chunking ChunkingConfig
}
