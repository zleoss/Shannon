// =============================================================================
// 文件: go/orchestrator/internal/activities/truncation.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   流式消息截断常量与工具 —— 统一管理消息长度限制。
// 【关键内容】
//   MaxMessageLength / TruncateMessage / 截断策略
// 【协作关系】
//   被 stream_messages.go 和各流式输出环节引用，确保消息不超限。
// =============================================================================
package activities

// Centralized truncation limits for streaming messages.
// Keep these values consistent across agent and synthesis paths.
const (
	// Max length for synthesized final content in SSE.
	MaxSynthesisOutputChars = 10000

	// Max length for agent LLM final outputs in SSE.
	MaxLLMOutputChars = 10000

	// Max length for prompts displayed/logged in SSE (sanitized).
	MaxPromptChars = 5000

	// Max length for "thinking" status snippets.
	MaxThinkingChars = 200
)
