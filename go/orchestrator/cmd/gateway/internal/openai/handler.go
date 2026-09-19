// =============================================================================
// 文件: go/orchestrator/cmd/gateway/internal/openai/handler.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   OpenAI 兼容 API 主入口 —— ChatCompletions(:84) / Completions(:644) / ListModels / GetModel。
// 【关键内容】
//   将 OpenAI 格式请求转为 Shannon 内部任务，经 orchestrator 处理后返回兼容响应。
// 【协作关系】
//   入口端点 -> translator 转换 -> orchestrator 处理 -> streamer 流式回写
// =============================================================================
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/attachments"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/auth"
	orchpb "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/pb/orchestrator"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)


// Handler handles OpenAI-compatible API requests.
type Handler struct {
	orchClient     orchpb.OrchestratorServiceClient
	db             *sqlx.DB
	redis          *redis.Client
	logger         *zap.Logger
	registry       *Registry
	translator     *Translator
	sessionManager *SessionManager
	adminURL      string // URL for SSE streaming (e.g., "http://orchestrator:8081")
	llmServiceURL string
	attStore       *attachments.Store
}

// NewHandler creates a new OpenAI API handler.
func NewHandler(
	orchClient orchpb.OrchestratorServiceClient,
	db *sqlx.DB,
	redisClient *redis.Client,
	logger *zap.Logger,
	adminURL string,
) (*Handler, error) {
	registry, err := GetRegistry()
	if err != nil {
		return nil, fmt.Errorf("failed to load model registry: %w", err)
	}

	llmURL := os.Getenv("LLM_SERVICE_URL")
	if llmURL == "" {
		llmURL = "http://llm-service:8000"
	}

	return &Handler{
		orchClient:     orchClient,
		db:             db,
		redis:          redisClient,
		logger:         logger,
		registry:       registry,
		translator:     NewTranslator(registry),
		sessionManager: NewSessionManager(redisClient, logger),
		adminURL:      strings.TrimRight(adminURL, "/"),
		llmServiceURL: strings.TrimRight(llmURL, "/"),
		attStore:       attachments.NewStore(redisClient, 30*time.Minute),
	}, nil
}

// Close gracefully shuts down the handler and its dependencies.
func (h *Handler) Close() {
	if h.sessionManager != nil {
		h.sessionManager.Close()
	}
}

// ChatCompletions handles POST /v1/chat/completions
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get user context from auth middleware
	userCtx, ok := ctx.Value(auth.UserContextKey).(*auth.UserContext)
	if !ok {
		h.sendError(w, "Unauthorized", ErrorTypeAuthentication, ErrorCodeInvalidAPIKey, http.StatusUnauthorized)
		return
	}

	// Limit request body size to accommodate multimodal payloads with inline
	// base64 attachments (images, PDFs) while preventing abuse.
	r.Body = http.MaxBytesReader(w, r.Body, attachments.MaxMultimodalBodyBytes)

	// Parse request body
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, fmt.Sprintf("Invalid request body: %v", err), ErrorTypeInvalidRequest, ErrorCodeInvalidRequest, http.StatusBadRequest)
		return
	}

	// Validate model
	modelName := req.Model
	if modelName == "" {
		modelName = h.registry.GetDefaultModel()
	}

	// Initialize metrics recorder
	metrics := NewMetricsRecorder(modelName, "chat_completions", req.Stream)

	if !h.registry.IsValidModel(modelName) {
		metrics.RecordError(ErrorTypeInvalidRequest, ErrorCodeModelNotFound)
		h.sendError(w, fmt.Sprintf("Model '%s' not found. Use GET /v1/models to list available models.", req.Model), ErrorTypeInvalidRequest, ErrorCodeModelNotFound, http.StatusNotFound)
		return
	}

	// Resolve session (with collision handling)
	providedSessionID := r.Header.Get(HeaderSessionID)
	sessionResult, err := h.sessionManager.ResolveSession(
		ctx,
		providedSessionID,
		userCtx.UserID.String(),
		userCtx.TenantID.String(),
		&req,
	)
	if err != nil {
		h.logger.Error("Session resolution failed", zap.Error(err))
		// Continue with derived session on error
	}

	// Echo back X-Session-ID if there was a collision or new session
	if sessionResult != nil && (sessionResult.WasCollision || sessionResult.IsNew) {
		w.Header().Set(HeaderSessionID, sessionResult.SessionID)
		// Also return the full Shannon session ID for history/events lookups
		w.Header().Set("X-Shannon-Session-ID", sessionResult.ShannonSession)
		if sessionResult.IsNew {
			RecordSessionCreated()
		}
		if sessionResult.WasCollision {
			RecordSessionCollision()
		}
	}

	// Extract and store attachments from the last user message.
	// Base64-encoded data is stored in Redis and replaced with lightweight
	// references; URL-based attachments are passed through as-is.
	attRefs, err := h.extractAndStoreAttachments(ctx, &req, sessionResult)
	if err != nil {
		metrics.RecordError(ErrorTypeInvalidRequest, ErrorCodeInvalidRequest)
		h.sendError(w, fmt.Sprintf("Attachment error: %v", err), ErrorTypeInvalidRequest, ErrorCodeInvalidRequest, http.StatusBadRequest)
		return
	}

	// Translate to Shannon request (use resolved session)
	translated, err := h.translator.TranslateWithSession(&req, userCtx.UserID.String(), userCtx.TenantID.String(), sessionResult)
	if err != nil {
		metrics.RecordError(ErrorTypeInvalidRequest, ErrorCodeInvalidRequest)
		h.sendError(w, err.Error(), ErrorTypeInvalidRequest, ErrorCodeInvalidRequest, http.StatusBadRequest)
		return
	}

	// Inject attachment references into the gRPC context so downstream
	// services (orchestrator, LLM-service) can resolve them from Redis.
	if len(attRefs) > 0 {
		if translated.GRPCRequest.Context == nil {
			ctxMap := map[string]interface{}{"attachments": attRefs}
			st, stErr := structpb.NewStruct(sanitizeForStructpb(ctxMap))
			if stErr == nil {
				translated.GRPCRequest.Context = st
			}
		} else {
			sanitized := sanitizeValue(attRefs)
			if list, ok := sanitized.([]interface{}); ok {
				translated.GRPCRequest.Context.Fields["attachments"] = structpb.NewListValue(
					mustNewList(list),
				)
			}
		}
	}

	// Add gRPC metadata
	ctx = h.withGRPCMetadata(ctx, r)

	// Submit task to orchestrator
	resp, err := h.orchClient.SubmitTask(ctx, translated.GRPCRequest)
	if err != nil {
		metrics.RecordError(ErrorTypeServer, ErrorCodeInternalError)
		h.handleGRPCError(w, err)
		return
	}

	sessionID := ""
	shannonSessionID := ""
	if sessionResult != nil {
		sessionID = sessionResult.SessionID
		shannonSessionID = sessionResult.ShannonSession
	}

	h.logger.Info("OpenAI task submitted",
		zap.String("task_id", resp.TaskId),
		zap.String("workflow_id", resp.WorkflowId),
		zap.String("model", translated.ModelName),
		zap.String("user_id", userCtx.UserID.String()),
		zap.String("session_id", sessionID),
		zap.String("shannon_session_id", shannonSessionID),
		zap.Bool("stream", translated.Stream),
	)

	// Handle streaming vs non-streaming response
	if translated.Stream {
		h.handleStreamingResponse(ctx, w, r, resp.WorkflowId, translated.ModelName, &req, metrics)
	} else {
		h.handleNonStreamingResponse(ctx, w, resp.TaskId, resp.WorkflowId, translated.ModelName, metrics)
	}
}

// handleStreamingResponse connects to Shannon SSE and streams OpenAI-format chunks.
func (h *Handler) handleStreamingResponse(ctx context.Context, w http.ResponseWriter, r *http.Request, workflowID, modelName string, req *ChatCompletionRequest, metrics *MetricsRecorder) {
	// Build SSE URL - include agent events for rich UI experiences
	// LLM events: LLM_PARTIAL (streaming), LLM_OUTPUT (final)
	// Stream lifecycle: STREAM_END
	// Subscribe to all event types that the streamer forwards:
	// - LLM events: LLM_PARTIAL, LLM_OUTPUT
	// - Stream lifecycle: STREAM_END
	// - Workflow lifecycle: WORKFLOW_STARTED, WORKFLOW_COMPLETED, WORKFLOW_FAILED, WORKFLOW_PAUSING, WORKFLOW_PAUSED, WORKFLOW_RESUMED, WORKFLOW_CANCELLING, WORKFLOW_CANCELLED
	// - Agent lifecycle: AGENT_STARTED, AGENT_COMPLETED, AGENT_THINKING
	// - Tool events: TOOL_INVOKED, TOOL_OBSERVATION
	// - Progress: PROGRESS, DATA_PROCESSING, WAITING, ERROR_RECOVERY
	// - Team/coordination: TEAM_RECRUITED, TEAM_RETIRED, TEAM_STATUS, ROLE_ASSIGNED, DELEGATION, DEPENDENCY_SATISFIED
	// - Budget/approval: BUDGET_THRESHOLD, APPROVAL_REQUESTED, APPROVAL_DECISION
	// - Errors: ERROR_OCCURRED
	sseEventTypes := "LLM_PARTIAL,LLM_OUTPUT,STREAM_END," +
		"WORKFLOW_STARTED,WORKFLOW_COMPLETED,WORKFLOW_FAILED,WORKFLOW_PAUSING,WORKFLOW_PAUSED,WORKFLOW_RESUMED,WORKFLOW_CANCELLING,WORKFLOW_CANCELLED," +
		"AGENT_STARTED,AGENT_COMPLETED,AGENT_THINKING," +
		"TOOL_INVOKED,TOOL_OBSERVATION," +
		"PROGRESS,DATA_PROCESSING,WAITING,ERROR_RECOVERY," +
		"TEAM_RECRUITED,TEAM_RETIRED,TEAM_STATUS,ROLE_ASSIGNED,DELEGATION,DEPENDENCY_SATISFIED," +
		"BUDGET_THRESHOLD,APPROVAL_REQUESTED,APPROVAL_DECISION," +
		"ERROR_OCCURRED"
	sseURL := fmt.Sprintf("%s/stream/sse?workflow_id=%s&types=%s", h.adminURL, workflowID, sseEventTypes)

	// Create SSE request
	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, sseURL, nil)
	if err != nil {
		metrics.RecordError(ErrorTypeServer, ErrorCodeInternalError)
		h.sendError(w, "Failed to create stream request", ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
		return
	}

	// Copy auth headers
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		sseReq.Header.Set("Authorization", authHeader)
	}
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		sseReq.Header.Set("X-API-Key", apiKey)
	}

	// Make SSE request
	// Use custom transport with connection timeouts but no request timeout.
	// Deep research and other long-running workflows can stream beyond 10 minutes.
	// The client request context controls cancellation when the caller disconnects.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second, // Connection timeout
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second, // Wait for response headers
			IdleConnTimeout:       90 * time.Second,
		},
	}
	sseResp, err := client.Do(sseReq)
	if err != nil {
		h.logger.Error("Failed to connect to SSE", zap.Error(err), zap.String("url", sseURL))
		metrics.RecordError(ErrorTypeServer, ErrorCodeInternalError)
		metrics.RecordStreamError("connection_failed")
		h.sendError(w, "Failed to connect to stream", ErrorTypeServer, ErrorCodeInternalError, http.StatusBadGateway)
		return
	}
	defer sseResp.Body.Close()

	if sseResp.StatusCode != http.StatusOK {
		h.logger.Error("SSE returned error", zap.Int("status", sseResp.StatusCode))
		metrics.RecordError(ErrorTypeServer, ErrorCodeInternalError)
		metrics.RecordStreamError("bad_status")
		h.sendError(w, "Stream unavailable", ErrorTypeServer, ErrorCodeInternalError, http.StatusBadGateway)
		return
	}

	// Create streamer and transform events
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	streamer := NewStreamerWithMetrics(h.logger, modelName, metrics)

	if err := streamer.StreamResponse(ctx, sseResp.Body, w, includeUsage); err != nil {
		h.logger.Error("Stream error", zap.Error(err))
		metrics.RecordStreamError("stream_interrupted")
		// Error already written to stream
	} else {
		metrics.RecordSuccess()
		metrics.RecordStreamComplete()
	}

	usage := streamer.GetUsage()
	if usage != nil && usage.TotalTokens == 0 {
		usage = h.getUsageFromDB(ctx, workflowID)
	}
	if usage != nil && usage.TotalTokens > 0 {
		metrics.RecordTokens(usage.PromptTokens, usage.CompletionTokens)
	}
}

// handleNonStreamingResponse waits for completion and returns full response.
func (h *Handler) handleNonStreamingResponse(ctx context.Context, w http.ResponseWriter, taskID, workflowID, modelName string, metrics *MetricsRecorder) {
	// Poll for completion
	var result string
	var usage *Usage

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// 35-minute timeout to support deep research and other long-running tasks.
	// For very long tasks, consider using streaming or async polling instead.
	timeout := time.After(35 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			metrics.RecordError(ErrorTypeServer, "request_cancelled")
			h.sendError(w, "Request cancelled", ErrorTypeServer, ErrorCodeInternalError, http.StatusGatewayTimeout)
			return
		case <-timeout:
			metrics.RecordError(ErrorTypeServer, "timeout")
			h.sendError(w, "Request timed out", ErrorTypeServer, ErrorCodeInternalError, http.StatusGatewayTimeout)
			return
		case <-ticker.C:
			// Check task status
			statusResp, err := h.orchClient.GetTaskStatus(ctx, &orchpb.GetTaskStatusRequest{TaskId: taskID})
			if err != nil {
				h.logger.Error("Failed to get task status", zap.Error(err))
				continue
			}

			switch statusResp.Status {
			case orchpb.TaskStatus_TASK_STATUS_COMPLETED:
				result = statusResp.Result
				// Try to get usage from database
				usage = h.getUsageFromDB(ctx, workflowID)
				if usage != nil && usage.TotalTokens > 0 {
					metrics.RecordTokens(usage.PromptTokens, usage.CompletionTokens)
				}
				goto respond
			case orchpb.TaskStatus_TASK_STATUS_FAILED:
				metrics.RecordError(ErrorTypeServer, "task_failed")
				h.sendError(w, statusResp.ErrorMessage, ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
				return
			case orchpb.TaskStatus_TASK_STATUS_CANCELLED:
				metrics.RecordError(ErrorTypeServer, "task_cancelled")
				h.sendError(w, "Task was cancelled", ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
				return
			}
		}
	}

respond:
	metrics.RecordSuccess()
	// Build response
	response := &ChatCompletionResponse{
		ID:      GenerateCompletionID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []Choice{
			{
				Index: 0,
				Message: &ChatMessage{
					Role:    "assistant",
					Content: result,
				},
				FinishReason: "stop",
			},
		},
		Usage: usage,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// ListModels handles GET /v1/models
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	// Auth check (optional for models endpoint, but good practice)
	if _, ok := r.Context().Value(auth.UserContextKey).(*auth.UserContext); !ok {
		h.sendError(w, "Unauthorized", ErrorTypeAuthentication, ErrorCodeInvalidAPIKey, http.StatusUnauthorized)
		return
	}

	models := h.registry.ListModels()
	response := ModelsResponse{
		Object: "list",
		Data:   models,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetModel handles GET /v1/models/{model}
func (h *Handler) GetModel(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")
	if modelID == "" {
		h.sendError(w, "Model ID required", ErrorTypeInvalidRequest, ErrorCodeInvalidRequest, http.StatusBadRequest)
		return
	}

	if !h.registry.IsValidModel(modelID) {
		h.sendError(w, fmt.Sprintf("Model '%s' not found", modelID), ErrorTypeNotFound, ErrorCodeModelNotFound, http.StatusNotFound)
		return
	}

	model, _ := h.registry.GetModel(modelID)
	response := ModelObject{
		ID:      modelID,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: "shannon",
	}

	// Add description in a non-standard but useful way
	if model.Description != "" {
		w.Header().Set("X-Model-Description", model.Description)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// getUsageFromDB retrieves usage statistics from the database.
func (h *Handler) getUsageFromDB(ctx context.Context, workflowID string) *Usage {
	var totalTokens, promptTokens, completionTokens int
	err := h.db.QueryRowxContext(ctx, `
		SELECT COALESCE(total_tokens, 0), COALESCE(prompt_tokens, 0), COALESCE(completion_tokens, 0)
		FROM task_executions WHERE workflow_id = $1
	`, workflowID).Scan(&totalTokens, &promptTokens, &completionTokens)

	if err != nil {
		h.logger.Debug("Failed to get usage from DB", zap.Error(err))
		return nil
	}

	if totalTokens == 0 && promptTokens == 0 && completionTokens == 0 {
		return nil
	}

	return &Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
}

// extractToken extracts the auth token from the request.
func (h *Handler) extractToken(r *http.Request) string {
	// Try Authorization header first
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Try API key
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		return apiKey
	}

	return ""
}

// withGRPCMetadata adds authentication metadata to gRPC context.
func (h *Handler) withGRPCMetadata(ctx context.Context, r *http.Request) context.Context {
	md := metadata.New(nil)

	// Forward user ID
	if userCtx, ok := ctx.Value(auth.UserContextKey).(*auth.UserContext); ok {
		md.Set("x-user-id", userCtx.UserID.String())
		md.Set("x-tenant-id", userCtx.TenantID.String())
	}

	// Forward auth headers.
	// IMPORTANT: orchestrator gRPC auth checks Authorization as JWT first and does not fall back to API key,
	// so we must map Bearer API keys to x-api-key metadata (not authorization).
	if apiKey := strings.TrimSpace(r.Header.Get("X-API-Key")); apiKey != "" {
		md.Set("x-api-key", normalizeAPIKey(apiKey))
	} else if authHeader := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		raw := strings.TrimSpace(authHeader[len("bearer "):])
		if raw != "" {
			if isLikelyJWT(raw) {
				md.Set("authorization", authHeader)
			} else {
				md.Set("x-api-key", normalizeAPIKey(raw))
			}
		}
	}

	// Forward trace headers
	if traceID := r.Header.Get("traceparent"); traceID != "" {
		md.Set("traceparent", traceID)
	}

	return metadata.NewOutgoingContext(ctx, md)
}

func normalizeAPIKey(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "sk-shannon-") {
		token = strings.TrimPrefix(token, "sk-shannon-")
		if !strings.HasPrefix(token, "sk_") {
			token = "sk_" + token
		}
	}
	return token
}

func isLikelyJWT(token string) bool {
	return strings.Count(token, ".") == 2
}

// handleGRPCError converts gRPC errors to OpenAI error responses.
func (h *Handler) handleGRPCError(w http.ResponseWriter, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument:
			h.sendError(w, st.Message(), ErrorTypeInvalidRequest, ErrorCodeInvalidRequest, http.StatusBadRequest)
		case codes.Unauthenticated:
			h.sendError(w, "Invalid API key", ErrorTypeAuthentication, ErrorCodeInvalidAPIKey, http.StatusUnauthorized)
		case codes.PermissionDenied:
			h.sendError(w, "Permission denied", ErrorTypePermission, ErrorCodeInvalidRequest, http.StatusForbidden)
		case codes.NotFound:
			h.sendError(w, st.Message(), ErrorTypeNotFound, ErrorCodeModelNotFound, http.StatusNotFound)
		case codes.ResourceExhausted:
			h.sendError(w, "Rate limit exceeded", ErrorTypeRateLimit, ErrorCodeRateLimitExceeded, http.StatusTooManyRequests)
		default:
			h.sendError(w, st.Message(), ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
		}
	} else {
		h.sendError(w, err.Error(), ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
	}
}

// sendError sends an OpenAI-compatible error response.
func (h *Handler) sendError(w http.ResponseWriter, message, errType, code string, httpStatus int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)

	resp := NewErrorResponse(message, errType, code)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("Failed to encode error response", zap.Error(err))
	}
}

// extractAndStoreAttachments finds the last user message in req.Messages,
// stores any base64-encoded attachments in Redis, and returns lightweight
// references. URL-based attachments are passed through as-is.
func (h *Handler) extractAndStoreAttachments(ctx context.Context, req *ChatCompletionRequest, session *SessionResult) ([]interface{}, error) {
	// Find the last user message (same scan order as extractQuery).
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := &req.Messages[i]
		if msg.Role != "user" || len(msg.RawAttachments) == 0 {
			continue
		}

		sessionID := ""
		if session != nil {
			sessionID = session.ShannonSession
		}

		var refs []interface{}
		for _, att := range msg.RawAttachments {
			switch att.SourceType {
			case "base64":
				id, err := h.attStore.Put(ctx, sessionID, att.Data, att.MediaType, att.Filename)
				if err != nil {
					return nil, fmt.Errorf("store attachment: %w", err)
				}
				refs = append(refs, map[string]interface{}{
					"id":         id,
					"media_type": att.MediaType,
					"filename":   att.Filename,
					"size_bytes": len(att.Data),
				})
			case "url":
				refs = append(refs, map[string]interface{}{
					"url":        att.URL,
					"media_type": att.MediaType,
					"source":     "url",
				})
			}
		}
		return refs, nil
	}
	return nil, nil
}

// mustNewList creates a structpb.ListValue from a []interface{}, panicking on error.
// This is safe because all values have already been sanitized by sanitizeValue.
func mustNewList(items []interface{}) *structpb.ListValue {
	list, err := structpb.NewList(items)
	if err != nil {
		// Should never happen after sanitization, but handle gracefully.
		return &structpb.ListValue{}
	}
	return list
}

// completionsRequestPeek is used to peek at the stream flag before proxying.
type completionsRequestPeek struct {
	Stream bool `json:"stream"`
}

// llmCompletionUsage represents token usage from the LLM service.
type llmCompletionUsage struct {
	InputTokens           int     `json:"input_tokens"`
	OutputTokens          int     `json:"output_tokens"`
	TotalTokens           int     `json:"total_tokens"`
	CostUSD               float64 `json:"cost_usd"`
	CacheReadTokens       int     `json:"cache_read_tokens"`
	CacheCreationTokens   int     `json:"cache_creation_tokens"`
	CacheCreation5mTokens int     `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int     `json:"cache_creation_1h_tokens"`
}

// llmCompletionResponse represents the LLM service /completions/ response.
type llmCompletionResponse struct {
	Provider string              `json:"provider"`
	Model    string              `json:"model"`
	Usage    *llmCompletionUsage `json:"usage"`
}

// Completions handles POST /v1/completions — thin authenticated proxy to the
// Python LLM service. The CLI sends messages + tool schemas and gets
// function_call responses back for local tool execution.
func (h *Handler) Completions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get user context from auth middleware
	userCtx, ok := ctx.Value(auth.UserContextKey).(*auth.UserContext)
	if !ok {
		h.sendError(w, "Unauthorized", ErrorTypeAuthentication, ErrorCodeInvalidAPIKey, http.StatusUnauthorized)
		return
	}

	// Read and buffer request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.sendError(w, "Failed to read request body", ErrorTypeInvalidRequest, ErrorCodeInvalidRequest, http.StatusBadRequest)
		return
	}

	// Peek-parse stream flag before proxying
	var reqPeek completionsRequestPeek
	_ = json.Unmarshal(body, &reqPeek)

	// Generate tracking ID
	requestID := uuid.New().String()

	// Build proxy request to LLM service
	proxyURL := h.llmServiceURL + "/completions/"
	proxyReq, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL, bytes.NewReader(body))
	if err != nil {
		h.sendError(w, "Failed to create proxy request", ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("X-User-ID", userCtx.UserID.String())
	proxyReq.Header.Set("X-Tenant-ID", userCtx.TenantID.String())
	proxyReq.Header.Set("X-Workflow-ID", requestID)

	// Streaming requests need no timeout (client context handles cancellation).
	// Non-streaming keeps the existing 120s timeout.
	var client *http.Client
	if reqPeek.Stream {
		client = &http.Client{}
	} else {
		client = &http.Client{Timeout: 120 * time.Second}
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		h.logger.Error("LLM service proxy failed", zap.Error(err))
		h.sendError(w, "LLM service unavailable", ErrorTypeServer, ErrorCodeInternalError, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// If LLM returned error, forward as-is
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(errBody)
		return
	}

	// Streaming: forward SSE from Python to client, capture usage from final event
	if reqPeek.Stream {
		h.handleCompletionsStream(ctx, w, resp, userCtx, requestID)
		return
	}

	// Non-streaming: read full response, record usage, forward
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		h.logger.Error("Failed to read LLM response", zap.Error(err))
		h.sendError(w, "Failed to read LLM response", ErrorTypeServer, ErrorCodeInternalError, http.StatusBadGateway)
		return
	}

	// Best-effort parse response for usage recording
	var llmResp llmCompletionResponse
	if err := json.Unmarshal(respBody, &llmResp); err != nil {
		h.logger.Warn("Failed to parse LLM completion response for usage", zap.Error(err))
	} else if llmResp.Usage != nil && (llmResp.Usage.TotalTokens > 0 ||
		llmResp.Usage.CacheReadTokens > 0 || llmResp.Usage.CacheCreationTokens > 0) {
		// Fire-and-forget usage recording. Cache-only calls (TotalTokens == 0
		// but cache_read or cache_creation > 0) must still record so the
		// quota cost shows up.
		go h.recordCompletionUsage(
			userCtx.UserID, userCtx.TenantID, requestID,
			llmResp.Provider, llmResp.Model, llmResp.Usage,
		)
	}

	h.logger.Info("Completions proxy response",
		zap.String("request_id", requestID),
		zap.String("user_id", userCtx.UserID.String()),
		zap.Int("status", resp.StatusCode),
	)

	// Forward response to client as-is
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// handleCompletionsStream forwards SSE from the Python LLM service to the client,
// capturing usage from the final "done" event for quota recording.
func (h *Handler) handleCompletionsStream(
	ctx context.Context,
	w http.ResponseWriter,
	resp *http.Response,
	userCtx *auth.UserContext,
	requestID string,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.sendError(w, "Streaming not supported", ErrorTypeServer, ErrorCodeInternalError, http.StatusInternalServerError)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	// Track the second-to-last data line — the "done" event with usage.
	// Python emits: data: {"type":"done",...} then data: [DONE].
	// We need the done event, not the [DONE] sentinel.
	var prevDataLine, lastDataLine string

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		// Track data lines for usage extraction
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			prevDataLine = lastDataLine
			lastDataLine = payload
		}

		// Forward every line to client as-is
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil {
		h.logger.Error("Completions stream scanner error", zap.Error(err))
	}

	// The done event is the second-to-last data line (before [DONE] sentinel).
	// If lastDataLine is not [DONE], it IS the done event (stream ended early).
	usageLine := prevDataLine
	if lastDataLine != "[DONE]" {
		usageLine = lastDataLine
	}

	// Extract usage from the done event
	if usageLine != "" {
		var doneEvent struct {
			Type     string              `json:"type"`
			Provider string              `json:"provider"`
			Model    string              `json:"model"`
			Usage    *llmCompletionUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(usageLine), &doneEvent); err == nil {
			if doneEvent.Usage != nil && (doneEvent.Usage.TotalTokens > 0 ||
				doneEvent.Usage.CacheReadTokens > 0 || doneEvent.Usage.CacheCreationTokens > 0) {
				go h.recordCompletionUsage(
					userCtx.UserID, userCtx.TenantID, requestID,
					doneEvent.Provider, doneEvent.Model, doneEvent.Usage,
				)
			}
		}
	}

	h.logger.Info("Completions stream completed",
		zap.String("request_id", requestID),
		zap.String("user_id", userCtx.UserID.String()),
	)
}

// recordCompletionUsage writes token usage to the DB and updates tenant quota
// counters. Runs in a background goroutine — errors are logged, not propagated.
func (h *Handler) recordCompletionUsage(
	userID, tenantID uuid.UUID,
	requestID, provider, model string,
	usage *llmCompletionUsage,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ensure public.users row exists — auth.users ID may not have a
	// matching public.users row, which would violate the token_usage FK.
	// Use "auth-user:<uuid>" as external_id to avoid collisions with legacy external IDs.
	// TODO: Long-term fix is to re-point token_usage FK to auth.users instead of public.users.
	externalID := "auth-user:" + userID.String()
	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO users (id, external_id, tenant_id, auth_user_id, created_at, updated_at)
		VALUES ($1, $2, $3, $1, NOW(), NOW())
		ON CONFLICT (id) DO NOTHING
	`, userID, externalID, tenantID); err != nil {
		h.logger.Warn("Failed to ensure public.users row for completions",
			zap.String("request_id", requestID),
			zap.String("user_id", userID.String()),
			zap.Error(err))
	}

	// Insert into token_usage (task_id=NULL for completions proxy).
	// cache_aware_total_tokens parallels total_tokens but adds prompt-cache
	// classes for accurate quota accounting (see migration 121).
	cacheAwareTotal := usage.InputTokens + usage.OutputTokens +
		usage.CacheReadTokens + usage.CacheCreationTokens
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO token_usage (
			user_id, task_id, agent_id, provider, model,
			prompt_tokens, completion_tokens, total_tokens, cost_usd,
			cache_read_tokens, cache_creation_tokens, cache_aware_total_tokens
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, userID, nil, "completions-proxy", provider, model,
		usage.InputTokens, usage.OutputTokens, usage.TotalTokens, usage.CostUSD,
		usage.CacheReadTokens, usage.CacheCreationTokens, cacheAwareTotal)
	if err != nil {
		h.logger.Warn("Failed to record completion token usage",
			zap.String("request_id", requestID),
			zap.Error(err))
	}

}

// streamContent is a helper to write raw bytes for non-buffered streaming.
func streamContent(w io.Writer, content string) {
	if flusher, ok := w.(http.Flusher); ok {
		w.Write([]byte(content))
		flusher.Flush()
	}
}
