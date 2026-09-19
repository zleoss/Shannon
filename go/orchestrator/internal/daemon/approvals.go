// =============================================================================
// 文件: go/orchestrator/internal/daemon/approvals.go
// -----------------------------------------------------------------------------
// 【一句话功能】 Daemon 审批请求的 Redis 存储与原子解析管理
// 【关键内容】 ApprovalManager 提供 Store/Get/Resolve/CancelByConn；Redis Lua 脚本保证原子性
// 【协作关系】 被 conn_handler.go 在审批请求处理中调用
// =============================================================================
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	approvalKeyPrefix = "shannon:daemon:approval:"
	approvalTTL       = 5 * time.Minute
)

var ErrApprovalNotFound = errors.New("approval not found")
var ErrApprovalAlreadyResolved = errors.New("approval already resolved")

// PendingApproval represents an approval request broadcast to channels.
type PendingApproval struct {
	RequestID   string            `json:"request_id"`
	ConnID      string            `json:"conn_id"`
	TenantID    string            `json:"tenant_id"`
	UserID      string            `json:"user_id"`
	Agent       string            `json:"agent"`
	Tool        string            `json:"tool"`
	Args        string            `json:"args"`
	ThreadID    string            `json:"thread_id,omitempty"`    // Slack/LINE thread context for in-thread approvals
	ChannelMsgs map[string]string `json:"channel_msgs,omitempty"` // channelID -> platform message ID (for dismissal)
	CreatedAt   string            `json:"created_at"`
}

// ApprovalManager manages pending approval requests in Redis.
// Stateless — safe for multi-instance Cloud deployments.
type ApprovalManager struct {
	rc redis.Cmdable
}

func NewApprovalManager(rc redis.Cmdable) *ApprovalManager {
	return &ApprovalManager{rc: rc}
}

func approvalKey(requestID string) string {
	return approvalKeyPrefix + requestID
}

// Store saves a pending approval with a 5-minute TTL.
func (am *ApprovalManager) Store(ctx context.Context, approval PendingApproval) error {
	data, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	return am.rc.Set(ctx, approvalKey(approval.RequestID), data, approvalTTL).Err()
}

// Get retrieves a pending approval without removing it.
func (am *ApprovalManager) Get(ctx context.Context, requestID string) (PendingApproval, error) {
	data, err := am.rc.Get(ctx, approvalKey(requestID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return PendingApproval{}, ErrApprovalNotFound
		}
		return PendingApproval{}, err
	}
	var approval PendingApproval
	if err := json.Unmarshal(data, &approval); err != nil {
		return PendingApproval{}, err
	}
	return approval, nil
}

// Resolve atomically retrieves and deletes a pending approval.
// Returns ErrApprovalNotFound if already resolved or expired (first-response-wins).
func (am *ApprovalManager) Resolve(ctx context.Context, requestID string) (PendingApproval, error) {
	// Lua script: atomic GET + DEL (first caller wins, subsequent get nil)
	script := redis.NewScript(`
		local data = redis.call("GET", KEYS[1])
		if not data then return nil end
		redis.call("DEL", KEYS[1])
		return data
	`)
	val, err := script.Run(ctx, am.rc, []string{approvalKey(requestID)}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return PendingApproval{}, ErrApprovalNotFound
		}
		return PendingApproval{}, err
	}
	data, ok := val.(string)
	if !ok {
		return PendingApproval{}, ErrApprovalNotFound
	}
	var approval PendingApproval
	if err := json.Unmarshal([]byte(data), &approval); err != nil {
		return PendingApproval{}, err
	}
	return approval, nil
}

// UpdateChannelMsg adds a platform message ID to a pending approval (for later dismissal).
func (am *ApprovalManager) UpdateChannelMsg(ctx context.Context, requestID, channelID, platformMsgID string) error {
	approval, err := am.Get(ctx, requestID)
	if err != nil {
		return err
	}
	if approval.ChannelMsgs == nil {
		approval.ChannelMsgs = make(map[string]string)
	}
	approval.ChannelMsgs[channelID] = platformMsgID

	data, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	// Re-set with remaining TTL
	ttl, err := am.rc.TTL(ctx, approvalKey(requestID)).Result()
	if err != nil || ttl <= 0 {
		ttl = approvalTTL
	}
	return am.rc.Set(ctx, approvalKey(requestID), data, ttl).Err()
}

// CancelByConn removes all pending approvals for a specific connection (daemon disconnect cleanup).
func (am *ApprovalManager) CancelByConn(ctx context.Context, connID string) ([]string, error) {
	var cancelled []string
	iter := am.rc.Scan(ctx, 0, approvalKeyPrefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		data, err := am.rc.Get(ctx, key).Bytes()
		if err != nil {
			continue
		}
		var approval PendingApproval
		if err := json.Unmarshal(data, &approval); err != nil {
			continue
		}
		if approval.ConnID == connID {
			am.rc.Del(ctx, key)
			cancelled = append(cancelled, approval.RequestID)
		}
	}
	if err := iter.Err(); err != nil {
		return cancelled, fmt.Errorf("scan approvals: %w", err)
	}
	return cancelled, nil
}
