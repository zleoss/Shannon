// =============================================================================
// 文件: go/orchestrator/internal/activities/session_memory.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   会话记忆 activity —— 读写指定会话的上下文记忆。
// 【关键内容】
//   GetSessionMemory / AppendSessionMemory / ClearSessionMemory
// 【协作关系】
//   在 agent 执行周期中被调用，维持同一会话内各步骤的上下文连贯性。
// =============================================================================
package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/metrics"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/streaming"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/vectordb"
	"go.temporal.io/sdk/activity"
)

// FetchSessionMemoryInput requests session-scoped context items
type FetchSessionMemoryInput struct {
	SessionID string `json:"session_id"`
	TenantID  string `json:"tenant_id"`
	TopK      int    `json:"top_k"`
}

// FetchSessionMemoryResult contains retrieved items for merging
type FetchSessionMemoryResult struct {
	Items []map[string]interface{} `json:"items"`
}

// FetchSessionMemory fetches recent items for a session from Qdrant
func FetchSessionMemory(ctx context.Context, in FetchSessionMemoryInput) (FetchSessionMemoryResult, error) {
	vdb := vectordb.Get()
	if vdb == nil || in.SessionID == "" {
		return FetchSessionMemoryResult{Items: nil}, nil
	}
	items, err := vdb.GetSessionContext(ctx, in.SessionID, in.TenantID, in.TopK)
	if err != nil {
		// Record metrics for failed fetch (miss)
		metrics.MemoryFetches.WithLabelValues("session", "qdrant", "miss").Inc()
		metrics.MemoryItemsRetrieved.WithLabelValues("session", "qdrant").Observe(0)
		return FetchSessionMemoryResult{Items: nil}, nil
	}

	// Record metrics based on whether we found items
	if len(items) == 0 {
		metrics.MemoryFetches.WithLabelValues("session", "qdrant", "miss").Inc()
	} else {
		metrics.MemoryFetches.WithLabelValues("session", "qdrant", "hit").Inc()
	}
	metrics.MemoryItemsRetrieved.WithLabelValues("session", "qdrant").Observe(float64(len(items)))

	// Emit a friendly memory retrieval event
	if info := activity.GetInfo(ctx); info.WorkflowExecution.ID != "" {
		wfID := info.WorkflowExecution.ID
		msg := fmt.Sprintf("Looking up past notes (%d found)", len(items))
		streaming.Get().Publish(wfID, streaming.Event{
			WorkflowID: wfID,
			Type:       string(StreamEventProgress),
			AgentID:    "memory",
			Message:    msg,
			Payload:    map[string]interface{}{"operation": "fetch", "hits": len(items)},
			Timestamp:  time.Now(),
		})
	}

	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		out = append(out, it.Payload)
	}
	return FetchSessionMemoryResult{Items: out}, nil
}
