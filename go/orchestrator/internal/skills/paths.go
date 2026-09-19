// =============================================================================
// 文件: go/orchestrator/internal/skills/paths.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Skill 搜索路径解析：内置默认目录与 SKILLS_PATH 环境变量覆盖。
// 【关键内容】
//   DefaultSkillDirs :11
//   ResolveSkillDirs 合并默认与环境变量 :23
//   SkillsPathEnvVar = "SKILLS_PATH"
// 【协作关系】
//   被 skills.Registry 在加载阶段调用以决定扫描目录。
// =============================================================================

package skills

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultSkillDirs are the default directories to search for skills.
// These are tried in order; missing directories are skipped.
var DefaultSkillDirs = []string{
	"config/skills/core",      // Development: relative to working directory
	"/app/config/skills/core", // Container: mounted config path
}

// ResolveSkillDirs returns the skill directories to scan.
//
// If SKILLS_PATH environment variable is set, it's used as a path-separated
// list of directories (like PATH). Otherwise, DefaultSkillDirs are used.
//
// Order matters for potential future override semantics. For now, we load
// everything and reject duplicates.
func ResolveSkillDirs() []string {
	if env := strings.TrimSpace(os.Getenv("SKILLS_PATH")); env != "" {
		return splitSearchPaths(env)
	}
	return DefaultSkillDirs
}

// splitSearchPaths splits a path-list string (like PATH) into individual paths.
// Empty entries are skipped and paths are cleaned.
func splitSearchPaths(value string) []string {
	parts := strings.Split(value, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

// SkillsPathEnvVar is the environment variable name for custom skill paths.
const SkillsPathEnvVar = "SKILLS_PATH"
