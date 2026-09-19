// =============================================================================
// 文件: go/orchestrator/cmd/gateway/internal/proxy/streaming.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   SSE / WebSocket 代理 —— 透传流式请求到后端 llm-service。
// 【关键内容】
//   SSE 代理 / WebSocket 代理 / HTTP 反向代理
// 【协作关系】
//   作为轻量级反向代理，不经过 orchestrator，直接转发到 Python LLM 服务。
// =============================================================================
package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"go.uber.org/zap"
)

// StreamingProxy handles reverse proxying to the admin server for SSE/WebSocket
type StreamingProxy struct {
	proxy  *httputil.ReverseProxy
	target *url.URL
	logger *zap.Logger
}

// NewStreamingProxy creates a new streaming proxy
func NewStreamingProxy(adminURL string, logger *zap.Logger) (*StreamingProxy, error) {
	target, err := url.Parse(adminURL)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Customize the proxy for SSE support
	sp := &StreamingProxy{
		proxy:  proxy,
		target: target,
		logger: logger,
	}

	// Customize Director to rewrite paths and preserve headers
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// Rewrite paths: /api/v1/stream/sse -> /stream/sse, /api/v1/blob/* -> /blob/*
		req.URL.Path = strings.Replace(req.URL.Path, "/api/v1/stream", "/stream", 1)
		req.URL.Path = strings.Replace(req.URL.Path, "/api/v1/blob", "/blob", 1)

		// Don't forward auth query params upstream (they may appear in logs).
		// Gateway auth middleware validates them before proxying.
		q := req.URL.Query()
		changed := false
		if q.Has("token") {
			q.Del("token")
			changed = true
		}
		if q.Has("api_key") {
			q.Del("api_key")
			changed = true
		}
		if changed {
			req.URL.RawQuery = q.Encode()
		}

		// Preserve important headers
		req.Host = target.Host

		// Log proxy request
		logger.Debug("Proxying streaming request",
			zap.String("original_path", req.URL.Path),
			zap.String("target", target.String()),
		)
	}

	// Customize ModifyResponse to ensure SSE works properly
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Ensure proper headers for SSE
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			resp.Header.Set("Cache-Control", "no-cache")
			resp.Header.Set("Connection", "keep-alive")
			resp.Header.Set("X-Accel-Buffering", "no")
		}

		// CORS headers handled by CORS middleware, not here

		return nil
	}

	// Set FlushInterval to 0 for immediate flushing (important for SSE)
	proxy.FlushInterval = -1 // Negative value means flush immediately

	return sp, nil
}

// ServeHTTP handles the HTTP request
func (sp *StreamingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if this is a WebSocket upgrade request
	if sp.isWebSocketRequest(r) {
		sp.handleWebSocket(w, r)
		return
	}

	// Handle as regular HTTP/SSE request
	sp.proxy.ServeHTTP(w, r)
}

// isWebSocketRequest checks if the request is for WebSocket upgrade
func (sp *StreamingProxy) isWebSocketRequest(r *http.Request) bool {
	return strings.ToLower(r.Header.Get("Upgrade")) == "websocket"
}

// handleWebSocket handles WebSocket proxy
func (sp *StreamingProxy) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// WebSocket requires special handling
	// For simplicity, we'll use the default proxy which handles WebSocket
	sp.proxy.ServeHTTP(w, r)
}
