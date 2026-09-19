// =============================================================================
// 文件: go/orchestrator/internal/activities/stream_messages.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   流式消息处理 —— 构建/格式化流式输出的消息事件。
// 【关键内容】
//   FormatStreamMessage / 消息截断、事件序列化
// 【协作关系】
//   被 stream_events.go 调用，生成供 Redis pubsub 发布的事件负载。
// =============================================================================
package activities

import (
	"fmt"
	"strings"
)

// compactTokens formats a token count into a compact human form, e.g. 14800 -> "14.8k".
func compactTokens(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	// one decimal precision for thousands
	k := float64(tokens) / 1000.0
	// Trim trailing .0
	s := fmt.Sprintf("%.1fk", k)
	s = strings.ReplaceAll(s, ".0k", "k")
	return s
}

// msgTruncate shortens a string for display, adding ellipsis if needed.
func msgTruncate(s string, maxLen int) string {
	return truncateQuery(s, maxLen)
}

// -----------------------------------------------------------------------------
// Workflow Lifecycle Messages
// -----------------------------------------------------------------------------

// MsgWorkflowStarted announces workflow initialization.
func MsgWorkflowStarted() string { return "Starting up" }

// MsgWorkflowRouting indicates routing to a specific workflow type.
func MsgWorkflowRouting() string {
	return "Planning the approach"
}

// MsgWorkflowCompleted announces successful workflow completion.
func MsgWorkflowCompleted() string { return "All done" }

// MsgStreamEnd marks the end of streaming.
func MsgStreamEnd() string { return "Done" }

// -----------------------------------------------------------------------------
// Task Processing Messages
// -----------------------------------------------------------------------------

// MsgThinking shows the system is analyzing the query.
func MsgThinking(query string) string {
	truncated := msgTruncate(query, 50)
	return fmt.Sprintf("Thinking about: %s", truncated)
}

// MsgProcessing indicates active processing.
func MsgProcessing() string { return "Working on it" }

// MsgTaskDone indicates task completion.
func MsgTaskDone() string { return "Done" }

// MsgTaskFailed reports task failure with reason.
func MsgTaskFailed(reason string) string {
	if reason == "" {
		return "Something went wrong"
	}
	// Truncate long error messages
	if len(reason) > 100 {
		reason = reason[:100] + "..."
	}
	return fmt.Sprintf("Hit an issue: %s", reason)
}

// -----------------------------------------------------------------------------
// Planning & Decomposition Messages
// -----------------------------------------------------------------------------

// MsgPlanCreated announces plan creation with step count.
func MsgPlanCreated(steps int) string {
	if steps == 1 {
		return "Got a plan — 1 step"
	}
	return fmt.Sprintf("Got a plan — %d steps", steps)
}

// MsgUnderstandingRequest indicates decomposition is in progress.
func MsgUnderstandingRequest() string { return "Understanding your request" }

// MsgDecompositionFailed indicates fallback to simple execution.
func MsgDecompositionFailed() string { return "Handling this directly" }

// MsgRoleAssigned announces role assignment with available tools.
func MsgRoleAssigned(role string) string {
	return fmt.Sprintf("Ready as %s", role)
}

// -----------------------------------------------------------------------------
// Agent Messages
// -----------------------------------------------------------------------------

// MsgContextPreparing returns a short status for context preparation.
func MsgContextPreparing(_ int, _ int) string { return "Gathering context" }

// MsgAgentRunning announces an agent role starting work.
func MsgAgentRunning(role string) string {
	if role == "" {
		return "Agent working"
	}
	return fmt.Sprintf("%s is on it", role)
}

// MsgAgentThinking indicates agent is analyzing.
func MsgAgentThinking() string { return "Thinking" }

// MsgAgentWorking indicates agent is actively working.
func MsgAgentWorking(agentName string) string {
	if agentName == "" {
		return "Working"
	}
	return fmt.Sprintf("%s working", agentName)
}

// -----------------------------------------------------------------------------
// Progress Messages
// -----------------------------------------------------------------------------

// MsgProgress reports step progress.
func MsgProgress(step, total int) string {
	if total <= 0 {
		total = 1
	}
	if step < 0 {
		step = 0
	}
	if step > total {
		step = total
	}
	return fmt.Sprintf("Step %d of %d done", step, total)
}

// MsgBudget reports a simple budget usage.
func MsgBudget(used, limit int) string {
	if limit <= 0 {
		return "Processing"
	}
	pct := (used * 100) / limit
	return fmt.Sprintf("Progress: %d%% of budget used", pct)
}

// MsgTokensUsed reports token consumption.
func MsgTokensUsed() string {
	return "Processing complete"
}

// -----------------------------------------------------------------------------
// Handoff Messages
// -----------------------------------------------------------------------------

// MsgHandoffTemplate announces handoff to a template workflow.
func MsgHandoffTemplate(templateName string) string {
	return fmt.Sprintf("Running workflow: %s", templateName)
}

// MsgHandoffSimple announces handoff to simple task workflow.
func MsgHandoffSimple() string { return "Processing" }

// MsgHandoffSupervisor announces handoff to supervisor workflow.
func MsgHandoffSupervisor() string { return "Coordinating agents" }

// MsgHandoffTeamPlan announces handoff to team plan workflow.
func MsgHandoffTeamPlan() string { return "Running team plan" }

// -----------------------------------------------------------------------------
// Synthesis Messages
// -----------------------------------------------------------------------------

// MsgCompressionApplied notes that trimming was applied.
func MsgCompressionApplied() string { return "Condensing the conversation" }

// MsgCombiningResults announces synthesis.
func MsgCombiningResults() string { return "Pulling it all together" }

// MsgSynthesizing indicates synthesis in progress.
func MsgSynthesizing() string { return "Combining results" }

// MsgFinalAnswer announces the final answer is ready.
func MsgFinalAnswer() string { return "Answer ready" }

// MsgSynthesisSummary reports synthesis completion with token count.
func MsgSynthesisSummary() string {
	return "Wrapping up"
}

// MsgSynthesisFallback warns that synthesis used a simpler approach.
func MsgSynthesisFallback(_ string) string {
	return "Summarizing results"
}

// -----------------------------------------------------------------------------
// Memory Messages
// -----------------------------------------------------------------------------

// MsgWaiting reports a generic waiting status.
func MsgWaiting(_ string) string { return "Waiting on previous step" }

// MsgMemoryRecalled reports number of memory items recalled.
func MsgMemoryRecalled(items int) string {
	if items == 1 {
		return "Recalled 1 past note"
	}
	return fmt.Sprintf("Recalled %d past notes", items)
}

// MsgSummaryAdded indicates a previous context summary was injected.
func MsgSummaryAdded() string { return "Loaded prior context" }

// -----------------------------------------------------------------------------
// Approval Messages
// -----------------------------------------------------------------------------

// MsgApprovalRequested indicates approval is needed.
func MsgApprovalRequested(reason, approvalID string) string {
	if reason != "" {
		return fmt.Sprintf("Needs approval: %s", reason)
	}
	return "Needs your approval"
}

// MsgApprovalProcessed reports approval decision.
func MsgApprovalProcessed(decision string) string {
	switch strings.ToLower(decision) {
	case "approved":
		return "Approved — continuing"
	case "denied", "rejected":
		return "Denied — stopping"
	default:
		return fmt.Sprintf("Approval %s", decision)
	}
}

// -----------------------------------------------------------------------------
// Supervisor Messages
// -----------------------------------------------------------------------------

// MsgSupervisorStarted announces supervisor workflow start.
func MsgSupervisorStarted() string { return "Coordinating agents" }

// MsgSubtaskProgress reports subtask completion progress.
func MsgSubtaskProgress(completed, total int) string {
	return fmt.Sprintf("%d of %d tasks done", completed, total)
}

// MsgSubtasksFailed reports too many subtask failures.
func MsgSubtasksFailed(failed, total int) string {
	return fmt.Sprintf("Too many failures (%d/%d) — stopping", failed, total)
}

// -----------------------------------------------------------------------------
// React Pattern Messages
// -----------------------------------------------------------------------------

// MsgReactIteration reports React loop progress.
func MsgReactIteration(current, max int) string {
	return fmt.Sprintf("Thinking step %d of %d", current, max)
}

// MsgReactReasoning indicates reasoning phase started.
func MsgReactReasoning() string { return "Reflecting on progress" }

// MsgReactReasoningDone indicates reasoning phase completed.
func MsgReactReasoningDone() string { return "Decided next step" }

// MsgReactActing indicates action phase started.
func MsgReactActing() string { return "Taking action" }

// MsgReactActingDone indicates action phase completed.
func MsgReactActingDone() string { return "Action done" }

// MsgReactLoopDone indicates the React loop finished.
func MsgReactLoopDone() string { return "Finished thinking" }

// -----------------------------------------------------------------------------
// Tool Observation Messages
// -----------------------------------------------------------------------------

// MsgToolCompleted generates a human-readable message for tool completion.
func MsgToolCompleted(toolName string, response string) string {
	if response == "" {
		return fmt.Sprintf("%s done", humanizeToolName(toolName))
	}

	// Extract first meaningful line or sentence
	summary := extractSummary(response, 80)
	if summary == "" {
		return fmt.Sprintf("%s done", humanizeToolName(toolName))
	}

	return fmt.Sprintf("%s: %s", humanizeToolName(toolName), summary)
}

// MsgToolFailed generates a human-readable message for tool failure.
func MsgToolFailed(toolName string) string {
	return fmt.Sprintf("%s failed", humanizeToolName(toolName))
}

// humanizeToolName converts tool_name to "Tool Name" format.
func humanizeToolName(toolName string) string {
	switch toolName {
	case "web_search":
		return "Search"
	case "web_fetch":
		return "Fetch"
	case "calculator":
		return "Calculator"
	case "python_code", "python_executor", "code_executor":
		return "Code"
	case "read_file", "file_reader", "file_read":
		return "File read"
	case "file_write":
		return "File write"
	case "file_list":
		return "File list"
	case "code_reader":
		return "Code review"
	case "ga4_run_report":
		return "GA4 report"
	case "yahoo_jp_ads":
		return "Yahoo ads lookup"
	case "meta_ad_library":
		return "Meta ads lookup"
	case "ads_competitor_discover":
		return "Competitor ads search"
	case "ads_transparency_search":
		return "Ads transparency search"
	case "page_screenshot":
		return "Taking screenshot"
	case "browser":
		return "Browsing"
	default:
		return strings.Title(strings.ReplaceAll(toolName, "_", " "))
	}
}

// HumanizeToolName is the exported version of humanizeToolName.
func HumanizeToolName(toolName string) string {
	return humanizeToolName(toolName)
}

// -----------------------------------------------------------------------------
// Swarm Workflow Messages
// -----------------------------------------------------------------------------

// MsgSwarmStarted announces swarm workflow initialization.
func MsgSwarmStarted() string { return "Assembling the team" }

// MsgSwarmPlanning announces task decomposition.
func MsgSwarmPlanning() string { return "Thinking about the approach..." }

// MsgSwarmSpawning announces agents being assigned.
func MsgSwarmSpawning(count int) string {
	if count == 1 {
		return "1 agent is on it"
	}
	return fmt.Sprintf("%d agents are on it", count)
}

// MsgSwarmMonitoring announces agent coordination phase.
func MsgSwarmMonitoring() string { return "Agents working in parallel" }

// MsgSwarmSynthesizing announces result combination.
func MsgSwarmSynthesizing(count int) string {
	return fmt.Sprintf("Combining findings from %d agents", count)
}

// MsgSwarmCompleted announces swarm completion.
func MsgSwarmCompleted() string { return "All done" }

// MsgAgentStarted announces an agent beginning work.
func MsgAgentStarted(agentName string) string {
	return fmt.Sprintf("%s is on it", agentName)
}

// MsgAgentCompleted announces an agent finishing work.
func MsgAgentCompleted(agentName string) string {
	return fmt.Sprintf("%s is done", agentName)
}

// humanizeAction converts a raw action name to user-friendly text.
func humanizeAction(action string) string {
	switch action {
	case "tool_call":
		return "researching"
	case "done":
		return "wrapping up"
	case "send_message":
		return "coordinating with team"
	case "publish_data":
		return "reporting back"
	case "idle":
		return "waiting for next task"
	case "request_help":
		return "requesting help"
	default:
		// For tool-specific actions like "web_search", "file_write"
		return humanizeToolName(action)
	}
}

// MsgAgentProgress reports agent iteration progress in UX-friendly terms.
func MsgAgentProgress(agentName string, step, total int, action string) string {
	friendly := humanizeAction(action)
	return fmt.Sprintf("%s — %s (%d/%d)", agentName, friendly, step, total)
}

// -----------------------------------------------------------------------------
// Citation Messages
// -----------------------------------------------------------------------------

// MsgCitationSkipped reports citation enrichment was skipped.
func MsgCitationSkipped() string { return "Skipping source attribution" }

// -----------------------------------------------------------------------------
// Browser Pattern Messages
// -----------------------------------------------------------------------------

// MsgBrowserStarted announces browser automation start.
func MsgBrowserStarted() string { return "Opening browser" }

// MsgBrowserAction reports browser iteration progress.
func MsgBrowserAction(step, total int) string {
	return fmt.Sprintf("Browser action %d of %d", step, total)
}

// MsgBrowserAnalyzing indicates page analysis.
func MsgBrowserAnalyzing() string { return "Analyzing page" }

// MsgBrowserCompleted announces browser automation completion.
func MsgBrowserCompleted() string { return "Browser task done" }

// -----------------------------------------------------------------------------
// Research Messages
// -----------------------------------------------------------------------------

// MsgResearchConfirmed announces research direction confirmed by user.
func MsgResearchConfirmed() string { return "Got it — starting research" }

// MsgResearchTimedOut announces research plan review timed out.
func MsgResearchTimedOut() string { return "Review timed out" }

// extractSummary extracts a brief summary from response text.
func extractSummary(text string, maxLen int) string {
	// Clean up the text
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Try to get first line that's not empty or a header
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip empty lines, headers, and JSON-like content
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "{") {
			continue
		}
		// Found a good line
		return truncateQuery(line, maxLen)
	}

	// Fallback: truncate the whole text
	return truncateQuery(text, maxLen)
}

// -----------------------------------------------------------------------------
// Workflow Control Messages (pause/resume/cancel)
// -----------------------------------------------------------------------------

// MsgWorkflowPausing formats a message when pause signal is received.
func MsgWorkflowPausing(reason string) string {
	if reason == "" {
		return "Pausing..."
	}
	return fmt.Sprintf("Pausing: %s", reason)
}

// MsgWorkflowPaused formats a message when workflow is blocked at checkpoint.
func MsgWorkflowPaused() string {
	return "Paused"
}

// MsgWorkflowResumed formats a message when workflow resumes.
func MsgWorkflowResumed(reason string) string {
	if reason == "" {
		return "Resuming"
	}
	return fmt.Sprintf("Resuming: %s", reason)
}

// MsgWorkflowCancelling formats a message when cancel signal is received.
func MsgWorkflowCancelling(reason string) string {
	if reason == "" {
		return "Cancelling..."
	}
	return fmt.Sprintf("Cancelling: %s", reason)
}

// MsgWorkflowCancelled formats a message when workflow is cancelled at checkpoint.
func MsgWorkflowCancelled(reason string) string {
	if reason == "" {
		return "Cancelled"
	}
	return fmt.Sprintf("Cancelled: %s", reason)
}
