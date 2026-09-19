// =============================================================================
// 文件: go/orchestrator/cmd/gateway/internal/handlers/llm_models.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   LLM 模型列表 handler —— 返回可用模型及定价信息。
// 【关键内容】
//   ListModels / GetModel / Model 元数据
// 【协作关系】
//   从 pricing 和 provider 注册表读取模型信息，返回给 OpenAI 兼容端点等。
// =============================================================================
package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"sync"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/auth"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// llmModelEntry represents a single LLM provider model.
type llmModelEntry struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Tier     string `json:"tier"`
	Priority int    `json:"priority"`
}

// llmModelsResponse is the response for GET /v1/llm-models.
type llmModelsResponse struct {
	Models []llmModelEntry `json:"models"`
}

// YAML parsing types for models.yaml

type modelsYAMLProvider struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Priority int    `yaml:"priority"`
}

type modelsYAMLTier struct {
	Providers []modelsYAMLProvider `yaml:"providers"`
}

type catalogModelMeta struct {
	Tier    string `yaml:"tier"`
	Enabled *bool  `yaml:"enabled,omitempty"`
}

type modelsYAMLConfig struct {
	ModelTiers   map[string]modelsYAMLTier              `yaml:"model_tiers"`
	ModelCatalog map[string]map[string]catalogModelMeta `yaml:"model_catalog"`
}

// LLMModelsHandler serves the list of available LLM provider models.
type LLMModelsHandler struct {
	configPath string
	logger     *zap.Logger

	once   sync.Once
	models []llmModelEntry
	err    error
}

// NewLLMModelsHandler creates a new LLM models handler.
func NewLLMModelsHandler(configPath string, logger *zap.Logger) *LLMModelsHandler {
	return &LLMModelsHandler{
		configPath: configPath,
		logger:     logger,
	}
}

// loadModels reads and parses models.yaml, caching the result.
func (h *LLMModelsHandler) loadModels() ([]llmModelEntry, error) {
	h.once.Do(func() {
		data, err := os.ReadFile(h.configPath)
		if err != nil {
			h.err = err
			return
		}

		var cfg modelsYAMLConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			h.err = err
			return
		}

		// Build priority lookup from model_tiers: "provider:model" -> priority
		tierPriority := map[string]int{}
		for _, tier := range cfg.ModelTiers {
			for _, p := range tier.Providers {
				key := p.Provider + ":" + p.Model
				tierPriority[key] = p.Priority
			}
		}

		// Providers the Python LLM service actually supports (must mirror type_map in manager.py)
		// Excludes: ollama (local-only, no server on EKS), meta (not implemented)
		supportedProviders := map[string]bool{
			"openai": true, "anthropic": true, "google": true, "groq": true,
			"xai": true, "zai": true, "kimi": true, "minimax": true,
			"deepseek": true, "qwen": true,
		}

		// Build entries from model_catalog (source of truth for callable models)
		var models []llmModelEntry
		if len(cfg.ModelCatalog) > 0 {
			for providerName, providerModels := range cfg.ModelCatalog {
				if !supportedProviders[providerName] {
					continue
				}
				for modelAlias, meta := range providerModels {
					if meta.Enabled != nil && !*meta.Enabled {
						continue
					}
					tier := meta.Tier
					if tier == "" {
						tier = "medium"
					}
					priority := tierPriority[providerName+":"+modelAlias]
					models = append(models, llmModelEntry{
						ID:       modelAlias,
						Provider: providerName,
						Tier:     tier,
						Priority: priority,
					})
				}
			}
		} else {
			// Fallback: use model_tiers directly if no catalog
			for tierName, tier := range cfg.ModelTiers {
				for _, p := range tier.Providers {
					models = append(models, llmModelEntry{
						ID:       p.Model,
						Provider: p.Provider,
						Tier:     tierName,
						Priority: p.Priority,
					})
				}
			}
		}

		// Stable sort: by tier (small < medium < large < unknown), then priority (lower first)
		tierOrder := map[string]int{"small": 1, "medium": 2, "large": 3}
		tierOrd := func(t string) int {
			if v, ok := tierOrder[t]; ok {
				return v
			}
			return 99
		}
		sort.SliceStable(models, func(i, j int) bool {
			ti, tj := tierOrd(models[i].Tier), tierOrd(models[j].Tier)
			if ti != tj {
				return ti < tj
			}
			// Priority 0 means not in model_tiers — sort to end
			pi, pj := models[i].Priority, models[j].Priority
			if pi == 0 {
				pi = 9999
			}
			if pj == 0 {
				pj = 9999
			}
			return pi < pj
		})

		h.models = models
	})
	return h.models, h.err
}

// ListLLMModels handles GET /v1/llm-models.
func (h *LLMModelsHandler) ListLLMModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := r.Context().Value(auth.UserContextKey).(*auth.UserContext); !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}

	models, err := h.loadModels()
	if err != nil {
		h.logger.Error("failed to load LLM models config", zap.Error(err))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to load model configuration"})
		return
	}

	// Optional tier filter
	tierFilter := r.URL.Query().Get("tier")
	result := models
	if tierFilter != "" {
		var filtered []llmModelEntry
		for _, m := range models {
			if m.Tier == tierFilter {
				filtered = append(filtered, m)
			}
		}
		result = filtered
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(llmModelsResponse{Models: result})
}
