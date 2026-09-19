// =============================================================================
// 文件: go/orchestrator/internal/vectordb/types.go
// -----------------------------------------------------------------------------
// 【一句话功能】 向量数据库的配置、查询结果和 Upsert 数据类型定义
// 【关键内容】 Config 包含 Qdrant 连接/集合/搜索/MMR 参数；ContextItem / SimilarQuery / UpsertItem 等模型
// 【协作关系】 被 vectordb 包内所有文件引用
// =============================================================================
package vectordb

import "time"

// Config controls Qdrant client behavior
type Config struct {
	Enabled bool
	Host    string
	Port    int
	// Collections
	TaskEmbeddings string
	ToolResults    string
	Cases          string
	DocumentChunks string
	Summaries      string
	// Search params
	TopK      int
	Threshold float64
	Timeout   time.Duration
	// Validation
	ExpectedEmbeddingDim int // Expected embedding dimension (e.g., 1536 for OpenAI ada-002)
	// MMR (diversity) re-ranking
	MMREnabled        bool
	MMRLambda         float64
	MMRPoolMultiplier int
}

// SimilarQuery represents a similar query with metadata
type SimilarQuery struct {
	Query      string    `json:"query"`
	Outcome    string    `json:"outcome"`
	Confidence float64   `json:"confidence"`
	Timestamp  time.Time `json:"timestamp"`
}

// ContextItem represents context retrieved for a session
type ContextItem struct {
	SessionID string                 `json:"session_id"`
	Payload   map[string]interface{} `json:"payload"`
	Score     float64                `json:"score"`
	// Optional candidate vector when fetched with vectors (for MMR)
	Vector []float32 `json:"-"`
}

// UpsertItem represents a single point to insert into Qdrant
type UpsertItem struct {
	ID      interface{}            `json:"id,omitempty"`
	Vector  []float32              `json:"vector"`
	Payload map[string]interface{} `json:"payload"`
}

// UpsertResponse captures basic Qdrant upsert response
type UpsertResponse struct {
	Status string  `json:"status"`
	Time   float64 `json:"time"`
}
