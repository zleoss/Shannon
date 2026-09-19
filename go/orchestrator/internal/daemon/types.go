// =============================================================================
// 文件: go/orchestrator/internal/daemon/types.go
// -----------------------------------------------------------------------------
// 【一句话功能】 Daemon 通信协议的消息类型、常量与数据结构定义
// 【关键内容】 服务端→daemon 和 daemon→服务端消息类型；ServerMessage / DaemonMessage 信封；各类 Payload 结构体
// 【协作关系】 被 daemon 包内所有文件引用，是通信协议的核心契约
// =============================================================================
package daemon

import "encoding/json"

// Server -> Daemon message types
const (
	MsgTypeConnected = "connected"
	MsgTypeMessage   = "message"
	MsgTypeClaimAck  = "claim_ack"
	MsgTypeSystem    = "system"
)

// Daemon -> Server message types
const (
	MsgTypeClaim      = "claim"
	MsgTypeReply      = "reply"
	MsgTypeProgress   = "progress"
	MsgTypeDisconnect = "disconnect"

	// Unified messaging: daemon -> server
	MsgTypeDaemonApprovalRequest  = "approval_request"
	MsgTypeDaemonApprovalResolved = "approval_resolved"

	// Agent loop event forwarding: daemon -> server
	MsgTypeEvent = "event"

	// Proactive messaging: daemon -> server (unsolicited, no prior claim)
	MsgTypeProactive = "proactive"
)

// Server -> Daemon message types (unified messaging)
const (
	MsgTypeApprovalResponse = "approval_response"
)

// Channel types for optional integrations (messaging platforms, schedulers, etc.)
const (
	ChannelSlack    = "slack"
	ChannelLINE     = "line"
	ChannelTeams    = "teams"
	ChannelWeChat   = "wechat"
	ChannelWeb      = "web"
	ChannelSchedule = "schedule"
	ChannelSystem   = "system"
	ChannelFeishu   = "feishu"
)

// Reply format types
const (
	FormatText     = "text"
	FormatMarkdown = "markdown"
)

// ServerMessage is the envelope for all server-to-daemon messages.
type ServerMessage struct {
	Type      string          `json:"type"`
	MessageID string          `json:"message_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// DaemonMessage is the envelope for all daemon-to-server messages.
type DaemonMessage struct {
	Type      string          `json:"type"`
	MessageID string          `json:"message_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// MessagePayload is what the daemon's agent loop processes.
type MessagePayload struct {
	Channel   string `json:"channel"`
	ThreadID  string `json:"thread_id"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	AgentName string `json:"agent_name,omitempty"`
	Source    string `json:"source,omitempty"`
	Timestamp string `json:"timestamp"`
}

// DaemonApprovalRequest is sent by daemon when it needs tool approval.
type DaemonApprovalRequest struct {
	RequestID string `json:"request_id"`
	Agent     string `json:"agent"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	ThreadID  string `json:"thread_id,omitempty"`  // populated by Cloud from active claim
	ChannelID string `json:"channel_id,omitempty"` // populated by Cloud from active claim
}

// DaemonApprovalResolved is sent by daemon when ShanClaw resolves an approval first.
type DaemonApprovalResolved struct {
	RequestID  string `json:"request_id"`
	Decision   string `json:"decision"`
	ResolvedBy string `json:"resolved_by"`
}

// ApprovalResponsePayload is sent from server to daemon with the approval decision.
type ApprovalResponsePayload struct {
	RequestID  string `json:"request_id"`
	Decision   string `json:"decision"`
	ResolvedBy string `json:"resolved_by,omitempty"`
}

// ReplyPayload is sent back after agent completes.
type ReplyPayload struct {
	Channel  string `json:"channel"`
	ThreadID string `json:"thread_id"`
	Text     string `json:"text"`
	Format   string `json:"format,omitempty"`
}

// ClaimAckPayload is sent to confirm or deny a claim.
type ClaimAckPayload struct {
	Granted bool `json:"granted"`
}

// ProgressPayload is an optional payload for progress heartbeats.
// When workflow_id is set, Cloud starts streaming for the associated channel.
type ProgressPayload struct {
	WorkflowID string `json:"workflow_id,omitempty"`
}

// DaemonEventPayload carries a single agent loop event from daemon to server.
// Published to streaming.Manager for channel card updates.
type DaemonEventPayload struct {
	EventType string                 `json:"event_type"` // TOOL_INVOKED, TOOL_COMPLETED, LLM_OUTPUT, LLM_PARTIAL
	Message   string                 `json:"message"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Seq       int64                  `json:"seq"`
	Timestamp string                 `json:"ts"`
}

// ProactivePayload is sent by the daemon to push an unsolicited message
// to all channels mapped to the named agent.
type ProactivePayload struct {
	AgentName string `json:"agent_name"`
	Text      string `json:"text"`
	Format    string `json:"format,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// IsSystemChannel returns true for channels that don't expect agent processing.
func IsSystemChannel(channel string) bool {
	return channel == ChannelSystem
}
