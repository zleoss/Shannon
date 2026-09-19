// =============================================================================
// 文件: go/orchestrator/internal/activities/agent_executor.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Agent 执行器 —— 调度 agent 单步执行、强制工具调用、上下文组装。
// 【关键内容】
//   ExecuteAgent / ForceToolCall / AssembleContext
// 【协作关系】
//   调 Python llm-service 完成 LLM 推理，管理 agent 执行上下文。
// =============================================================================
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/streaming"
	"go.temporal.io/sdk/activity"
	"gopkg.in/yaml.v3"
)

// maxBase64FieldSize is the threshold above which base64 fields are stored in Redis
// instead of being returned through Temporal to avoid the 2MB payload limit.
// Set to 500KB to allow headroom for multiple fields and JSON overhead.
const maxBase64FieldSize = 500 * 1024

// base64FieldsToStore are the field names that may contain large base64 data
var base64FieldsToStore = []string{
	"screenshot_b64",
	"popup_screenshot_b64",
	"data_base64",
	"image_base64",
	"screenshot",
}

// storeOversizedBase64Fields recursively processes a map and stores large
// base64-encoded fields in Redis, replacing them with reference keys.
// Returns the sanitized output and whether any fields were stored.
func storeOversizedBase64Fields(ctx context.Context, workflowID string, output interface{}, path string) (interface{}, bool) {
	if output == nil {
		return nil, false
	}

	switch v := output.(type) {
	case map[string]interface{}:
		stored := false
		result := make(map[string]interface{}, len(v))
		for key, val := range v {
			// Build the path for nested fields
			fieldPath := key
			if path != "" {
				fieldPath = path + "." + key
			}

			// Check if this is a known base64 field
			isBase64Field := false
			for _, fieldName := range base64FieldsToStore {
				if key == fieldName {
					isBase64Field = true
					break
				}
			}

			if isBase64Field {
				if strVal, ok := val.(string); ok && len(strVal) > maxBase64FieldSize {
					// Store in Redis and replace with reference key
					refKey, err := streaming.Get().StoreBlob(ctx, workflowID, fieldPath, strVal)
					if err != nil {
						// If storage fails, use a placeholder
						result[key] = fmt.Sprintf("[STORAGE_FAILED: %d bytes - %v]", len(strVal), err)
					} else {
						// Replace with reference object containing key and size
						result[key+"_ref"] = map[string]interface{}{
							"redis_key": refKey,
							"size":      len(strVal),
							"ttl_days":  7,
						}
						// Remove original field (don't copy it)
						result[key] = nil
					}
					stored = true
					continue
				}
			}

			// Recursively process nested structures
			newVal, childStored := storeOversizedBase64Fields(ctx, workflowID, val, fieldPath)
			result[key] = newVal
			if childStored {
				stored = true
			}
		}
		return result, stored

	case []interface{}:
		stored := false
		result := make([]interface{}, len(v))
		for i, item := range v {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			newItem, childStored := storeOversizedBase64Fields(ctx, workflowID, item, itemPath)
			result[i] = newItem
			if childStored {
				stored = true
			}
		}
		return result, stored

	default:
		return output, false
	}
}

// AgentDefinition represents the configuration for a single agent
type AgentDefinition struct {
	Name         string                 `yaml:"name"`
	Description  string                 `yaml:"description"`
	Tool         string                 `yaml:"tool"`
	Category     string                 `yaml:"category"`
	InputSchema  map[string]interface{} `yaml:"input_schema"`
	OutputSchema map[string]interface{} `yaml:"output_schema"`
	CostPerCall  float64                `yaml:"cost_per_call"`
	TimeoutSecs  int                    `yaml:"timeout_seconds"`
}

// AgentsYAMLConfig represents the full agents.yaml configuration
type AgentsYAMLConfig struct {
	Version  string                     `yaml:"version"`
	Agents   map[string]AgentDefinition `yaml:"agents"`
	Settings map[string]interface{}     `yaml:"settings"`
}

var (
	agentsYAMLConfigOnce   sync.Once
	agentsYAMLConfigCached *AgentsYAMLConfig
	agentsYAMLConfigErr    error
)

// LoadAgentsYAMLConfig loads the agents configuration from YAML
func LoadAgentsYAMLConfig() (*AgentsYAMLConfig, error) {
	agentsYAMLConfigOnce.Do(func() {
		paths := []string{
			"config/agents.yaml",
			"/app/config/agents.yaml",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				data, err := os.ReadFile(p)
				if err != nil {
					agentsYAMLConfigErr = fmt.Errorf("failed to read agents.yaml: %w", err)
					return
				}
				var cfg AgentsYAMLConfig
				if err := yaml.Unmarshal(data, &cfg); err != nil {
					agentsYAMLConfigErr = fmt.Errorf("failed to parse agents.yaml: %w", err)
					return
				}
				agentsYAMLConfigCached = &cfg
				agentsYAMLConfigErr = nil
				return
			}
		}
		agentsYAMLConfigErr = fmt.Errorf("agents.yaml not found")
	})
	return agentsYAMLConfigCached, agentsYAMLConfigErr
}

// GetAgentDefinition retrieves configuration for a specific agent
func GetAgentDefinition(agentID string) (*AgentDefinition, error) {
	cfg, err := LoadAgentsYAMLConfig()
	if err != nil {
		return nil, err
	}
	agent, ok := cfg.Agents[agentID]
	if !ok {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	return &agent, nil
}

// ListAgentIDs returns all available agent IDs
func ListAgentIDs() ([]string, error) {
	cfg, err := LoadAgentsYAMLConfig()
	if err != nil {
		return nil, err
	}
	agents := make([]string, 0, len(cfg.Agents))
	for id := range cfg.Agents {
		agents = append(agents, id)
	}
	return agents, nil
}

// ExecuteAgentInput is the input for ExecuteAgentActivity
type ExecuteAgentInput struct {
	AgentID    string                 `json:"agent_id"`
	Input      map[string]interface{} `json:"input"`
	WorkflowID string                 `json:"workflow_id"`
	UserID     string                 `json:"user_id"`
	TenantID   string                 `json:"tenant_id"`
}

// ExecuteAgentOutput is the output from ExecuteAgentActivity
type ExecuteAgentOutput struct {
	Success       bool                   `json:"success"`
	Output        interface{}            `json:"output"`
	Error         string                 `json:"error,omitempty"`
	AgentID       string                 `json:"agent_id"`
	ToolName      string                 `json:"tool_name"`
	ExecutionTime int                    `json:"execution_time_ms"`
	CostUSD       float64                `json:"cost_usd"`
	TokensUsed    int                    `json:"tokens_used,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// ExecuteAgentActivity executes a single agent by calling its underlying tool
func ExecuteAgentActivity(ctx context.Context, input ExecuteAgentInput) (*ExecuteAgentOutput, error) {
	startTime := time.Now()
	logger := activity.GetLogger(ctx)

	// Log context deadline for debugging timeout issues
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		logger.Info("ExecuteAgentActivity started",
			"agent_id", input.AgentID,
			"workflow_id", input.WorkflowID,
			"context_deadline", deadline.Format(time.RFC3339),
			"context_remaining", remaining.String())
	} else {
		logger.Info("ExecuteAgentActivity started",
			"agent_id", input.AgentID,
			"workflow_id", input.WorkflowID,
			"context_deadline", "none")
	}

	// Load agent configuration
	agentDef, err := GetAgentDefinition(input.AgentID)
	if err != nil {
		return &ExecuteAgentOutput{
			Success: false,
			Error:   fmt.Sprintf("failed to load agent config: %v", err),
			AgentID: input.AgentID,
		}, nil
	}

	// Determine timeout
	timeout := 120 * time.Second
	if agentDef.TimeoutSecs > 0 {
		timeout = time.Duration(agentDef.TimeoutSecs) * time.Second
	}

	params := input.Input
	if params == nil {
		params = map[string]interface{}{}
	}

	// Special handling for keyword_extract (uses LLM activity directly)
	if agentDef.Tool == "keyword_extract" {
		return executeKeywordExtractAgent(ctx, input, agentDef, startTime)
	}

	// Special handling for browser-screenshot agent (navigate + screenshot + close)
	// Dispatch by agent config key, not tool field, since tool is now "browser"
	if input.AgentID == "browser-screenshot" {
		return executeBrowserScreenshotAgent(ctx, input, agentDef, startTime, timeout)
	}

	// Special handling for ads_transparency_search (multi-platform fan-out)
	if agentDef.Tool == "ads_transparency_search" {
		return executeMultiPlatformTransparency(ctx, input, agentDef, startTime)
	}

	// Call the underlying tool with heartbeat support for long-running tools
	result, err := callToolWithHeartbeat(ctx, agentDef.Tool, params, timeout, input.AgentID)
	if err != nil {
		logger.Warn("Tool call failed", "tool", agentDef.Tool, "error", err, "elapsed", time.Since(startTime).String())
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         err.Error(),
			AgentID:       input.AgentID,
			ToolName:      agentDef.Tool,
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	logger.Info("Tool call succeeded, processing result",
		"tool", agentDef.Tool,
		"elapsed", time.Since(startTime).String(),
		"result_has_output", result.Output != nil)

	// Check context before processing result
	if ctx.Err() != nil {
		logger.Warn("Context error after tool call", "error", ctx.Err())
	}

	// Extract cost from result metadata if available
	costUSD := agentDef.CostPerCall
	tokensUsed := result.TokensUsed
	if result.Metadata != nil {
		if cost, ok := result.Metadata["api_cost_usd"].(float64); ok {
			costUSD = cost
		}
		if cost, ok := result.Metadata["cost_usd"].(float64); ok {
			costUSD = cost
		}
	}
	// Also check output for cost
	if outputMap, ok := result.Output.(map[string]interface{}); ok {
		if cost, ok := outputMap["api_cost_usd"].(float64); ok {
			costUSD = cost
		}
		if cost, ok := outputMap["cost_usd"].(float64); ok {
			costUSD = cost
		}
	}

	// Store oversized base64 fields in Redis to avoid Temporal's 2MB payload limit.
	// This is critical for tools like lp_visual_analyze that return large screenshots.
	// The fields are replaced with reference keys that can be used to fetch the data.
	sanitizedOutput, stored := storeOversizedBase64Fields(ctx, input.WorkflowID, result.Output, "")
	if stored {
		logger.Info("Stored oversized base64 fields in Redis",
			"agent_id", input.AgentID,
			"tool", agentDef.Tool,
			"workflow_id", input.WorkflowID)
	}

	// Safety net: if total serialized output still exceeds 1.5MB, store entire output in Redis.
	// This catches cases where many small base64 fields collectively exceed the Temporal limit.
	const maxOutputSize = 1500 * 1024 // 1.5MB - leaves headroom for Temporal's 2MB limit
	if serialized, err := json.Marshal(sanitizedOutput); err == nil && len(serialized) > maxOutputSize {
		logger.Warn("Agent output exceeds safe size, storing entire output in Redis",
			"agent_id", input.AgentID,
			"size_bytes", len(serialized),
			"workflow_id", input.WorkflowID)
		refKey, storeErr := streaming.Get().StoreBlob(ctx, input.WorkflowID, "full_output", string(serialized))
		if storeErr == nil {
			sanitizedOutput = map[string]interface{}{
				"_stored_in_redis": true,
				"redis_key":        refKey,
				"size_bytes":       len(serialized),
				"ttl_days":         7,
			}
		} else {
			logger.Error("Failed to store oversized output in Redis", "error", storeErr)
			sanitizedOutput = map[string]interface{}{
				"_error":     "output_too_large_redis_failed",
				"size_bytes": len(serialized),
				"agent_id":   input.AgentID,
			}
		}
	}

	output := &ExecuteAgentOutput{
		Success:       true,
		Output:        sanitizedOutput,
		AgentID:       input.AgentID,
		ToolName:      agentDef.Tool,
		ExecutionTime: int(time.Since(startTime).Milliseconds()),
		CostUSD:       costUSD,
		TokensUsed:    tokensUsed,
		Metadata:      result.Metadata,
	}

	logger.Info("ExecuteAgentActivity returning",
		"agent_id", input.AgentID,
		"success", true,
		"stored_in_redis", stored,
		"elapsed", time.Since(startTime).String())

	return output, nil
}

// executeKeywordExtractAgent handles the special keyword extraction agent
func executeKeywordExtractAgent(ctx context.Context, input ExecuteAgentInput, agentDef *AgentDefinition, startTime time.Time) (*ExecuteAgentOutput, error) {
	// Extract parameters
	params := input.Input
	if params == nil {
		params = map[string]interface{}{}
	}

	query, _ := params["query"].(string)
	country, _ := params["country"].(string)
	if country == "" {
		country = "us"
	}
	language, _ := params["language"].(string)

	if query == "" {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         "query is required",
			AgentID:       input.AgentID,
			ToolName:      "keyword_extract",
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	// Call the existing keyword extraction function
	extractInput := ExtractKeywordsInput{
		Query:    query,
		Country:  country,
		Language: language,
	}

	extractOutput, err := ExtractSearchKeywords(ctx, extractInput)
	if err != nil {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         err.Error(),
			AgentID:       input.AgentID,
			ToolName:      "keyword_extract",
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	// Calculate total tokens and estimate cost (small model: ~$0.001 per 1K tokens)
	totalTokens := extractOutput.InputTokens + extractOutput.OutputTokens
	estimatedCost := float64(totalTokens) * 0.000001 // Rough estimate

	output := map[string]interface{}{
		"keywords":          extractOutput.Keywords,
		"detected_language": extractOutput.DetectedLanguage,
		"detected_country":  extractOutput.DetectedCountry,
		"api_cost_usd":      estimatedCost,
	}

	return &ExecuteAgentOutput{
		Success:       true,
		Output:        output,
		AgentID:       input.AgentID,
		ToolName:      "keyword_extract",
		ExecutionTime: int(time.Since(startTime).Milliseconds()),
		CostUSD:       estimatedCost,
		TokensUsed:    totalTokens,
	}, nil
}

type toolExecuteRequestWithSessionContext struct {
	ToolName       string                 `json:"tool_name"`
	Parameters     map[string]interface{} `json:"parameters"`
	SessionContext map[string]interface{} `json:"session_context,omitempty"`
}

func executeBrowserScreenshotAgent(ctx context.Context, input ExecuteAgentInput, agentDef *AgentDefinition, startTime time.Time, timeout time.Duration) (*ExecuteAgentOutput, error) {
	params := input.Input
	if params == nil {
		params = map[string]interface{}{}
	}

	url, _ := params["url"].(string)
	fullPage, _ := params["full_page"].(bool)
	waitUntil, _ := params["wait_until"].(string)
	if waitUntil == "" {
		waitUntil = "domcontentloaded"
	}

	timeoutMS := 30000
	if v, ok := params["timeout_ms"].(int); ok {
		timeoutMS = v
	} else if v, ok := params["timeout_ms"].(float64); ok {
		timeoutMS = int(v)
	}

	if url == "" {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         "url is required",
			AgentID:       input.AgentID,
			ToolName:      agentDef.Tool,
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	// Validate URL scheme - only allow http/https for security
	if err := validateBrowserURL(url); err != nil {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         err.Error(),
			AgentID:       input.AgentID,
			ToolName:      agentDef.Tool,
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	sessionCtx := map[string]interface{}{
		"session_id": fmt.Sprintf("agent-%s-%s", input.WorkflowID, input.AgentID),
	}
	// Use context.Background() for cleanup to ensure browser is closed even if parent context is cancelled
	defer func() {
		_, _ = callToolForAgentExec(context.Background(), "browser", map[string]interface{}{"action": "close"}, sessionCtx, 10*time.Second)
	}()

	_, err := callToolForAgentExec(ctx, "browser", map[string]interface{}{
		"action":     "navigate",
		"url":        url,
		"wait_until": waitUntil,
		"timeout_ms": timeoutMS,
	}, sessionCtx, timeout)
	if err != nil {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         err.Error(),
			AgentID:       input.AgentID,
			ToolName:      agentDef.Tool,
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	result, err := callToolForAgentExec(ctx, "browser", map[string]interface{}{
		"action":    "screenshot",
		"full_page": fullPage,
	}, sessionCtx, timeout)
	if err != nil {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         err.Error(),
			AgentID:       input.AgentID,
			ToolName:      agentDef.Tool,
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	return &ExecuteAgentOutput{
		Success:       true,
		Output:        result.Output,
		AgentID:       input.AgentID,
		ToolName:      agentDef.Tool,
		ExecutionTime: int(time.Since(startTime).Milliseconds()),
		CostUSD:       agentDef.CostPerCall,
		TokensUsed:    result.TokensUsed,
		Metadata:      result.Metadata,
	}, nil
}

// callToolForAgentExec is a helper that calls the LLM service tool endpoint.
// When sessionContext is provided, it is forwarded as request.session_context.
func callToolForAgentExec(ctx context.Context, toolName string, params map[string]interface{}, sessionContext map[string]interface{}, timeout time.Duration) (*ToolExecuteResponse, error) {
	if params == nil {
		params = map[string]interface{}{}
	}
	if sessionContext == nil {
		return callToolWithTimeout(ctx, toolName, params, timeout)
	}

	llmServiceURL := getenv("LLM_SERVICE_URL", "http://llm-service:8000")
	url := fmt.Sprintf("%s/tools/execute", llmServiceURL)

	reqBody := toolExecuteRequestWithSessionContext{
		ToolName:       toolName,
		Parameters:     params,
		SessionContext: sessionContext,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tool execution failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ToolExecuteResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("tool error: %s", result.Error)
	}

	return &result, nil
}

// validateBrowserURL checks that a URL is safe for browser navigation.
// Only http and https schemes are allowed to prevent security issues
// with javascript:, file://, data:, and other potentially dangerous URIs.
func validateBrowserURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("invalid URL scheme %q: only http and https are allowed", parsed.Scheme)
	}

	return nil
}

// callToolWithHeartbeat calls a tool and sends periodic heartbeats to Temporal
// to prevent heartbeat timeout for long-running tools like LP analysis.
func callToolWithHeartbeat(ctx context.Context, toolName string, params map[string]interface{}, timeout time.Duration, agentID string) (*ToolExecuteResponse, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("callToolWithHeartbeat starting", "tool", toolName, "agent_id", agentID, "timeout", timeout.String())

	type result struct {
		resp *ToolExecuteResponse
		err  error
	}

	resultCh := make(chan result, 1)
	startTime := time.Now()

	// Run tool call in goroutine
	go func() {
		logger.Info("Tool goroutine starting HTTP call", "tool", toolName)
		resp, err := callToolWithTimeout(ctx, toolName, params, timeout)
		logger.Info("Tool goroutine HTTP call completed", "tool", toolName, "success", resp != nil && resp.Success, "error", err)
		resultCh <- result{resp: resp, err: err}
	}()

	// Send heartbeats every 30 seconds while waiting for the tool
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case r := <-resultCh:
			logger.Info("callToolWithHeartbeat received result", "tool", toolName, "elapsed", time.Since(startTime).String(), "success", r.resp != nil && r.resp.Success, "error", r.err)
			return r.resp, r.err
		case <-ticker.C:
			// Record heartbeat to prevent Temporal heartbeat timeout
			logger.Info("Sending heartbeat", "tool", toolName, "elapsed", time.Since(startTime).String())
			activity.RecordHeartbeat(ctx, map[string]interface{}{
				"agent_id": agentID,
				"tool":     toolName,
				"status":   "executing",
				"elapsed":  time.Since(startTime).String(),
			})
		case <-ctx.Done():
			logger.Warn("Context cancelled", "tool", toolName, "elapsed", time.Since(startTime).String(), "error", ctx.Err())
			return nil, ctx.Err()
		}
	}
}

// determinePlatforms decides which ad platforms to query based on country and explicit overrides.
func determinePlatforms(country string, platforms []string) (google, yahoo, meta bool) {
	if len(platforms) > 0 {
		for _, p := range platforms {
			switch strings.ToLower(p) {
			case "google":
				google = true
			case "yahoo":
				yahoo = true
			case "meta":
				meta = true
			}
		}
		return
	}
	google = true
	meta = true
	yahoo = strings.EqualFold(country, "jp")
	return
}

// executeMultiPlatformTransparency fans out to Google Transparency + Yahoo JP + Meta Ad Library in parallel.
func executeMultiPlatformTransparency(ctx context.Context, input ExecuteAgentInput, agentDef *AgentDefinition, startTime time.Time) (*ExecuteAgentOutput, error) {
	logger := activity.GetLogger(ctx)
	params := input.Input
	if params == nil {
		params = map[string]interface{}{}
	}

	// Apply agent-level timeout as overall deadline
	if agentDef.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(agentDef.TimeoutSecs)*time.Second)
		defer cancel()
	}

	domain, _ := params["domain"].(string)
	advertiserID, _ := params["advertiser_id"].(string)
	country, _ := params["country"].(string)
	if country == "" {
		country, _ = params["region"].(string)
	}

	var platformsList []string
	if pRaw, ok := params["platforms"]; ok {
		if pSlice, ok := pRaw.([]interface{}); ok {
			for _, p := range pSlice {
				if s, ok := p.(string); ok {
					platformsList = append(platformsList, s)
				}
			}
		} else if pSlice, ok := pRaw.([]string); ok {
			platformsList = pSlice
		}
	}

	runGoogle, runYahoo, runMeta := determinePlatforms(country, platformsList)

	// If no platforms are selected (e.g. invalid override like ["tiktok"]), fail fast
	if !runGoogle && !runYahoo && !runMeta {
		return &ExecuteAgentOutput{
			Success:       false,
			Error:         "no valid platforms selected (valid: google, yahoo, meta)",
			AgentID:       input.AgentID,
			ToolName:      "ads_transparency_search",
			ExecutionTime: int(time.Since(startTime).Milliseconds()),
		}, nil
	}

	logger.Info("Multi-platform transparency starting",
		"domain", domain,
		"advertiser_id", advertiserID,
		"country", country,
		"run_google", runGoogle,
		"run_yahoo", runYahoo,
		"run_meta", runMeta,
	)

	type platformResult struct {
		name   string
		output interface{}
		err    error
		cost   float64
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []platformResult
	)

	collect := func(name string, output interface{}, err error, cost float64) {
		mu.Lock()
		results = append(results, platformResult{name: name, output: output, err: err, cost: cost})
		mu.Unlock()
	}

	// --- Google Transparency ---
	if runGoogle {
		wg.Add(1)
		go func() {
			defer wg.Done()
			activity.RecordHeartbeat(ctx, "google_transparency")
			googleParams := map[string]interface{}{}
			if advertiserID != "" {
				googleParams["advertiser_id"] = advertiserID
			}
			if domain != "" {
				googleParams["domain"] = domain
			}
			if country != "" {
				googleParams["region"] = country
			}
			if v, ok := params["platform"]; ok {
				googleParams["platform"] = v
			}
			if v, ok := params["creative_format"]; ok {
				googleParams["creative_format"] = v
			}
			if v, ok := params["start_date"]; ok {
				googleParams["start_date"] = v
			}
			if v, ok := params["end_date"]; ok {
				googleParams["end_date"] = v
			}
			result, err := callToolWithTimeout(ctx, "ads_transparency_search", googleParams, 30*time.Second)
			var cost float64
			var output interface{}
			if err == nil && result != nil {
				output = result.Output
				cost = extractCostFromResult(result)
			}
			collect("google", output, err, cost)
		}()
	}

	// --- Yahoo JP ---
	if runYahoo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			activity.RecordHeartbeat(ctx, "yahoo_jp")
			keyword := domain
			if keyword == "" {
				keyword = advertiserID
			}
			if keyword == "" {
				collect("yahoo_jp", nil, fmt.Errorf("no domain or advertiser_id for Yahoo search"), 0)
				return
			}
			yahooParams := map[string]interface{}{
				"keywords":     []string{keyword},
				"max_results":  20,
				"resolve_urls": false,
			}
			result, err := callToolWithTimeout(ctx, "yahoo_jp_ads_discover", yahooParams, 120*time.Second)
			var cost float64
			var output interface{}
			if err == nil && result != nil {
				output = result.Output
				cost = extractCostFromResult(result)
			}
			collect("yahoo_jp", output, err, cost)
		}()
	}

	// --- Meta Ad Library ---
	if runMeta {
		wg.Add(1)
		go func() {
			defer wg.Done()
			activity.RecordHeartbeat(ctx, "meta_ad_library")
			query := domain
			if query == "" {
				query = advertiserID
			}
			if query == "" {
				collect("meta", nil, fmt.Errorf("no domain or advertiser_id for Meta search"), 0)
				return
			}
			metaParams := map[string]interface{}{
				"query":       query,
				"ad_status":   "active",
				"max_results": 20,
			}
			if country != "" {
				metaParams["country"] = strings.ToUpper(country)
			}
			if mp, ok := params["meta_platform"].(string); ok && mp != "" {
				metaParams["platform"] = mp
			}
			result, err := callToolWithTimeout(ctx, "meta_ad_library_search", metaParams, 30*time.Second)
			var cost float64
			var output interface{}
			if err == nil && result != nil {
				output = result.Output
				cost = extractCostFromResult(result)
			}
			collect("meta", output, err, cost)
		}()
	}

	wg.Wait()

	merged := map[string]interface{}{}
	errors := map[string]string{}
	queried := []string{}
	var totalCost float64

	for _, r := range results {
		queried = append(queried, r.name)
		if r.err != nil {
			errors[r.name] = r.err.Error()
			logger.Warn("Platform failed", "platform", r.name, "error", r.err)
		} else {
			merged[r.name] = r.output
		}
		totalCost += r.cost
	}

	merged["platforms_queried"] = queried
	if country != "" {
		merged["country"] = country
	}
	if len(errors) > 0 {
		merged["errors"] = errors
	}

	logger.Info("Multi-platform transparency completed",
		"platforms_queried", queried,
		"errors", len(errors),
		"total_cost", totalCost,
		"elapsed", time.Since(startTime).String(),
	)

	allFailed := len(results) > 0 && len(errors) == len(results)
	var errMsg string
	if allFailed {
		msgs := make([]string, 0, len(errors))
		for platform, msg := range errors {
			msgs = append(msgs, platform+": "+msg)
		}
		errMsg = "all platforms failed: " + strings.Join(msgs, "; ")
	}

	return &ExecuteAgentOutput{
		Success:       !allFailed,
		Error:         errMsg,
		Output:        merged,
		AgentID:       input.AgentID,
		ToolName:      "ads_transparency_search",
		ExecutionTime: int(time.Since(startTime).Milliseconds()),
		CostUSD:       totalCost,
	}, nil
}

// extractCostFromResult extracts cost from tool result metadata or output body.
// Checks Metadata["api_cost_usd"], Metadata["cost_usd"], then Output["cost_usd"].
func extractCostFromResult(result *ToolExecuteResponse) float64 {
	if result.Metadata != nil {
		if c, ok := result.Metadata["api_cost_usd"].(float64); ok {
			return c
		}
		if c, ok := result.Metadata["cost_usd"].(float64); ok {
			return c
		}
	}
	if outputMap, ok := result.Output.(map[string]interface{}); ok {
		if c, ok := outputMap["api_cost_usd"].(float64); ok {
			return c
		}
		if c, ok := outputMap["cost_usd"].(float64); ok {
			return c
		}
	}
	return 0
}
