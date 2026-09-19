// =============================================================================
// 文件: go/orchestrator/internal/attachments/store.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   附件存储 —— 上传文件到本地或对象存储并返回可访问 URL。
// 【关键内容】
//   StoreAttachment / GetAttachment / DeleteAttachment
// 【协作关系】
//   被附件相关的 handler 和 activity 调用，统一管理附件生命周期。
// =============================================================================
package attachments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"log"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var ErrAttachmentNotFound = errors.New("attachment not found or expired")

// Attachment represents a stored file attachment.
type Attachment struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	MediaType string `json:"media_type"`
	Filename  string `json:"filename"`
	Data      []byte `json:"data"`
	SizeBytes int    `json:"size_bytes"`
}

// Store manages attachment storage in Redis.
type Store struct {
	client *redis.Client
	ttl    time.Duration
}

// NewStore creates a new attachment store backed by Redis.
func NewStore(client *redis.Client, ttl time.Duration) *Store {
	return &Store{client: client, ttl: ttl}
}

func (s *Store) redisKey(id string) string {
	return fmt.Sprintf("shannon:att:%s", id)
}

// Put stores an attachment and returns its ID.
func (s *Store) Put(ctx context.Context, sessionID string, data []byte, mediaType, filename string) (string, error) {
	id := uuid.New().String()[:12]
	att := Attachment{
		ID:        id,
		SessionID: sessionID,
		MediaType: mediaType,
		Filename:  filepath.Base(filename),
		Data:      data,
		SizeBytes: len(data),
	}
	val, err := json.Marshal(att)
	if err != nil {
		return "", fmt.Errorf("marshal attachment: %w", err)
	}
	if err := s.client.Set(ctx, s.redisKey(id), val, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("redis set: %w", err)
	}
	return id, nil
}

// Get retrieves an attachment and refreshes its TTL.
// When sessionID is non-empty, it is validated against the stored record
// to prevent cross-session attachment access.
func (s *Store) Get(ctx context.Context, id string, sessionID ...string) (*Attachment, error) {
	key := s.redisKey(id)
	val, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrAttachmentNotFound
		}
		return nil, fmt.Errorf("redis get: %w", err)
	}
	// Refresh TTL on access
	if err := s.client.Expire(ctx, key, s.ttl).Err(); err != nil {
		log.Printf("WARN: failed to refresh TTL for attachment %s: %v", id, err)
	}

	var att Attachment
	if err := json.Unmarshal(val, &att); err != nil {
		return nil, fmt.Errorf("unmarshal attachment: %w", err)
	}

	// Session isolation: when a sessionID is provided, it MUST match the stored record.
	// Both sides must be non-empty for the check to apply — internal callers that
	// legitimately need to bypass (e.g., admin/cleanup) simply omit the parameter.
	if len(sessionID) > 0 && sessionID[0] != "" {
		if att.SessionID != "" && att.SessionID != sessionID[0] {
			return nil, ErrAttachmentNotFound // don't leak existence
		}
	}
	return &att, nil
}

// Delete removes an attachment.
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.client.Del(ctx, s.redisKey(id)).Err()
}
