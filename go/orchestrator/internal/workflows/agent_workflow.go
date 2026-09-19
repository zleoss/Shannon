// =============================================================================
// 文件: go/orchestrator/internal/workflows/agent_workflow.go
// -----------------------------------------------------------------------------
// 【一句话功能】 AgentWorkflow —— 单一确定性 agent 执行，自带完整 schema 校验。
//   与 swarm/random 多 agent 不同，这是"一个具名 agent 跑一轮"的轻量入口。
// 【定位】 "确定性单 agent 通路"：当请求 context 携带 `agent: <agentID>` 时启用。
// 【触发】 orchestrator_router.go:587-625；并被 skip_synthesis 路由复用。
// 【关键函数】 AgentWorkflow :42
// 【协作】 多走 ExecuteAgent activity → HTTP Python /agent/loop 单步；与
//   roles.AllowedTools 协同做可用工具过滤。
// =============================================================================

package workflows

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/activities"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// AgentWorkflowInput is the input for AgentWorkflow
type AgentWorkflowInput struct {
	AgentID  string                 `json:"agent_id"`
	Input    map[string]interface{} `json:"input"`
	UserID   string                 `json:"user_id"`
	TenantID string                 `json:"tenant_id"`
	// Optional: stream output events
	Stream bool `json:"stream,omitempty"`
	// ParentWorkflowID is used for unified event streaming when running as child workflow.
	// If set, events are emitted to the parent's workflow ID instead of this workflow's ID.
	ParentWorkflowID string `json:"parent_workflow_id,omitempty"`
}

// AgentWorkflowOutput is the output from AgentWorkflow
type AgentWorkflowOutput struct {
	Success       bool        `json:"success"`
	Output        interface{} `json:"output"`
	Error         string      `json:"error,omitempty"`
	AgentID       string      `json:"agent_id"`
	ToolName      string      `json:"tool_name"`
	ExecutionTime int         `json:"execution_time_ms"`
	CostUSD       float64     `json:"cost_usd"`
	TokensUsed    int         `json:"tokens_used,omitempty"`
}

// AgentWorkflow executes a single agent deterministically
// It wraps a tool call in a Temporal workflow for consistency and observability
func AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (*AgentWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)

	// Use parent workflow ID for event streaming if this is a child workflow,
	// otherwise use own workflow ID. This ensures events appear in parent's SSE stream.
	workflowID := input.ParentWorkflowID
	if workflowID == "" {
		workflowID = workflow.GetInfo(ctx).WorkflowExecution.ID
	}

	logger.Info("AgentWorkflow started",
		"agent_id", input.AgentID,
		"workflow_id", workflowID,
		"own_workflow_id", workflow.GetInfo(ctx).WorkflowExecution.ID,
	)

	// Set up activity options with appropriate timeout
	// LP analyze agents need longer timeouts for full-page captures with OCR
	timeout := 5 * time.Minute
	heartbeat := 3 * time.Minute
	if strings.HasPrefix(input.AgentID, "lp-") {
		timeout = 10 * time.Minute
		heartbeat = 5 * time.Minute
		logger.Info("Using extended timeout for LP agent",
			"agent_id", input.AgentID,
			"start_to_close", timeout.String(),
			"heartbeat", heartbeat.String())
	}

	activityOpts := workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		HeartbeatTimeout:    heartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, activityOpts)

	// Emit activity context for progress events
	emitCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})

	// Emit workflow started event
	_ = workflow.ExecuteActivity(emitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
		WorkflowID: workflowID,
		EventType:  activities.StreamEventWorkflowStarted,
		AgentID:    input.AgentID,
		Message:    activities.MsgAgentStarted(input.AgentID),
		Timestamp:  workflow.Now(ctx),
		Payload: map[string]interface{}{
			"agent_id": input.AgentID,
		},
	}).Get(ctx, nil)

	// Execute the agent activity
	execInput := activities.ExecuteAgentInput{
		AgentID:    input.AgentID,
		Input:      input.Input,
		WorkflowID: workflowID,
		UserID:     input.UserID,
		TenantID:   input.TenantID,
	}

	var execOutput activities.ExecuteAgentOutput
	err := workflow.ExecuteActivity(ctx, "ExecuteAgentActivity", execInput).Get(ctx, &execOutput)
	if err != nil {
		logger.Error("Agent execution activity failed", "error", err)

		// Emit error event
		_ = workflow.ExecuteActivity(emitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: workflowID,
			EventType:  activities.StreamEventErrorOccurred,
			AgentID:    input.AgentID,
			Message:    activities.MsgTaskFailed(err.Error()),
			Timestamp:  workflow.Now(ctx),
		}).Get(ctx, nil)

		return &AgentWorkflowOutput{
			Success: false,
			Error:   err.Error(),
			AgentID: input.AgentID,
		}, nil
	}

	// Handle agent execution failure (non-activity error)
	if !execOutput.Success {
		logger.Warn("Agent execution returned error", "error", execOutput.Error)

		// Emit error event
		_ = workflow.ExecuteActivity(emitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
			WorkflowID: workflowID,
			EventType:  activities.StreamEventErrorOccurred,
			AgentID:    input.AgentID,
			Message:    activities.MsgTaskFailed(execOutput.Error),
			Timestamp:  workflow.Now(ctx),
		}).Get(ctx, nil)

		return &AgentWorkflowOutput{
			Success:       false,
			Error:         execOutput.Error,
			AgentID:       execOutput.AgentID,
			ToolName:      execOutput.ToolName,
			ExecutionTime: execOutput.ExecutionTime,
		}, nil
	}

	// Emit completion event
	_ = workflow.ExecuteActivity(emitCtx, "EmitTaskUpdate", activities.EmitTaskUpdateInput{
		WorkflowID: workflowID,
		EventType:  activities.StreamEventWorkflowCompleted,
		AgentID:    input.AgentID,
		Message:    activities.MsgAgentCompleted(input.AgentID),
		Timestamp:  workflow.Now(ctx),
		Payload: map[string]interface{}{
			"agent_id":       input.AgentID,
			"tool_name":      execOutput.ToolName,
			"execution_time": execOutput.ExecutionTime,
			"cost_usd":       execOutput.CostUSD,
		},
	}).Get(ctx, nil)

	logger.Info("AgentWorkflow completed",
		"agent_id", input.AgentID,
		"tool_name", execOutput.ToolName,
		"execution_time_ms", execOutput.ExecutionTime,
		"cost_usd", execOutput.CostUSD,
	)

	return &AgentWorkflowOutput{
		Success:       true,
		Output:        execOutput.Output,
		AgentID:       execOutput.AgentID,
		ToolName:      execOutput.ToolName,
		ExecutionTime: execOutput.ExecutionTime,
		CostUSD:       execOutput.CostUSD,
		TokensUsed:    execOutput.TokensUsed,
	}, nil
}

// ConvertAgentInputFromTask converts TaskInput to AgentWorkflowInput
// This is used when routing from the orchestrator
func ConvertAgentInputFromTask(input TaskInput) (*AgentWorkflowInput, error) {
	if input.Context == nil {
		return nil, fmt.Errorf("context is required for agent workflow")
	}

	agentID, ok := input.Context["agent"].(string)
	if !ok || agentID == "" {
		return nil, fmt.Errorf("agent ID is required in context.agent")
	}

	// Load agent definition to get allowed fields from schema
	agentDef, err := activities.GetAgentDefinition(agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to load agent definition: %w", err)
	}

	// Build set of allowed fields and their schema definitions
	schemaProperties := getSchemaProperties(agentDef.InputSchema)

	agentInput := make(map[string]interface{})

	// Merge agent_input from context with type validation.
	if ai, ok := input.Context["agent_input"].(map[string]interface{}); ok && ai != nil {
		for k, v := range ai {
			// Only copy fields that are defined in the agent's schema
			propSchema, allowed := schemaProperties[k]
			if !allowed {
				continue
			}
			// Validate type matches schema
			if err := validateSchemaFieldType(k, v, propSchema); err == nil {
				agentInput[k] = v
			}
			// Silently skip fields with wrong type to prevent injection
		}
	}

	// Copy context fields to agent input, but ONLY if they are defined in the agent's schema
	// AND pass type validation. This prevents arbitrary field injection through context.
	for field, propSchema := range schemaProperties {
		if v, ok := input.Context[field]; ok {
			if existing, exists := agentInput[field]; !exists || existing == nil {
				// Validate type before accepting
				if err := validateSchemaFieldType(field, v, propSchema); err == nil {
					agentInput[field] = v
				}
			}
		}
	}

	// If query is provided, map it to the expected primary input for the agent.
	// Always map query to primary input field if that field is not already set.
	if input.Query != "" {
		switch agentID {
		case "serp-ads":
			if v, ok := agentInput["keywords"]; !ok || v == nil {
				agentInput["keywords"] = input.Query
			}
		case "competitor-discover":
			if v, ok := agentInput["keywords"]; !ok || v == nil {
				agentInput["keywords"] = []string{input.Query}
			}
		case "ads-transparency":
			_, hasAdvertiserID := agentInput["advertiser_id"]
			_, hasDomain := agentInput["domain"]
			if !hasAdvertiserID && !hasDomain {
				agentInput["domain"] = input.Query
			}
		case "lp-visual-analyze":
			if v, ok := agentInput["url"]; !ok || v == nil {
				agentInput["url"] = input.Query
			}
		case "lp-batch-analyze":
			if v, ok := agentInput["urls"]; !ok || v == nil {
				agentInput["urls"] = []string{input.Query}
			}
		case "keyword-extract":
			if v, ok := agentInput["query"]; !ok || v == nil {
				agentInput["query"] = input.Query
			}
		case "browser-screenshot":
			if v, ok := agentInput["url"]; !ok || v == nil {
				agentInput["url"] = input.Query
			}
		// Financial agents
		case "sec-filings", "twitter-sentiment":
			if v, ok := agentInput["ticker"]; !ok || v == nil {
				agentInput["ticker"] = input.Query
			}
		case "alpaca-news":
			if v, ok := agentInput["symbols"]; !ok || v == nil {
				agentInput["symbols"] = input.Query
			}
		}
	}

	// Agent-specific defaults.
	switch agentID {
	case "competitor-discover":
		if v, ok := agentInput["country"]; !ok || v == nil {
			agentInput["country"] = "us"
		} else if s, ok := v.(string); ok && s == "" {
			agentInput["country"] = "us"
		}
	}

	// Normalize URLs/domains based on agent requirements.
	// This allows users to provide URLs in any format (with or without scheme).
	normalizeAgentInput(agentID, agentInput)

	// Minimal required-field validation for agents that cannot infer parameters from query.
	switch agentID {
	case "serp-ads":
		kw, ok := agentInput["keywords"].(string)
		if !ok || kw == "" {
			return nil, fmt.Errorf("keywords is required for agent %s", agentID)
		}
	case "competitor-discover":
		if _, ok := agentInput["keywords"]; !ok {
			return nil, fmt.Errorf("keywords is required for agent %s", agentID)
		}
		country, ok := agentInput["country"].(string)
		if !ok || country == "" {
			return nil, fmt.Errorf("country is required for agent %s", agentID)
		}
	case "ad-creative-analyze":
		if v, ok := agentInput["ads"]; !ok || v == nil {
			return nil, fmt.Errorf("ads is required for agent %s (provide context.agent_input.ads)", agentID)
		}
	case "browser-screenshot":
		url, ok := agentInput["url"].(string)
		if !ok || url == "" {
			return nil, fmt.Errorf("url is required for agent %s", agentID)
		}
	case "keyword-extract":
		q, ok := agentInput["query"].(string)
		if !ok || q == "" {
			return nil, fmt.Errorf("query is required for agent %s", agentID)
		}
	case "sec-filings", "twitter-sentiment":
		ticker, ok := agentInput["ticker"].(string)
		if !ok || ticker == "" {
			return nil, fmt.Errorf("ticker is required for agent %s", agentID)
		}
	case "alpaca-news":
		symbols, ok := agentInput["symbols"].(string)
		if !ok || symbols == "" {
			return nil, fmt.Errorf("symbols is required for agent %s", agentID)
		}
	}

	return &AgentWorkflowInput{
		AgentID:          agentID,
		Input:            agentInput,
		UserID:           input.UserID,
		TenantID:         input.TenantID,
		ParentWorkflowID: input.ParentWorkflowID,
	}, nil
}

// AgentWorkflowOutputToTaskResult converts AgentWorkflowOutput to TaskResult
func AgentWorkflowOutputToTaskResult(output *AgentWorkflowOutput) TaskResult {
	if !output.Success {
		return TaskResult{
			Success:      false,
			ErrorMessage: output.Error,
		}
	}

	// Convert output to JSON string for result
	var resultStr string
	if outputBytes, err := json.Marshal(output.Output); err == nil {
		resultStr = string(outputBytes)
	} else {
		resultStr = fmt.Sprintf("%v", output.Output)
	}

	return TaskResult{
		Success: true,
		Result:  resultStr,
		Metadata: map[string]interface{}{
			"agent_id":       output.AgentID,
			"tool_name":      output.ToolName,
			"execution_time": output.ExecutionTime,
			"cost_usd":       output.CostUSD,
			"tokens_used":    output.TokensUsed,
		},
	}
}

// getSchemaProperties extracts property definitions from an InputSchema
// Returns a map of field name -> property schema for type validation
func getSchemaProperties(schema map[string]interface{}) map[string]map[string]interface{} {
	result := make(map[string]map[string]interface{})
	if schema == nil {
		return result
	}

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return result
	}

	for propName, propDef := range properties {
		if propSchema, ok := propDef.(map[string]interface{}); ok {
			result[propName] = propSchema
		}
	}
	return result
}

// validateSchemaFieldType validates a value against its schema definition
// Returns nil if valid, error if type mismatch
func validateSchemaFieldType(fieldName string, value interface{}, propSchema map[string]interface{}) error {
	if value == nil {
		return nil // nil values are acceptable (will be caught by required check)
	}

	expectedType, ok := propSchema["type"].(string)
	if !ok {
		return nil // No type specified, allow any
	}

	actualType := reflect.TypeOf(value)
	valid := false

	switch expectedType {
	case "string":
		_, valid = value.(string)
	case "integer":
		switch v := value.(type) {
		case int, int32, int64:
			valid = true
		case float64:
			// JSON numbers come as float64, accept if whole number
			valid = v == float64(int64(v))
		}
	case "number":
		switch value.(type) {
		case int, int32, int64, float32, float64:
			valid = true
		}
	case "boolean":
		_, valid = value.(bool)
	case "array":
		valid = actualType != nil && actualType.Kind() == reflect.Slice
	case "object":
		_, valid = value.(map[string]interface{})
	default:
		valid = true // Unknown type, allow
	}

	if !valid {
		return fmt.Errorf("field %s: expected type %s, got %T", fieldName, expectedType, value)
	}
	return nil
}

// normalizeToFullURL ensures a URL has a scheme (https:// by default).
// Used for tools that require full URLs (browser-screenshot, lp-analyze, lp-batch-analyze).
func normalizeToFullURL(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return input
	}
	lower := strings.ToLower(input)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "https://" + input
	}
	return input
}

// normalizeToDomain extracts just the domain from a URL or domain string.
// Used for tools that expect domain-only input (ads-transparency).
func normalizeToDomain(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return input
	}
	// Remove scheme
	input = strings.TrimPrefix(input, "https://")
	input = strings.TrimPrefix(input, "http://")
	// Remove www.
	input = strings.TrimPrefix(input, "www.")
	// Remove path (everything after first /)
	if idx := strings.Index(input, "/"); idx > 0 {
		input = input[:idx]
	}
	// Remove query string
	if idx := strings.Index(input, "?"); idx > 0 {
		input = input[:idx]
	}
	return input
}

// normalizeAgentInput applies URL/domain normalization based on agent type.
// This ensures users can provide URLs in any format and the backend handles it correctly.
func normalizeAgentInput(agentID string, agentInput map[string]interface{}) {
	switch agentID {
	// URL-required tools: ensure full URL with scheme
	case "browser-screenshot", "lp-visual-analyze":
		if url, ok := agentInput["url"].(string); ok && url != "" {
			agentInput["url"] = normalizeToFullURL(url)
		}

	case "lp-batch-analyze":
		if urls, ok := agentInput["urls"].([]interface{}); ok {
			normalized := make([]interface{}, len(urls))
			for i, u := range urls {
				if urlStr, ok := u.(string); ok {
					normalized[i] = normalizeToFullURL(urlStr)
				} else {
					normalized[i] = u
				}
			}
			agentInput["urls"] = normalized
		}
		// Also handle []string case
		if urls, ok := agentInput["urls"].([]string); ok {
			normalized := make([]string, len(urls))
			for i, u := range urls {
				normalized[i] = normalizeToFullURL(u)
			}
			agentInput["urls"] = normalized
		}

	// Domain-only tools: strip scheme and path
	case "ads-transparency":
		if domain, ok := agentInput["domain"].(string); ok && domain != "" {
			agentInput["domain"] = normalizeToDomain(domain)
		}
	}
}
