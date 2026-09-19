// =============================================================================
// 文件: go/orchestrator/cmd/gateway/internal/middleware/ratelimit.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   速率限制中间件 —— 按 API key / IP 限制请求频率。
// 【关键内容】
//   令牌桶算法 / 滑动窗口，返回 429 当超限。
// 【协作关系】
//   使用 ratecontrol 包实现限流逻辑，通过 Redis 共享计数。
// =============================================================================
package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/auth"
	cfgutil "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/config"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// RateLimitConfig holds rate limit configuration loaded from rate_limits.yaml
type RateLimitConfig struct {
	Tiers map[string]TierLimits `yaml:"tiers"`
}

// TierLimits defines rate limits for a tier
type TierLimits struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	RequestsPerHour   int `yaml:"requests_per_hour"`
	RequestsPerDay    int `yaml:"requests_per_day"`
}

// RateLimitInfo contains rate limit status for response headers
type RateLimitInfo struct {
	LimitMinute     int
	RemainingMinute int
	LimitHour       int
	RemainingHour   int
	ResetAt         time.Time
	Window          string // Which window was exceeded: "minute", "hour", "day"
}

// RateLimiter provides rate limiting middleware
type RateLimiter struct {
	redis  *redis.Client
	logger *zap.Logger
	config *RateLimitConfig
}

// DefaultRateLimitConfig returns default configuration
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		Tiers: map[string]TierLimits{
			"free":       {RequestsPerMinute: 60, RequestsPerHour: 1000, RequestsPerDay: 10000},
			"pro":        {RequestsPerMinute: 200, RequestsPerHour: 5000, RequestsPerDay: 50000},
			"max":        {RequestsPerMinute: 300, RequestsPerHour: 10000, RequestsPerDay: 100000},
			"enterprise": {RequestsPerMinute: 500, RequestsPerHour: 20000, RequestsPerDay: 200000},
			"admin":      {RequestsPerMinute: 1000, RequestsPerHour: 100000, RequestsPerDay: 1000000},
		},
	}
}

// LoadRateLimitConfig loads configuration from file
func LoadRateLimitConfig(path string) (*RateLimitConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read rate limit config: %w", err)
	}

	config := DefaultRateLimitConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse rate limit config: %w", err)
	}

	return config, nil
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(redisClient *redis.Client, logger *zap.Logger) *RateLimiter {
	config := DefaultRateLimitConfig()

	// Try to load from config file
	configPath := cfgutil.ResolveConfigFile("RATE_LIMIT_CONFIG_PATH", cfgutil.RateLimitConfigPaths, "config/rate_limits.yaml")
	if loadedConfig, err := LoadRateLimitConfig(configPath); err == nil {
		config = loadedConfig
		logger.Info("Loaded rate limit config", zap.String("path", configPath))
	} else {
		logger.Warn("Using default rate limit config", zap.Error(err))
	}

	return &RateLimiter{
		redis:  redisClient,
		logger: logger,
		config: config,
	}
}

// NewRateLimiterWithConfig creates a rate limiter with explicit config
func NewRateLimiterWithConfig(redisClient *redis.Client, logger *zap.Logger, config *RateLimitConfig) *RateLimiter {
	if config == nil {
		config = DefaultRateLimitConfig()
	}
	return &RateLimiter{
		redis:  redisClient,
		logger: logger,
		config: config,
	}
}

// Middleware returns the HTTP middleware function
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Get user context from auth middleware
		userCtx, ok := ctx.Value(auth.UserContextKey).(*auth.UserContext)
		if !ok {
			// If no user context, reject if require_tenant_id is enabled
			if true { // Require authenticated user context for rate limiting
				rl.logger.Warn("Rate limit check: missing user context",
					zap.String("path", r.URL.Path))
				rl.sendUnauthorizedError(w, "Authentication required")
				return
			}
			// Otherwise skip rate limiting (auth middleware will handle it)
			next.ServeHTTP(w, r)
			return
		}

		// Reject if tenant_id is missing (prevents bypass)
		if userCtx.TenantID == uuid.Nil {
			rl.logger.Warn("Rate limit check: missing tenant_id",
				zap.String("user_id", userCtx.UserID.String()),
				zap.String("path", r.URL.Path))
			rl.sendUnauthorizedError(w, "Tenant context required")
			return
		}

		// Determine rate limit key and tier
		var rateLimitKey string
		var tier string

		if userCtx.IsAPIKey && userCtx.APIKeyID != uuid.Nil {
			// Per-key rate limiting for API keys
			rateLimitKey = fmt.Sprintf("ratelimit:key:%s", userCtx.APIKeyID.String())
			tier = userCtx.APIKeyTier
		} else {
			// Per-user rate limiting for JWT auth
			rateLimitKey = fmt.Sprintf("ratelimit:user:%s", userCtx.UserID.String())
			tier = userCtx.TenantPlan
		}

		// Default to "free" tier if not set
		if tier == "" {
			tier = "free"
		}

		// Get tier limits
		limits := rl.getLimitsForTier(tier)

		// Check all rate limit windows
		allowed, info := rl.checkAllWindows(ctx, rateLimitKey, limits)

		// Set rate limit headers
		rl.setRateLimitHeaders(w, info, limits)

		if !allowed {
			// Rate limit exceeded
			rl.logger.Warn("Rate limit exceeded",
				zap.String("user_id", userCtx.UserID.String()),
				zap.String("tenant_id", userCtx.TenantID.String()),
				zap.String("tier", tier),
				zap.String("window", info.Window),
				zap.String("path", r.URL.Path),
			)

			retryAfter := int64(info.ResetAt.Sub(time.Now()).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			rl.sendRateLimitError(w, info.Window)
			return
		}

		// Continue with request
		next.ServeHTTP(w, r)
	})
}

// getLimitsForTier returns rate limits for the given tier
func (rl *RateLimiter) getLimitsForTier(tier string) TierLimits {
	if limits, ok := rl.config.Tiers[tier]; ok {
		return limits
	}
	// Fall back to free tier
	if limits, ok := rl.config.Tiers["free"]; ok {
		return limits
	}
	// Ultimate fallback
	return TierLimits{
		RequestsPerMinute: 10,
		RequestsPerHour:   100,
		RequestsPerDay:    1000,
	}
}

// checkAllWindows checks rate limits across all time windows
func (rl *RateLimiter) checkAllWindows(ctx context.Context, baseKey string, limits TierLimits) (allowed bool, info RateLimitInfo) {
	now := time.Now()

	// Define windows to check
	windows := []struct {
		name     string
		duration time.Duration
		limit    int
	}{
		{"minute", time.Minute, limits.RequestsPerMinute},
		{"hour", time.Hour, limits.RequestsPerHour},
		{"day", 24 * time.Hour, limits.RequestsPerDay},
	}

	info.LimitMinute = limits.RequestsPerMinute
	info.LimitHour = limits.RequestsPerHour
	info.ResetAt = now.Truncate(time.Minute).Add(time.Minute)

	// Check each window
	for _, window := range windows {
		windowStart := now.Truncate(window.duration)
		windowKey := fmt.Sprintf("%s:%s:%d", baseKey, window.name, windowStart.Unix())

		count, err := rl.incrementAndGet(ctx, windowKey, window.duration)
		if err != nil {
			rl.logger.Error("Rate limit check failed",
				zap.String("window", window.name),
				zap.Error(err))

			// Fail open if configured
			if true { // Fail open on Redis errors
				continue
			}
			// Fail closed: reject the request
			info.Window = window.name
			info.ResetAt = windowStart.Add(window.duration)
			return false, info
		}

		remaining := window.limit - int(count)
		if remaining < 0 {
			remaining = 0
		}

		// Update info based on window
		switch window.name {
		case "minute":
			info.RemainingMinute = remaining
			info.ResetAt = windowStart.Add(time.Minute)
		case "hour":
			info.RemainingHour = remaining
		}

		// Check if limit exceeded
		if count > int64(window.limit) {
			info.Window = window.name
			info.ResetAt = windowStart.Add(window.duration)
			return false, info
		}
	}

	return true, info
}

// incrementAndGet atomically increments a counter and returns the new value
func (rl *RateLimiter) incrementAndGet(ctx context.Context, key string, expiry time.Duration) (int64, error) {
	pipe := rl.redis.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, expiry+time.Second) // Add buffer to prevent race conditions
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// setRateLimitHeaders sets standard rate limit response headers
func (rl *RateLimiter) setRateLimitHeaders(w http.ResponseWriter, info RateLimitInfo, limits TierLimits) {
	w.Header().Set("X-RateLimit-Limit-Minute", fmt.Sprintf("%d", limits.RequestsPerMinute))
	w.Header().Set("X-RateLimit-Remaining-Minute", fmt.Sprintf("%d", info.RemainingMinute))
	w.Header().Set("X-RateLimit-Limit-Hour", fmt.Sprintf("%d", limits.RequestsPerHour))
	w.Header().Set("X-RateLimit-Remaining-Hour", fmt.Sprintf("%d", info.RemainingHour))
	w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", info.ResetAt.Unix()))
}

// sendRateLimitError sends a rate limit exceeded error response
func (rl *RateLimiter) sendRateLimitError(w http.ResponseWriter, window string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)

	response := map[string]interface{}{
		"error":   "Rate limit exceeded",
		"message": fmt.Sprintf("Too many requests. %s limit exceeded. Please retry after the rate limit window resets.", window),
		"window":  window,
	}

	json.NewEncoder(w).Encode(response)
}

// sendUnauthorizedError sends an unauthorized error response
func (rl *RateLimiter) sendUnauthorizedError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)

	response := map[string]interface{}{
		"error":   "Unauthorized",
		"message": message,
	}

	json.NewEncoder(w).Encode(response)
}

// ServeHTTP implements http.Handler interface
func (rl *RateLimiter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rl.sendRateLimitError(w, "minute")
}

// GetConfig returns the rate limit configuration (for testing/debugging)
func (rl *RateLimiter) GetConfig() *RateLimitConfig {
	return rl.config
}
