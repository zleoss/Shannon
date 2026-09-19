// =============================================================================
// 文件: go/orchestrator/cmd/gateway/internal/openai/translator.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   请求/响应翻译器 —— 在 OpenAI 格式与 Shannon 内部格式之间双向转换。
// 【关键内容】
//   OpenAI -> Shannon 任务请求、Shannon -> OpenAI 响应
// 【协作关系】
//   handler.go 的入口/出口管线核心组件，完成协议转换。
// =============================================================================
package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	commonpb "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/pb/common"
	orchpb "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/pb/orchestrator"
	"google.golang.org/protobuf/types/known/structpb"
)

// TranslatedRequest contains the Shannon-native request and metadata.
type TranslatedRequest struct {
	GRPCRequest *orchpb.SubmitTaskRequest
	SessionID   string
	ModelName   string
	Stream      bool
}

// Translator converts OpenAI requests to Shannon format.
type Translator struct {
	registry *Registry
}

// NewTranslator creates a new request translator.
func NewTranslator(registry *Registry) *Translator {
	return &Translator{registry: registry}
}

// Translate converts an OpenAI ChatCompletionRequest to a Shannon SubmitTaskRequest.
func (t *Translator) Translate(req *ChatCompletionRequest, userID, tenantID string) (*TranslatedRequest, error) {
	// Validate request
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages array is required")
	}

	// Get model configuration
	modelName := req.Model
	if modelName == "" {
		modelName = t.registry.GetDefaultModel()
	}

	modelConfig, err := t.registry.GetModel(modelName)
	if err != nil {
		return nil, fmt.Errorf("invalid model: %s", modelName)
	}

	// Extract the query from messages
	query := t.extractQuery(req.Messages)
	if query == "" {
		return nil, fmt.Errorf("no user message found in messages array")
	}

	// Generate or derive session ID
	sessionID := t.deriveSessionID(req, userID)

	// Build context map from model config + request parameters
	ctxMap := t.buildContext(req, modelConfig)

	// Sanitize context for structpb compatibility (converts []string to []interface{}, etc.)
	sanitizedCtx := sanitizeForStructpb(ctxMap)

	// Convert context to protobuf struct
	ctxStruct, err := structpb.NewStruct(sanitizedCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to build context: %w", err)
	}

	// Build labels for mode routing
	labels := map[string]string{}
	// If context.agent is present, use "standard" mode to route through OrchestratorWorkflow
	// which has the AgentWorkflow routing logic
	if _, hasAgent := ctxMap["agent"].(string); hasAgent {
		labels["mode"] = "standard"
	} else {
		switch modelConfig.WorkflowMode {
		case "simple":
			labels["mode"] = "simple"
		case "research":
			labels["mode"] = "standard" // Research uses standard routing with force_research
		case "supervisor":
			labels["mode"] = "supervisor"
		default:
			labels["mode"] = "simple"
		}
	}

	// Build the gRPC request
	grpcReq := &orchpb.SubmitTaskRequest{
		Metadata: &commonpb.TaskMetadata{
			UserId:    userID,
			TenantId:  tenantID,
			SessionId: sessionID,
			Labels:    labels,
		},
		Query:   query,
		Context: ctxStruct,
	}

	return &TranslatedRequest{
		GRPCRequest: grpcReq,
		SessionID:   sessionID,
		ModelName:   modelName,
		Stream:      req.Stream,
	}, nil
}

// TranslateWithSession converts an OpenAI request using an existing session result.
func (t *Translator) TranslateWithSession(req *ChatCompletionRequest, userID, tenantID string, session *SessionResult) (*TranslatedRequest, error) {
	// Validate request
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("messages array is required")
	}

	// Get model configuration
	modelName := req.Model
	if modelName == "" {
		modelName = t.registry.GetDefaultModel()
	}

	modelConfig, err := t.registry.GetModel(modelName)
	if err != nil {
		return nil, fmt.Errorf("invalid model: %s", modelName)
	}

	// Extract the query from messages
	query := t.extractQuery(req.Messages)
	if query == "" {
		return nil, fmt.Errorf("no user message found in messages array")
	}

	// Use session from session manager
	sessionID := ""
	if session != nil {
		sessionID = session.ShannonSession
	}
	if sessionID == "" {
		sessionID = t.deriveSessionID(req, userID)
	}

	// Build context map from model config + request parameters
	ctxMap := t.buildContext(req, modelConfig)

	// Sanitize context for structpb compatibility (converts []string to []interface{}, etc.)
	sanitizedCtx := sanitizeForStructpb(ctxMap)

	// Convert context to protobuf struct
	ctxStruct, err := structpb.NewStruct(sanitizedCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to build context: %w", err)
	}

	// Build labels for mode routing
	labels := map[string]string{}
	// If context.agent is present, use "standard" mode to route through OrchestratorWorkflow
	// which has the AgentWorkflow routing logic
	if _, hasAgent := ctxMap["agent"].(string); hasAgent {
		labels["mode"] = "standard"
	} else {
		switch modelConfig.WorkflowMode {
		case "simple":
			labels["mode"] = "simple"
		case "research":
			labels["mode"] = "standard"
		case "supervisor":
			labels["mode"] = "supervisor"
		default:
			labels["mode"] = "simple"
		}
	}

	// Build the gRPC request
	grpcReq := &orchpb.SubmitTaskRequest{
		Metadata: &commonpb.TaskMetadata{
			UserId:    userID,
			TenantId:  tenantID,
			SessionId: sessionID,
			Labels:    labels,
		},
		Query:   query,
		Context: ctxStruct,
	}

	return &TranslatedRequest{
		GRPCRequest: grpcReq,
		SessionID:   sessionID,
		ModelName:   modelName,
		Stream:      req.Stream,
	}, nil
}

// extractQuery extracts the query from the messages array.
// Uses the last user message as the primary query.
// Accepts messages with text content OR attachments (attachment-only is valid).
func (t *Translator) extractQuery(messages []ChatMessage) string {
	// Find the last user message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			if strings.TrimSpace(messages[i].Content) != "" {
				return messages[i].Content
			}
			// Attachment-only message: use attachment summary as query
			if len(messages[i].RawAttachments) > 0 {
				if messages[i].ContentWithAttachmentSummary != "" {
					return messages[i].ContentWithAttachmentSummary
				}
				return "[User sent file attachments]"
			}
		}
	}
	return ""
}

// deriveSessionID generates a session ID from the conversation.
// Uses hash of authenticated userID + system message + first user message for consistency.
// userID is included to prevent cross-user session collision.
func (t *Translator) deriveSessionID(req *ChatCompletionRequest, userID string) string {
	// If user provided a custom user ID, combine with auth user for isolation
	if req.User != "" {
		if userID != "" {
			return "openai-" + userID + "-" + req.User
		}
		return "openai-" + req.User
	}

	// Build a hash from authenticated user + conversation start
	var parts []string

	// Include authenticated user ID for cross-user isolation
	if userID != "" {
		parts = append(parts, userID)
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			parts = append(parts, msg.Content)
			break
		}
	}
	// Add first user message (use attachment summary for attachment-only messages)
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			content := msg.Content
			if content == "" && msg.ContentWithAttachmentSummary != "" {
				content = msg.ContentWithAttachmentSummary
			}
			if len(content) > 100 {
				content = content[:100]
			}
			if content != "" {
				parts = append(parts, content)
			}
			break
		}
	}

	if len(parts) == 0 {
		// Fallback: generate unique session
		return "openai-" + GenerateCompletionID()
	}

	// Hash the parts
	h := sha256.New()
	h.Write([]byte(strings.Join(parts, "|")))
	hash := hex.EncodeToString(h.Sum(nil))[:16]
	return "openai-" + hash
}

// buildContext creates the Shannon context map from request parameters.
func (t *Translator) buildContext(req *ChatCompletionRequest, modelConfig *ModelConfig) map[string]interface{} {
	ctx := make(map[string]interface{})

	// Copy model config context
	for k, v := range modelConfig.Context {
		ctx[k] = v
	}

	// Apply shannon_options if present (these take precedence)
	if req.ShannonOptions != nil {
		// Copy context from shannon_options
		for k, v := range req.ShannonOptions.Context {
			ctx[k] = v
		}

		// Agent routing (single-purpose deterministic agents)
		if req.ShannonOptions.Agent != "" {
			ctx["agent"] = req.ShannonOptions.Agent
		}

		// Agent input (can be provided with explicit agent or via model-based agent routing)
		if req.ShannonOptions.AgentInput != nil {
			ctx["agent_input"] = req.ShannonOptions.AgentInput
		}

		// Role routing (workflow roles like browser_use)
		if req.ShannonOptions.Role != "" {
			ctx["role"] = req.ShannonOptions.Role
		}

		// Research strategy
		if req.ShannonOptions.ResearchStrategy != "" {
			ctx["research_strategy"] = req.ShannonOptions.ResearchStrategy
		}

		// Model tier override
		if req.ShannonOptions.ModelTier != "" {
			ctx["model_tier"] = req.ShannonOptions.ModelTier
		}
	}

	// Apply research strategy preset defaults for chat-completion research requests.
	applyShannonResearchPreset(ctx)

	// Apply max_tokens
	if req.MaxTokens > 0 {
		maxLimit := t.registry.GetMaxTokensLimit()
		if req.MaxTokens > maxLimit {
			ctx["max_tokens"] = maxLimit
		} else {
			ctx["max_tokens"] = req.MaxTokens
		}
	} else if modelConfig.MaxTokensDefault > 0 {
		ctx["max_tokens"] = modelConfig.MaxTokensDefault
	}

	// Apply temperature (pointer allows distinguishing "not set" from 0)
	if req.Temperature != nil {
		ctx["temperature"] = *req.Temperature
	}

	// Apply top_p (pointer allows distinguishing "not set" from 0)
	if req.TopP != nil {
		ctx["top_p"] = *req.TopP
	}

	// Apply stop sequences
	if len(req.Stop) > 0 {
		ctx["stop"] = req.Stop
	}

	// Apply user ID for tracking
	if req.User != "" {
		ctx["openai_user"] = req.User
	}

	// Build system prompt from conversation context
	systemPrompt := t.extractSystemPrompt(req.Messages)
	if systemPrompt != "" {
		ctx["system_prompt"] = systemPrompt
	}

	// Include conversation history for context (except last user message which is the query)
	history := t.buildConversationHistory(req.Messages)
	if len(history) > 0 {
		ctx["conversation_history"] = history
	}

	return ctx
}

// applyShannonResearchPreset seeds model_tier defaults for research requests
// arriving via chat completions. Mirrors the task handler's applyStrategyPreset
// logic: force_research without explicit strategy defaults to "standard".
func applyShannonResearchPreset(ctx map[string]interface{}) {
	if ctx == nil {
		return
	}

	// Default force_research to "standard" strategy when none specified
	// (matches task.go:340-344 behavior)
	rs, rsOk := ctx["research_strategy"].(string)
	if !rsOk || strings.TrimSpace(rs) == "" {
		if forceResearch, _ := ctx["force_research"].(bool); forceResearch {
			rs = "standard"
			ctx["research_strategy"] = rs
		}
	}

	if strings.TrimSpace(rs) == "" {
		return
	}
	if _, hasTier := ctx["model_tier"]; hasTier {
		return
	}

	switch strings.ToLower(strings.TrimSpace(rs)) {
	case "quick", "standard":
		ctx["model_tier"] = "small"
	case "deep", "academic":
		ctx["model_tier"] = "medium"
	}
}

// extractSystemPrompt extracts the system prompt from messages.
func (t *Translator) extractSystemPrompt(messages []ChatMessage) string {
	for _, msg := range messages {
		if msg.Role == "system" {
			return msg.Content
		}
	}
	return ""
}

// buildConversationHistory formats previous messages for context.
func (t *Translator) buildConversationHistory(messages []ChatMessage) []map[string]string {
	var history []map[string]string

	// Skip system messages and the last user message (which is the query)
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	for i, msg := range messages {
		if msg.Role == "system" {
			continue // System prompt handled separately
		}
		if i == lastUserIdx {
			continue // Last user message is the query
		}
		// Use attachment-enriched content for history so downstream sees
		// "[Attached: ...]" summaries (from Task 2 content parsing).
		content := msg.Content
		if msg.ContentWithAttachmentSummary != "" {
			content = msg.ContentWithAttachmentSummary
		}
		history = append(history, map[string]string{
			"role":    msg.Role,
			"content": content,
		})
	}

	return history
}

// min returns the minimum of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sanitizeForStructpb converts values to types compatible with structpb.NewStruct.
// structpb.NewStruct only accepts:
// - nil, bool, int/float64, string
// - map[string]interface{} (recursively sanitized)
// - []interface{} (NOT []string, []int, etc.)
func sanitizeForStructpb(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = sanitizeValue(v)
	}
	return result
}

// sanitizeValue converts a single value to structpb-compatible type.
func sanitizeValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case bool, int, int32, int64, float32, float64, string:
		return val
	case []interface{}:
		// Already correct type, but sanitize elements
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = sanitizeValue(elem)
		}
		return result
	case map[string]interface{}:
		// Recursively sanitize nested maps
		return sanitizeForStructpb(val)
	default:
		// Use reflection for typed slices and maps
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			return sanitizeSliceValue(rv)
		case reflect.Map:
			return sanitizeMapValue(rv)
		default:
			// For other types, return as-is and let structpb handle/reject
			return v
		}
	}
}

// sanitizeSliceValue converts typed slices ([]string, []int, etc.) to []interface{}.
func sanitizeSliceValue(rv reflect.Value) []interface{} {
	length := rv.Len()
	result := make([]interface{}, length)
	for i := 0; i < length; i++ {
		result[i] = sanitizeValue(rv.Index(i).Interface())
	}
	return result
}

// sanitizeMapValue converts typed maps (map[string]string, etc.) to map[string]interface{}.
func sanitizeMapValue(rv reflect.Value) map[string]interface{} {
	if rv.Type().Key().Kind() != reflect.String {
		// structpb only supports string keys
		return nil
	}
	result := make(map[string]interface{}, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		key := iter.Key().String()
		result[key] = sanitizeValue(iter.Value().Interface())
	}
	return result
}
