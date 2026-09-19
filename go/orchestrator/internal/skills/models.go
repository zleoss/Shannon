// Package skills implements a markdown-based skill system compatible with
// Anthropic's Agent Skills specification.
//
// Skills are markdown files with YAML frontmatter that define reusable
// system prompts, tool requirements, and execution constraints.
// =============================================================================
// 文件: go/orchestrator/internal/skills/models.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Skill 数据模型 —— 定义 skill 的结构体、元数据与序列化。
// 【关键内容】
//   Skill / SkillMeta / Frontmatter 解析后的内存表示
// 【协作关系】
//   被 loader/registry 引用，作为整个 skill 系统的核心数据模型。
// =============================================================================
package skills

import (
	"sync"
	"time"
)

// Skill represents a parsed skill definition from a markdown file.
type Skill struct {
	Name          string                 `yaml:"name"`
	Version       string                 `yaml:"version"`
	Author        string                 `yaml:"author"`
	Category      string                 `yaml:"category"`
	Description   string                 `yaml:"description"`
	RequiresTools []string               `yaml:"requires_tools"`
	RequiresRole  string                 `yaml:"requires_role"`
	BudgetMax     int                    `yaml:"budget_max"`
	Dangerous     bool                   `yaml:"dangerous"`
	Enabled       bool                   `yaml:"enabled"`
	Metadata      map[string]interface{} `yaml:"metadata"`
	Content       string                 `yaml:"-"` // Markdown content after frontmatter
}

// SkillRegistry manages loaded skills with thread-safe access.
type SkillRegistry struct {
	mu         sync.RWMutex
	skills     map[string]SkillEntry   // Key: "name@version" or "name" for latest
	byCategory map[string][]string     // Category -> skill keys
	byName     map[string][]SkillEntry // Name -> all versions (sorted by version desc)
}

// SkillEntry wraps a skill with loading metadata.
type SkillEntry struct {
	Key         string
	Skill       *Skill
	SourcePath  string
	ContentHash string // SHA256 of file content
	LoadedAt    time.Time
}

// SkillSummary is a lightweight representation for API responses.
type SkillSummary struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Category      string   `json:"category"`
	Description   string   `json:"description"`
	RequiresTools []string `json:"requires_tools"`
	Dangerous     bool     `json:"dangerous"`
	Enabled       bool     `json:"enabled"`
}

// SkillDetail includes full skill information for API responses.
type SkillDetail struct {
	Skill    *Skill                 `json:"skill"`
	Metadata map[string]interface{} `json:"metadata"`
}
