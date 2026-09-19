// =============================================================================
// 文件: go/orchestrator/internal/metadata/aggregate.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   元数据聚合 —— 汇总来自多个源的元数据并去重。
// 【关键内容】
//   AggregateMetadata / 合并/去重/排序
// 【协作关系】
//   被合成和研究 workflow 调用，整合各 agent 返回的元数据。
// =============================================================================
package metadata

import (
    "sort"

    "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/activities"
    "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/models"
    "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/pricing"
)

// AggregateAgentMetadata extracts model, provider, and token information from agent results.
// It is a legacy helper used to build per-task metadata.
// NOTE: agent_usages emitted here is best-effort and deprecated for external consumption.
// For complete, authoritative usage and cost breakdowns, use model_breakdown from the token_usage table.
func AggregateAgentMetadata(agentResults []activities.AgentExecutionResult, synthesisTokens int) map[string]interface{} {
	meta := make(map[string]interface{})

	if len(agentResults) == 0 {
		return meta
	}

	// Find the primary model (from first successful agent or most used model)
	var primaryModel string
	// Track providers reported by agents (prefer real provider over detection)
	providerCounts := make(map[string]int)
	totalInputTokens := 0
	totalOutputTokens := 0
	totalTokensUsed := 0
	totalCacheRead := 0
	totalCacheCreation := 0
	modelCounts := make(map[string]int)

	// Per-agent usage details for visibility
	agentUsages := make([]map[string]interface{}, 0, len(agentResults))

	// Track tool invocations across all agents
	totalToolsInvoked := 0
	toolNames := make(map[string]bool)

	for _, result := range agentResults {
		// Count tool executions
		totalToolsInvoked += len(result.ToolExecutions)
		for _, te := range result.ToolExecutions {
			toolNames[te.Tool] = true
		}
		if result.Success && result.ModelUsed != "" {
			modelCounts[result.ModelUsed]++
			if primaryModel == "" {
				primaryModel = result.ModelUsed
			}
		}
		// Count provider if present
		if result.Success {
			if p := result.Provider; p != "" {
				providerCounts[p]++
			}
		}
		totalInputTokens += result.InputTokens
		totalOutputTokens += result.OutputTokens
		totalTokensUsed += result.TokensUsed
		totalCacheRead += result.CacheReadTokens
		totalCacheCreation += result.CacheCreationTokens

		// Record per-agent usage
        if result.Success {
            agentUsage := map[string]interface{}{
                "agent_id": result.AgentID,
            }
            if result.ModelUsed != "" {
                agentUsage["model"] = result.ModelUsed
            }
            // Tokens and per-agent cost
            var cost float64
            if result.InputTokens > 0 || result.OutputTokens > 0 {
                agentUsage["input_tokens"] = result.InputTokens
                agentUsage["output_tokens"] = result.OutputTokens
                total := result.InputTokens + result.OutputTokens
                agentUsage["total_tokens"] = total
                cost = pricing.CostForSplit(result.ModelUsed, result.InputTokens, result.OutputTokens)
            } else if result.TokensUsed > 0 {
                agentUsage["total_tokens"] = result.TokensUsed
                cost = pricing.CostForTokens(result.ModelUsed, result.TokensUsed)
            }
            agentUsage["cost_usd"] = cost
            if result.CacheReadTokens > 0 {
                agentUsage["cache_read_tokens"] = result.CacheReadTokens
            }
            if result.CacheCreationTokens > 0 {
                agentUsage["cache_creation_tokens"] = result.CacheCreationTokens
            }
            agentUsages = append(agentUsages, agentUsage)
        }
	}

	// Use the most frequently used model if available
	maxCount := 0
	for model, count := range modelCounts {
		if count > maxCount {
			maxCount = count
			primaryModel = model
		}
	}

	// Populate metadata
	if primaryModel != "" {
		meta["model"] = primaryModel
		meta["model_used"] = primaryModel
		// Prefer the most frequent non-empty provider from agent results; fallback to detection
		topProvider := ""
		maxProv := 0
		for prov, c := range providerCounts {
			if c > maxProv {
				maxProv = c
				topProvider = prov
			}
		}
		if topProvider != "" {
			meta["provider"] = topProvider
		} else {
			meta["provider"] = models.DetectProvider(primaryModel)
		}
	}

	// Add cache stats
	if totalCacheRead > 0 {
		meta["cache_read_tokens"] = totalCacheRead
	}
	if totalCacheCreation > 0 {
		meta["cache_creation_tokens"] = totalCacheCreation
	}

	// Add token breakdown
	// Prefer split tokens when available, fallback to TokensUsed sum
	if totalInputTokens > 0 || totalOutputTokens > 0 {
		meta["input_tokens"] = totalInputTokens
		meta["output_tokens"] = totalOutputTokens
		totalTokens := totalInputTokens + totalOutputTokens + synthesisTokens
		meta["total_tokens"] = totalTokens
		// cache_aware_total_tokens parallels total_tokens but adds prompt-cache
		// classes for quota accounting (see migration 121). Synthesis tokens
		// have no cache component so they only appear in the input+output sum.
		meta["cache_aware_total_tokens"] = totalTokens + totalCacheRead + totalCacheCreation
	} else if totalTokensUsed > 0 {
		// Fallback: use TokensUsed when splits unavailable
		// Estimate 60/40 split for input/output
		totalTokens := totalTokensUsed + synthesisTokens
		meta["input_tokens"] = int(float64(totalTokensUsed) * 0.6)
		meta["output_tokens"] = int(float64(totalTokensUsed) * 0.4)
		meta["total_tokens"] = totalTokens
		meta["cache_aware_total_tokens"] = totalTokens + totalCacheRead + totalCacheCreation
	}

    // Do not set cost_usd here; server or workflow computes accurately from pricing

	// Include per-agent usage details when present.
	// NOTE: agent_usages is deprecated; consumers should prefer model_breakdown.
	if len(agentUsages) > 0 {
		meta["agent_usages"] = agentUsages
	}

	// Include tool invocation count
	if totalToolsInvoked > 0 {
		meta["tools_invoked"] = totalToolsInvoked
		// Also include list of unique tools used (sorted for deterministic output)
		tools := make([]string, 0, len(toolNames))
		for tool := range toolNames {
			tools = append(tools, tool)
		}
		sort.Strings(tools)
		meta["tools_used"] = tools
	}

	return meta
}
