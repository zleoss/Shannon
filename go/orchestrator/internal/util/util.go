// =============================================================================
// 文件: go/orchestrator/internal/util/util.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   通用工具函数集 —— 字符串处理、ID 生成、格式转换等。
// 【关键内容】
//   TruncateString / GenerateID / ParseDuration / SanitizeInput
// 【协作关系】
//   被 orchestrator 内所有包引用，提供跨模块通用的工具方法。
// =============================================================================
package util

import (
	"strconv"
	"strings"
)

// ContainsString reports whether slice contains item.
func ContainsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// ParseNumericValue attempts to extract a numeric value from a free‑form string.
// Preference order: direct parse, "equals|is N" pattern, then last numeric token.
func ParseNumericValue(response string) (float64, bool) {
	response = strings.TrimSpace(response)
	if val, err := strconv.ParseFloat(response, 64); err == nil {
		return val, true
	}
	fields := strings.Fields(response)
	var numbers []float64
	for i := 0; i < len(fields); i++ {
		token := strings.Trim(fields[i], ".,!?:;")
		if v, err := strconv.ParseFloat(token, 64); err == nil {
			numbers = append(numbers, v)
		}
		if (strings.EqualFold(token, "equals") || strings.EqualFold(token, "is")) && i+1 < len(fields) {
			next := strings.Trim(fields[i+1], ".,!?:;")
			if v, err := strconv.ParseFloat(next, 64); err == nil {
				return v, true
			}
		}
	}
	if len(numbers) > 0 {
		return numbers[len(numbers)-1], true
	}
	return 0, false
}

// TruncateString truncates s to maxLen and appends "..." if truncated (UTF-8 safe).
// If preserveWords is true, truncates at the last space before maxLen when possible.
func TruncateString(s string, maxLen int, preserveWords bool) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	// Reserve space for ellipsis
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	cut := maxLen - 3
	if preserveWords {
		// Find last space before cut (in rune positions)
		if idx := lastSpaceBeforeRune(s, cut); idx > 0 {
			cut = idx
		}
	}
	return string(runes[:cut]) + "..."
}

func lastSpaceBefore(s string, pos int) int {
	if pos > len(s) {
		pos = len(s)
	}
	for i := pos - 1; i >= 0; i-- {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			return i
		}
	}
	return -1
}

// lastSpaceBeforeRune finds the last space before pos (in rune count, UTF-8 safe)
func lastSpaceBeforeRune(s string, pos int) int {
	runes := []rune(s)
	if pos > len(runes) {
		pos = len(runes)
	}
	for i := pos - 1; i >= 0; i-- {
		if runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\n' {
			return i
		}
	}
	return -1
}

// GetContextBool extracts a boolean value from a context map, handling both bool and string "true"/"false".
// This is needed because proto map<string, string> converts booleans to strings.
func GetContextBool(ctx map[string]interface{}, key string) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx[key]
	if !ok {
		return false
	}
	// Handle bool type
	if b, ok := v.(bool); ok {
		return b
	}
	// Handle string type ("true", "false", "1", "0")
	if s, ok := v.(string); ok {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "true" || s == "1"
	}
	return false
}

// GetContextInt extracts an integer value from a context map with a default fallback.
// Handles int, float64 (JSON numbers), and string representations.
func GetContextInt(ctx map[string]interface{}, key string, defaultVal int) int {
	if ctx == nil {
		return defaultVal
	}
	v, ok := ctx[key]
	if !ok {
		return defaultVal
	}
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return i
		}
	}
	return defaultVal
}
