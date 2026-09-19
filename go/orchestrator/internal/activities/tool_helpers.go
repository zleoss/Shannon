// =============================================================================
// 文件: go/orchestrator/internal/activities/tool_helpers.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   工具辅助函数 —— 解析、校验和格式化工具调用参数。
// 【关键内容】
//   ParseToolArgs / ValidateToolParams / FormatToolResult
// 【协作关系】
//   被 agent_executor.go 和各 tool activity 调用，作为工具调用的基础设施。
// =============================================================================
package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.uber.org/zap"
)

// ToolExecuteRequest is the request format for /tools/execute
type ToolExecuteRequest struct {
	ToolName   string                 `json:"tool_name"`
	Parameters map[string]interface{} `json:"parameters"`
}

// ToolExecuteResponse is the response from /tools/execute
type ToolExecuteResponse struct {
	Success       bool                   `json:"success"`
	Output        interface{}            `json:"output"`
	Error         string                 `json:"error,omitempty"`
	ExecutionTime int                    `json:"execution_time_ms,omitempty"`
	TokensUsed    int                    `json:"tokens_used,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// ExtractKeywordsInput is the input for keyword extraction
type ExtractKeywordsInput struct {
	Query    string `json:"query"`
	Language string `json:"language,omitempty"`
	Country  string `json:"country,omitempty"`
}

// ExtractKeywordsOutput is the output from keyword extraction
type ExtractKeywordsOutput struct {
	Keywords         []string `json:"keywords"`
	BrandKeywords    []string `json:"brand_keywords,omitempty"`
	DetectedLanguage string   `json:"detected_language,omitempty"`
	DetectedCountry  string   `json:"detected_country,omitempty"`
	ModelUsed        string   `json:"model_used,omitempty"`
	InputTokens      int      `json:"input_tokens,omitempty"`
	OutputTokens     int      `json:"output_tokens,omitempty"`
}

// ExtractSearchKeywords uses a small LLM to extract search keywords from a query
func ExtractSearchKeywords(ctx context.Context, input ExtractKeywordsInput) (ExtractKeywordsOutput, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Extracting search keywords", zap.String("query", input.Query))

	llmServiceURL := getenv("LLM_SERVICE_URL", "http://llm-service:8000")
	url := fmt.Sprintf("%s/agent/query", llmServiceURL)

	countryHint := ""
	if input.Country != "" {
		countryHint = fmt.Sprintf("\nTarget Market: %s (USE THIS COUNTRY'S LANGUAGE FOR CATEGORY KEYWORDS)", input.Country)
	}

	prompt := fmt.Sprintf(`You are a keyword researcher. Extract search keywords for discovery. Return JSON only.

Query: %s%s

Return format:
{"keywords": ["keyword1", "keyword2", "keyword3"], "brand_keywords": ["brand1", "brand2"], "language": "ja|en|zh", "country": "jp|us|cn"}

Rules:
- "keywords": 3-5 search keywords for commercial/transactional intent
- "brand_keywords": 1-3 exact entity names ONLY
- Detect language from query and infer country (ja->jp, zh->cn, en->us)
- Remove task instructions (分析して, 調べて, analyze, research)`, input.Query, countryHint)

	reqBody := map[string]interface{}{
		"query":      prompt,
		"model_tier": "small",
		"max_tokens": 200,
		"context":    map[string]interface{}{},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ExtractKeywordsOutput{Keywords: []string{input.Query}}, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ExtractKeywordsOutput{Keywords: []string{input.Query}}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Warn("Keyword extraction LLM call failed", zap.Error(err))
		return ExtractKeywordsOutput{Keywords: []string{input.Query}}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ExtractKeywordsOutput{Keywords: []string{input.Query}}, nil
	}

	var llmResp map[string]interface{}
	if err := json.Unmarshal(respBody, &llmResp); err != nil {
		return ExtractKeywordsOutput{Keywords: []string{input.Query}}, nil
	}

	responseText := ""
	if r, ok := llmResp["response"].(string); ok {
		responseText = r
	}

	output := ExtractKeywordsOutput{}

	jsonStart := strings.Index(responseText, "{")
	jsonEnd := strings.LastIndex(responseText, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		jsonStr := responseText[jsonStart : jsonEnd+1]
		var extracted map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &extracted); err == nil {
			if kws, ok := extracted["keywords"].([]interface{}); ok {
				for _, kw := range kws {
					if s, ok := kw.(string); ok && s != "" {
						output.Keywords = append(output.Keywords, s)
					}
				}
			}
			if bkws, ok := extracted["brand_keywords"].([]interface{}); ok {
				for _, kw := range bkws {
					if s, ok := kw.(string); ok && s != "" {
						output.BrandKeywords = append(output.BrandKeywords, s)
					}
				}
			}
			if lang, ok := extracted["language"].(string); ok {
				output.DetectedLanguage = lang
			}
			if country, ok := extracted["country"].(string); ok {
				output.DetectedCountry = country
			}
		}
	}

	if len(output.Keywords) == 0 {
		output.Keywords = []string{input.Query}
	}

	if model, ok := llmResp["model_used"].(string); ok {
		output.ModelUsed = model
	} else if model, ok := llmResp["model"].(string); ok {
		output.ModelUsed = model
	}
	if metadata, ok := llmResp["metadata"].(map[string]interface{}); ok {
		if inTok, ok := metadata["input_tokens"].(float64); ok {
			output.InputTokens = int(inTok)
		}
		if outTok, ok := metadata["output_tokens"].(float64); ok {
			output.OutputTokens = int(outTok)
		}
	}
	if output.InputTokens == 0 {
		if inTok, ok := llmResp["input_tokens"].(float64); ok {
			output.InputTokens = int(inTok)
		}
	}
	if output.OutputTokens == 0 {
		if outTok, ok := llmResp["output_tokens"].(float64); ok {
			output.OutputTokens = int(outTok)
		}
	}
	if output.InputTokens == 0 || output.OutputTokens == 0 {
		if usage, ok := llmResp["usage"].(map[string]interface{}); ok {
			if output.InputTokens == 0 {
				if inTok, ok := usage["input_tokens"].(float64); ok {
					output.InputTokens = int(inTok)
				}
			}
			if output.OutputTokens == 0 {
				if outTok, ok := usage["output_tokens"].(float64); ok {
					output.OutputTokens = int(outTok)
				}
			}
		}
	}

	logger.Info("Keywords extracted",
		zap.Strings("keywords", output.Keywords),
		zap.String("language", output.DetectedLanguage),
		zap.String("country", output.DetectedCountry),
	)
	return output, nil
}

// callToolWithTimeout makes a request to the LLM service /tools/execute endpoint with custom timeout
func callToolWithTimeout(ctx context.Context, toolName string, params map[string]interface{}, timeout time.Duration) (*ToolExecuteResponse, error) {
	llmServiceURL := getenv("LLM_SERVICE_URL", "http://llm-service:8000")
	url := fmt.Sprintf("%s/tools/execute", llmServiceURL)

	reqBody := ToolExecuteRequest{
		ToolName:   toolName,
		Parameters: params,
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
