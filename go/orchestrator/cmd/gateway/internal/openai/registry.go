// =============================================================================
// 文件: go/orchestrator/cmd/gateway/internal/openai/registry.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   OpenAI 兼容端点路由注册 —— 将 OpenAI API 路径映射到内部 handler。
// 【关键内容】
//   Handler 注册 / 路由表构建
// 【协作关系】
//   在 Gateway 启动时调用，动态注册到 HTTP 路由树。
// =============================================================================
package openai

import (
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ModelConfig represents a single model's configuration.
type ModelConfig struct {
	WorkflowMode     string                 `yaml:"workflow_mode"`      // simple, research, supervisor
	Context          map[string]interface{} `yaml:"context"`            // Shannon context to inject
	Description      string                 `yaml:"description"`        // Human-readable description
	MaxTokensDefault int                    `yaml:"max_tokens_default"` // Default max_tokens
}

// RegistryConfig represents the full model registry configuration.
type RegistryConfig struct {
	Models       map[string]ModelConfig `yaml:"models"`
	DefaultModel string                 `yaml:"default_model"`
	Settings     struct {
		MaxTokensLimit     int              `yaml:"max_tokens_limit"`
		DefaultTemperature float64          `yaml:"default_temperature"`
		SessionTTL         int              `yaml:"session_ttl"`
	} `yaml:"settings"`
}

// Registry manages OpenAI-compatible model mappings.
type Registry struct {
	config  *RegistryConfig
	mu      sync.RWMutex
	created time.Time
}

var (
	globalRegistry     *Registry
	globalRegistryOnce sync.Once
	globalRegistryErr  error
)

// GetRegistry returns the singleton model registry.
func GetRegistry() (*Registry, error) {
	globalRegistryOnce.Do(func() {
		globalRegistry, globalRegistryErr = loadRegistry()
	})
	return globalRegistry, globalRegistryErr
}

// loadRegistry loads the model registry from configuration files.
func loadRegistry() (*Registry, error) {
	candidates := []string{
		"config/openai_models.yaml",
		"/app/config/openai_models.yaml",
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %w", path, err)
			}

			var config RegistryConfig
			if err := yaml.Unmarshal(data, &config); err != nil {
				return nil, fmt.Errorf("failed to parse %s: %w", path, err)
			}

			// Apply defaults
			if config.DefaultModel == "" {
				config.DefaultModel = "shannon-chat"
			}
			if config.Settings.MaxTokensLimit == 0 {
				config.Settings.MaxTokensLimit = 16384
			}
			if config.Settings.DefaultTemperature == 0 {
				config.Settings.DefaultTemperature = 0.7
			}
			if config.Settings.SessionTTL == 0 {
				config.Settings.SessionTTL = 86400
			}

			return &Registry{
				config:  &config,
				created: time.Now(),
			}, nil
		}
	}

	// Return default registry if no config file found
	return newDefaultRegistry(), nil
}

// newDefaultRegistry creates a registry with built-in defaults.
func newDefaultRegistry() *Registry {
	config := &RegistryConfig{
		DefaultModel: "shannon-chat",
		Models: map[string]ModelConfig{
			"shannon-deep-research": {
				WorkflowMode: "research",
				Context: map[string]interface{}{
					"force_research":             true,
					"research_strategy":          "deep",
					"iterative_research_enabled": true,
					"iterative_max_iterations":   3,
				},
				Description:      "Deep research with iterative refinement",
				MaxTokensDefault: 8192,
			},
			"shannon-standard-research": {
				WorkflowMode: "research",
				Context: map[string]interface{}{
					"force_research":    true,
					"research_strategy": "standard",
				},
				Description:      "Balanced research with moderate depth",
				MaxTokensDefault: 4096,
			},
			"shannon-quick-research": {
				WorkflowMode: "research",
				Context: map[string]interface{}{
					"force_research":    true,
					"research_strategy": "quick",
				},
				Description:      "Fast research for simple queries",
				MaxTokensDefault: 4096,
			},
			"shannon-chat": {
				WorkflowMode:     "simple",
				Context:          map[string]interface{}{},
				Description:      "General chat completion",
				MaxTokensDefault: 4096,
			},
			"shannon-complex": {
				WorkflowMode:     "supervisor",
				Context:          map[string]interface{}{},
				Description:      "Multi-agent orchestration for complex tasks",
				MaxTokensDefault: 8192,
			},
		},
	}
	config.Settings.MaxTokensLimit = 16384
	config.Settings.DefaultTemperature = 0.7
	config.Settings.SessionTTL = 86400

	return &Registry{
		config:  config,
		created: time.Now(),
	}
}

// GetModel returns the configuration for a model, or error if not found.
func (r *Registry) GetModel(modelName string) (*ModelConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if modelName == "" {
		modelName = r.config.DefaultModel
	}

	model, ok := r.config.Models[modelName]
	if !ok {
		return nil, fmt.Errorf("model not found: %s", modelName)
	}

	return &model, nil
}

// GetDefaultModel returns the default model name.
func (r *Registry) GetDefaultModel() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.DefaultModel
}

// ListModels returns all available models.
func (r *Registry) ListModels() []ModelObject {
	r.mu.RLock()
	defer r.mu.RUnlock()

	models := make([]ModelObject, 0, len(r.config.Models))
	for name := range r.config.Models {
		models = append(models, ModelObject{
			ID:      name,
			Object:  "model",
			Created: r.created.Unix(),
			OwnedBy: "shannon",
		})
	}
	return models
}

// GetMaxTokensLimit returns the global max tokens limit.
func (r *Registry) GetMaxTokensLimit() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.Settings.MaxTokensLimit
}

// GetDefaultTemperature returns the default temperature.
func (r *Registry) GetDefaultTemperature() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.Settings.DefaultTemperature
}

// IsValidModel checks if a model name is valid.
func (r *Registry) IsValidModel(modelName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.config.Models[modelName]
	return ok
}


