// =============================================================================
// 文件: go/orchestrator/internal/daemon/conn_handler.go
// -----------------------------------------------------------------------------
// 【一句话功能】 Daemon WebSocket 连接生命周期管理，处理消息路由/认领/审批/主动推送
// 【关键内容】 HandleConnection 主循环；handleDaemonMessage 消息分发；流式事件转发
// 【协作关系】 依赖 Hub 连接管理和 streaming.Manager 事件发布
// =============================================================================
package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/streaming"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// daemonStreamTracker tracks initialization state of daemon event streams.
type daemonStreamTracker struct {
	initState *sync.Map // Key: messageID, Value: bool (true=initialized, false=failed)
}

func (t *daemonStreamTracker) isInitialized(messageID string) bool {
	v, ok := t.initState.Load(messageID)
	if !ok {
		return false
	}
	return v.(bool)
}

func (t *daemonStreamTracker) cleanup(messageID string) {
	t.initState.Delete(messageID)
}

// eventPublisher abstracts streaming.Manager.Publish for testability.
type eventPublisher interface {
	Publish(workflowID string, evt streaming.Event)
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 20 * time.Second
)

// ReplyCallback is called when a daemon sends a reply. The Hub calls this
// to route replies back to the originating channel.
type ReplyCallback func(ctx context.Context, meta ClaimMetadata, reply ReplyPayload)

// ApprovalRequestCallback is called when a daemon sends an approval request
// that needs to be broadcast to channels (Slack buttons, LINE flex).
type ApprovalRequestCallback func(ctx context.Context, connID, tenantID, userID string, req DaemonApprovalRequest)

// ApprovalResolvedCallback is called when a daemon reports that an approval
// was resolved locally (e.g. by ShanClaw). Cloud should dismiss channel messages.
type ApprovalResolvedCallback func(ctx context.Context, resolved DaemonApprovalResolved)

// DisconnectCallback is called when a daemon connection is closed, with the conn ID.
type DisconnectCallback func(ctx context.Context, connID string)

// ProactiveCallback is called when a daemon sends an unsolicited message
// to be delivered to all channels mapped to the named agent.
type ProactiveCallback func(ctx context.Context, connID, tenantID, userID string, payload ProactivePayload) error

// ConnCallbacks groups all callbacks for daemon message handling.
type ConnCallbacks struct {
	OnReply            ReplyCallback
	OnApprovalRequest  ApprovalRequestCallback
	OnApprovalResolved ApprovalResolvedCallback
	OnProactive        ProactiveCallback
	OnDisconnect       DisconnectCallback
	StreamTracker      *daemonStreamTracker
}

// HandleConnection runs the read/write loop for a single daemon WebSocket connection.
// Blocks until the connection is closed.
func HandleConnection(ctx context.Context, hub *Hub, ws *websocket.Conn, tenantID, userID string, onReply ReplyCallback, logger *zap.Logger, opts ...func(*ConnCallbacks)) {
	cbs := &ConnCallbacks{OnReply: onReply}
	for _, opt := range opts {
		opt(cbs)
	}
	cbs.StreamTracker = &daemonStreamTracker{initState: &sync.Map{}}
	connID := uuid.New().String()

	conn := &DaemonConn{
		ID:          connID,
		TenantID:    tenantID,
		UserID:      userID,
		ConnectedAt: time.Now(),
		LastActive:  time.Now(),
		sendFn: func(msg ServerMessage) error {
			ws.SetWriteDeadline(time.Now().Add(writeWait))
			return ws.WriteJSON(msg)
		},
	}

	hub.Register(conn)
	defer func() {
		released, _ := hub.HandleDisconnect(ctx, connID)
		if len(released) > 0 {
			logger.Warn("daemon disconnected with active claims",
				zap.String("conn_id", connID),
				zap.Strings("released_messages", released),
			)
		}
		if cbs.OnDisconnect != nil {
			cbs.OnDisconnect(ctx, connID)
		}
		ws.Close()
	}()

	// Send connected confirmation.
	conn.Send(ServerMessage{Type: MsgTypeConnected})

	// Configure WebSocket.
	ws.SetReadLimit(64 * 1024)
	ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Ping ticker.
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	// Read pump in separate goroutine.
	msgCh := make(chan DaemonMessage, 16)
	go func() {
		defer close(msgCh)
		for {
			var msg DaemonMessage
			if err := ws.ReadJSON(&msg); err != nil {
				return
			}
			msgCh <- msg
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			handleDaemonMessage(ctx, hub, conn, msg, cbs, logger)
		case <-ticker.C:
			// Acquire conn mutex — gorilla/websocket requires single-writer serialization.
			// Send() also holds this lock, so pings are serialized with data writes.
			conn.mu.Lock()
			ws.SetWriteDeadline(time.Now().Add(writeWait))
			err := ws.WriteMessage(websocket.PingMessage, nil)
			conn.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func handleDaemonMessage(ctx context.Context, hub *Hub, conn *DaemonConn, msg DaemonMessage, cbs *ConnCallbacks, logger *zap.Logger) {
	if msg.Type == "" {
		return // Ignore empty-type messages (e.g. keep-alive frames)
	}

	if msg.MessageID == "" && (msg.Type == MsgTypeClaim || msg.Type == MsgTypeReply || msg.Type == MsgTypeProgress || msg.Type == MsgTypeEvent) {
		logger.Warn("daemon message missing message_id", zap.String("type", msg.Type))
		return
	}

	switch msg.Type {
	case MsgTypeClaim:
		granted, err := hub.HandleClaim(ctx, conn.ID, msg.MessageID, ClaimMetadata{
			ConnID:    conn.ID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			logger.Error("claim failed", zap.Error(err), zap.String("message_id", msg.MessageID))
			return
		}
		ackPayload, _ := json.Marshal(ClaimAckPayload{Granted: granted})
		conn.Send(ServerMessage{
			Type:      MsgTypeClaimAck,
			MessageID: msg.MessageID,
			Payload:   ackPayload,
		})

	case MsgTypeProgress:
		var payload *ProgressPayload
		if len(msg.Payload) > 0 {
			var p ProgressPayload
			if err := json.Unmarshal(msg.Payload, &p); err == nil && p.WorkflowID != "" {
				payload = &p
			}
		}
		if err := hub.HandleProgress(ctx, msg.MessageID, payload); err != nil {
			logger.Warn("progress heartbeat failed", zap.Error(err), zap.String("message_id", msg.MessageID))
		}

	case MsgTypeEvent:
		publisher := streaming.Get()
		if cbs.StreamTracker == nil {
			cbs.StreamTracker = &daemonStreamTracker{initState: &sync.Map{}}
		}
		handleDaemonEvent(ctx, hub, msg.MessageID, msg.Payload, publisher, cbs.StreamTracker, logger)

	case MsgTypeReply:
		// If streaming was initialized for this message, publish WORKFLOW_COMPLETED
		// before HandleReply so StreamConsumer processes it before the reply callback
		// tears down the channel context.
		if cbs.StreamTracker != nil {
			streamID := "daemon:" + msg.MessageID
			if cbs.StreamTracker.isInitialized(msg.MessageID) {
				var reply ReplyPayload
				if err := json.Unmarshal(msg.Payload, &reply); err == nil {
					publisher := streaming.Get()
					publisher.Publish(streamID, streaming.Event{
						WorkflowID: streamID,
						Type:       "WORKFLOW_COMPLETED",
						AgentID:    "",
						Message:    reply.Text,
						Payload:    map[string]interface{}{"response": reply.Text},
						Timestamp:  time.Now(),
					})
					// No sleep needed — RouteReply unconditionally suppresses plain-text
					// replies for daemon streams (WorkflowID prefix "daemon:").
				}
			}
			// Always cleanup to prevent sync.Map leak.
			cbs.StreamTracker.cleanup(msg.MessageID)
		}

		meta, err := hub.HandleReply(ctx, msg.MessageID)
		if err != nil {
			logger.Warn("reply for unknown claim", zap.Error(err), zap.String("message_id", msg.MessageID))
			return
		}
		var reply ReplyPayload
		if err := json.Unmarshal(msg.Payload, &reply); err != nil {
			logger.Error("invalid reply payload", zap.Error(err))
			return
		}
		if cbs.OnReply != nil {
			cbs.OnReply(ctx, meta, reply)
		}
		logger.Info("daemon reply received",
			zap.String("message_id", msg.MessageID),
			zap.String("channel_type", meta.ChannelType),
			zap.String("conn_id", conn.ID),
		)

	case MsgTypeDaemonApprovalRequest:
		var req DaemonApprovalRequest
		if err := json.Unmarshal(msg.Payload, &req); err != nil {
			logger.Error("invalid approval_request payload", zap.Error(err))
			return
		}
		// Resolve thread context from the active claim for this message
		if msg.MessageID != "" {
			if meta, err := hub.Claims().GetClaim(ctx, msg.MessageID); err == nil {
				req.ThreadID = meta.ThreadID
				req.ChannelID = meta.ChannelID
			}
		}
		if cbs.OnApprovalRequest != nil {
			cbs.OnApprovalRequest(ctx, conn.ID, conn.TenantID, conn.UserID, req)
		}
		logger.Info("daemon approval request received",
			zap.String("request_id", req.RequestID),
			zap.String("tool", req.Tool),
			zap.String("conn_id", conn.ID),
		)

	case MsgTypeDaemonApprovalResolved:
		var resolved DaemonApprovalResolved
		if err := json.Unmarshal(msg.Payload, &resolved); err != nil {
			logger.Error("invalid approval_resolved payload", zap.Error(err))
			return
		}
		if cbs.OnApprovalResolved != nil {
			cbs.OnApprovalResolved(ctx, resolved)
		}
		logger.Info("daemon approval resolved locally",
			zap.String("request_id", resolved.RequestID),
			zap.String("resolved_by", resolved.ResolvedBy),
			zap.String("conn_id", conn.ID),
		)

	case MsgTypeProactive:
		var payload ProactivePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			logger.Warn("invalid proactive payload", zap.Error(err))
			return
		}
		if payload.AgentName == "" || payload.Text == "" {
			logger.Warn("proactive message missing required fields")
			return
		}
		if cbs.OnProactive != nil {
			if err := cbs.OnProactive(ctx, conn.ID, conn.TenantID, conn.UserID, payload); err != nil {
				logger.Error("proactive delivery failed", zap.Error(err), zap.String("agent", payload.AgentName))
			}
		}
		logger.Info("daemon proactive message received",
			zap.String("agent", payload.AgentName),
			zap.String("conn_id", conn.ID),
		)

	case MsgTypeDisconnect:
		logger.Info("daemon graceful disconnect", zap.String("conn_id", conn.ID))

	default:
		logger.Warn("unknown daemon message type", zap.String("type", msg.Type))
	}
}

// handleDaemonEvent processes a single agent loop event forwarded by the daemon.
// On the first event for a given messageID, it initializes the stream via HandleProgress
// (which triggers OnWorkflowStarted for channel card creation). All events are published
// to the streaming.Manager for progressive card updates.
func handleDaemonEvent(ctx context.Context, hub *Hub, messageID string, rawPayload json.RawMessage, publisher eventPublisher, tracker *daemonStreamTracker, logger *zap.Logger) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if messageID == "" {
		return
	}

	var payload DaemonEventPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		logger.Warn("invalid daemon event payload", zap.Error(err), zap.String("message_id", messageID))
		return
	}

	streamID := "daemon:" + messageID

	// Initialize stream on first event (or retry after previous failure).
	if val, loaded := tracker.initState.Load(messageID); !loaded || !val.(bool) {
		err := hub.HandleProgress(ctx, messageID, &ProgressPayload{WorkflowID: streamID})
		if err != nil {
			tracker.initState.Store(messageID, false)
			logger.Warn("daemon event init failed (HandleProgress)",
				zap.Error(err),
				zap.String("message_id", messageID),
			)
		} else {
			tracker.initState.Store(messageID, true)
		}
	}

	// Preserve daemon timestamp; fall back to now.
	ts := time.Now()
	if payload.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, payload.Timestamp); err == nil {
			ts = parsed
		}
	}

	// Copy daemon data and inject daemon_seq.
	data := payload.Data
	if data == nil {
		data = make(map[string]interface{})
	}
	data["daemon_seq"] = payload.Seq

	publisher.Publish(streamID, streaming.Event{
		WorkflowID: streamID,
		Type:       payload.EventType,
		AgentID:    "", // Must be empty — StreamConsumer's isSynthesisAgent filter drops non-empty for LLM_OUTPUT/LLM_PARTIAL
		Message:    payload.Message,
		Payload:    data,
		Timestamp:  ts,
	})
}
