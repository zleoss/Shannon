// =============================================================================
// 文件: go/orchestrator/cmd/gateway/main.go
// 翻译源: package main
// -----------------------------------------------------------------------------
// 【一句话功能】
//   Shannon 的 HTTP 网关进程入口（监听 :8080）。所有外部用户请求从这里进入
//   系统；它将 REST/SSE/OpenAI 兼容端点转译为对 orchestrator gRPC 的调用。
//
// 【在 AI Agent 体系中的定位】
//   充当"大脑入口神经"：(1) 接 API、(2) 鉴权/限流/校验、(3) gRPC 调 orchestrator
//   触发 Temporal workflow、(4) 通过 SSE/WS 把流式事件回吐客户端。
//
// 【本文件主要内容（按行号区间）】
//   1-34  : 导入 —— handlers/middleware/openai/proxy + 内部 auth/config/daemon/
//           db/session/skills/streaming/pb。注意同时引入 v8 与 v9 两个 redis 客户端
//           （v8 给 daemon 老 bridge 用，v9 给主流 redis 用）。
//   36-205: main() 启动序列 —— logger、config 加载、Redis/Postgres 连接、
//           session manager、skills registry、streaming、daemon hub、
//           orchestrator gRPC dial、gRPC auth token。
//   207-890: http.NewServeMux 注册全部路由（Go 1.22 方法前缀语法）：
//            - /api/v1/tasks*         task handler
//            - /api/v1/sessions*      session handler
//            - /api/v1/tools*         tools handler（dangerous 工具默认拦截）
//            - /api/v1/agents*        agent handler
//            - /api/v1/skills*        skills handler
//            - /api/v1/approvals*     HITL 审批
//            - /api/v1/schedules*     定时任务
//            - /v1/chat/completions   OpenAI 兼容 ChatCompletions（含编排）
//            - /v1/completions        OpenAI 兼容 Completions（薄代理，无编排）
//            - /v1/models             模型列表
//            - SSE/WS 代理            admin streaming/approval/websocket
//            - daemon WebSocket       shan CLI daemon 连接
//   891-1039: 启动 HTTP server、优雅退出（SIGTERM/SIGINT）。
//
// 【关键 handler/Middleware 委托】
//   - health、auth、rate-limit、idempotency、validation、tracing 等 middleware
//     在 cmd/gateway/internal/middleware/ 下。
//   - 业务 handler 在 cmd/gateway/internal/handlers/*。
//   - OpenAI 兼容入口在 cmd/gateway/internal/openai/handler.go。
//   - 流式代理在 cmd/gateway/internal/proxy/streaming.go。
//
// 【四类对外 API 端点（注意：4 个入口）】
//   POST /v1/chat/completions  —— OpenAI 兼容，经过 orchestrator 编排
//   POST /v1/completions       —— OpenAI 兼容，薄代理，无编排
//   POST /api/v1/tasks         —— Shannon 原生同步
//   POST /api/v1/tasks/stream  —— Shannon 原生 SSE
//
// 【鉴权开关】由 GATEWAY_SKIP_AUTH=1 控制（dev/test）；prod 必须设为 0。
//
// 【学习建议】先按上面四个端点跟踪一次请求流；具体业务逻辑在各 handler 文件。
//   想看路由表 → 直接定位到第 207 行 `http.NewServeMux()` 之后。
// =============================================================================

package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kocoro-lab/Shannon/go/orchestrator/cmd/gateway/internal/handlers"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/cmd/gateway/internal/middleware"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/cmd/gateway/internal/openai"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/cmd/gateway/internal/proxy"
	authpkg "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/auth"
	cfg "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/config"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/daemon"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/db"
	orchpb "github.com/Kocoro-lab/Shannon/go/orchestrator/internal/pb/orchestrator"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/session"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/skills"
	"github.com/Kocoro-lab/Shannon/go/orchestrator/internal/streaming"
	redisv8 "github.com/go-redis/redis/v8"
	"github.com/jmoiron/sqlx"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()

	// Discover configuration defaults from features.yaml (best effort)
	var gatewaySkipAuthDefault *bool
	if featuresCfg, err := cfg.Load(); err != nil {
		logger.Warn("Failed to load feature configuration", zap.Error(err))
	} else if featuresCfg != nil && featuresCfg.Gateway.SkipAuth != nil {
		gatewaySkipAuthDefault = featuresCfg.Gateway.SkipAuth
	}
	if envVal := os.Getenv("GATEWAY_SKIP_AUTH"); envVal != "" {
		logger.Warn("Environment variable overrides gateway authentication setting",
			zap.String("env", "GATEWAY_SKIP_AUTH"),
			zap.String("config_key", "gateway.skip_auth"),
			zap.String("value", envVal))
	} else if gatewaySkipAuthDefault != nil {
		if *gatewaySkipAuthDefault {
			_ = os.Setenv("GATEWAY_SKIP_AUTH", "1")
		} else {
			_ = os.Setenv("GATEWAY_SKIP_AUTH", "0")
		}
	}

	// Initialize database
	dbConfig := &db.Config{
		Host:     getEnvOrDefault("POSTGRES_HOST", "postgres"),
		Port:     getEnvOrDefaultInt("POSTGRES_PORT", 5432),
		User:     getEnvOrDefault("POSTGRES_USER", "shannon"),
		Password: getEnvOrDefault("POSTGRES_PASSWORD", "shannon"),
		Database: getEnvOrDefault("POSTGRES_DB", "shannon"),
		SSLMode:  getEnvOrDefault("POSTGRES_SSLMODE", "disable"),
	}

	dbClient, err := db.NewClient(dbConfig, logger)
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	}
	defer dbClient.Close()

	// Create sqlx.DB wrapper for auth service
	pgDB := sqlx.NewDb(dbClient.GetDB(), "postgres")

	// Initialize Redis client for rate limiting and idempotency
	redisURL := getEnvOrDefault("REDIS_URL", "redis://redis:6379")
	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Fatal("Failed to parse Redis URL", zap.Error(err))
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()

	// Test Redis connection
	ctx := context.Background()
	if _, err := redisClient.Ping(ctx).Result(); err != nil {
		logger.Fatal("Failed to connect to Redis", zap.Error(err))
	}

	// Initialize streaming manager with Redis so gateway can subscribe to
	// workflow events for streaming card replies.
	// Streaming manager uses go-redis/v8, gateway uses redis/v9 — create a v8 client.
	streamingRedis := redisv8.NewClient(&redisv8.Options{
		Addr:     redisOpts.Addr,
		Password: redisOpts.Password,
		DB:       redisOpts.DB,
	})
	defer streamingRedis.Close()
	streaming.InitializeRedis(streamingRedis, logger)

	// Initialize auth service (direct access to internal package)
	jwtSecret := getEnvOrDefault("JWT_SECRET", "your-secret-key")
	authService := authpkg.NewService(pgDB, logger, jwtSecret)

	// Connect to orchestrator gRPC
	orchAddr := getEnvOrDefault("ORCHESTRATOR_GRPC", "orchestrator:50052")
	conn, err := grpc.Dial(orchAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(50*1024*1024)), // 50MB
	)
	if err != nil {
		logger.Fatal("Failed to connect to orchestrator", zap.Error(err))
	}
	defer conn.Close()

	orchClient := orchpb.NewOrchestratorServiceClient(conn)

	// Initialize skill registry
	skillRegistry := skills.NewRegistry()
	for _, dir := range skills.ResolveSkillDirs() {
		if err := skillRegistry.LoadDirectory(dir); err != nil {
			logger.Warn("Failed to load skills from directory", zap.String("path", dir), zap.Error(err))
		}
	}
	if err := skillRegistry.Finalize(); err != nil {
		logger.Fatal("Failed to finalize skill registry", zap.Error(err))
	}
	logger.Info("Skill registry initialized",
		zap.Int("total_skills", skillRegistry.Count()),
		zap.Strings("categories", skillRegistry.Categories()),
	)

	// Create session manager for persisting HITL messages to session history
	sessionMgr, err := session.NewManager(redisOpts.Addr, logger)
	if err != nil {
		logger.Warn("Failed to create session manager for HITL persistence", zap.Error(err))
	}

	// Create handlers
	taskHandler := handlers.NewTaskHandler(orchClient, pgDB, redisClient, skillRegistry, logger, sessionMgr)
	sessionHandler := handlers.NewSessionHandler(pgDB, redisClient, logger)
	approvalHandler := handlers.NewApprovalHandler(orchClient, logger)
	reviewHandler := handlers.NewReviewHandler(orchClient, redisClient, logger)
	scheduleHandler := handlers.NewScheduleHandler(orchClient, pgDB, logger)
	healthHandler := handlers.NewHealthHandler(orchClient, logger)
	openapiHandler := handlers.NewOpenAPIHandler()
	agentsHandler := handlers.NewAgentsHandler(orchClient, logger)
	skillHandler := handlers.NewSkillHandler(skillRegistry, logger)
	workspaceHandler := handlers.NewWorkspaceHandler(pgDB, logger)
	memoryHandler := handlers.NewMemoryHandler(logger)
	llmModelsPath := cfg.ResolveConfigFile("MODELS_CONFIG_PATH", []string{
		"/app/config/models.yaml",
		"config/models.yaml",
		"../../config/models.yaml",
	}, "config/models.yaml")
	llmModelsHandler := handlers.NewLLMModelsHandler(llmModelsPath, logger)

	adminURL := getEnvOrDefault("ADMIN_SERVER", "http://orchestrator:8081")

	// OAuth verifier (enterprise-only)
	// Web OAuth credentials
	googleClientID := getEnvOrDefault("GOOGLE_OAUTH_CLIENT_ID", "")
	googleClientSecret := getEnvOrDefault("GOOGLE_OAUTH_CLIENT_SECRET", "")
	// Desktop OAuth credentials (for Tauri/native apps)
	googleDesktopClientID := getEnvOrDefault("GOOGLE_DESKTOP_CLIENT_ID", "")
	googleDesktopClientSecret := getEnvOrDefault("GOOGLE_DESKTOP_CLIENT_SECRET", "")
	oauthVerifier := authpkg.NewGoogleOAuthVerifier(googleClientID, googleClientSecret, googleDesktopClientID, googleDesktopClientSecret)

	// Enterprise: Load rate limit config from YAML (or use defaults)
	rateLimitConfigPath := cfg.ResolveConfigFile("RATE_LIMIT_CONFIG_PATH", cfg.RateLimitConfigPaths, "config/rate_limits.yaml")
	rateLimitConfig, err := middleware.LoadRateLimitConfig(rateLimitConfigPath)
	if err != nil {
		logger.Warn("Failed to load rate limit config, using defaults", zap.String("path", rateLimitConfigPath), zap.Error(err))
		rateLimitConfig = middleware.DefaultRateLimitConfig()
	} else {
		logger.Info("Loaded rate limit config", zap.String("path", rateLimitConfigPath))
	}

	// Create auth handler (needs rateLimitConfig for config-based RPM/RPH in /auth/me)
	authHandler := handlers.NewAuthHandler(authService, oauthVerifier, pgDB, logger, rateLimitConfig)

	// Create middlewares
	authMiddleware := middleware.NewAuthMiddleware(authService, logger).Middleware
	rateLimiter := middleware.NewRateLimiterWithConfig(redisClient, logger, rateLimitConfig).Middleware
	idempotencyMiddleware := middleware.NewIdempotencyMiddleware(redisClient, logger).Middleware
	tracingMiddleware := middleware.NewTracingMiddleware(logger).Middleware
	validationMiddleware := middleware.NewValidationMiddleware(logger).Middleware

	// OpenAI-compatible API handler
	openaiHandler, err := openai.NewHandler(orchClient, pgDB, redisClient, logger, adminURL)
	if err != nil {
		logger.Warn("Failed to create OpenAI handler, endpoint disabled", zap.Error(err))
	}

	// Tool execution handler
	toolsHandler := handlers.NewToolsHandler(pgDB, logger)

	// Setup HTTP mux
	mux := http.NewServeMux()

	// Health check (no auth required)
	mux.HandleFunc("GET /health", healthHandler.Health)
	mux.HandleFunc("GET /readiness", healthHandler.Readiness)

	// Prometheus metrics endpoint (no auth required)
	mux.Handle("GET /metrics", promhttp.Handler())

	// OpenAPI spec (no auth required)
	mux.HandleFunc("GET /openapi.json", openapiHandler.ServeSpec)

	// Auth endpoints (no auth required for registration, auth required for me/refresh)
	// Auth endpoints
	mux.HandleFunc("POST /api/v1/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/v1/auth/refresh", authHandler.Refresh)
	mux.Handle("GET /api/v1/auth/me",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(authHandler.Me),
			),
		),
	)
	mux.Handle("POST /api/v1/auth/refresh-key",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(authHandler.RefreshKey),
			),
		),
	)

	// API Key management endpoints (require auth)
	mux.Handle("GET /api/v1/auth/api-keys",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(authHandler.ListAPIKeys),
			),
		),
	)
	mux.Handle("POST /api/v1/auth/api-keys",
		tracingMiddleware(
			authMiddleware(
				rateLimiter(
					http.HandlerFunc(authHandler.CreateKey),
				),
			),
		),
	)
	mux.Handle("DELETE /api/v1/auth/api-keys/{id}",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(authHandler.RevokeKey),
			),
		),
	)

	// Task endpoints (require auth)
	mux.Handle("POST /api/v1/tasks",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(taskHandler.SubmitTask),
						),
					),
				),
			),
		),
	)

	// Unified submit that returns the SSE stream URL (no long-lived stream here)
	mux.Handle("POST /api/v1/tasks/stream",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(taskHandler.SubmitTaskAndGetStreamURL),
						),
					),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/tasks",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(taskHandler.ListTasks),
					),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/tasks/{id}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(taskHandler.GetTaskStatus),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/tasks/{id}/stream",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(taskHandler.StreamTask),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/tasks/{id}/events",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(taskHandler.GetTaskEvents),
					),
				),
			),
		),
	)

	mux.Handle("POST /api/v1/tasks/{id}/cancel",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(taskHandler.CancelTask),
						),
					),
				),
			),
		),
	)

	// Pause workflow
	mux.Handle("POST /api/v1/tasks/{id}/pause",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(taskHandler.PauseTask),
						),
					),
				),
			),
		),
	)

	// Resume workflow
	mux.Handle("POST /api/v1/tasks/{id}/resume",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(taskHandler.ResumeTask),
						),
					),
				),
			),
		),
	)

	// Swarm HITL: send human message to Lead
	mux.Handle("POST /api/v1/swarm/{workflowID}/message",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(taskHandler.SendSwarmMessage),
					),
				),
			),
		),
	)

	// Get control state
	mux.Handle("GET /api/v1/tasks/{id}/control-state",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(taskHandler.GetControlState),
					),
				),
			),
		),
	)

	// Approval endpoints (require auth)
	mux.Handle("POST /api/v1/approvals/decision",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(approvalHandler.SubmitDecision),
						),
					),
				),
			),
		),
	)

	// HITL Research Review
	mux.Handle("GET /api/v1/tasks/{workflowID}/review",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(reviewHandler.HandleGetReview),
				),
			),
		),
	)
	mux.Handle("POST /api/v1/tasks/{workflowID}/review",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(reviewHandler.HandleReview),
					),
				),
			),
		),
	)

	// Session endpoints (require auth)
	mux.Handle("GET /api/v1/sessions",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(sessionHandler.ListSessions),
					),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/sessions/{sessionId}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(sessionHandler.GetSession),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/sessions/{sessionId}/history",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(sessionHandler.GetSessionHistory),
				),
			),
		),
	)

	// Session events (chat history-like, excludes LLM_PARTIAL)
	mux.Handle("GET /api/v1/sessions/{sessionId}/events",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(sessionHandler.GetSessionEvents),
				),
			),
		),
	)

	// Update session title
	mux.Handle("PATCH /api/v1/sessions/{sessionId}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(sessionHandler.UpdateSessionTitle),
					),
				),
			),
		),
	)

	// Soft delete session (idempotent)
	mux.Handle("DELETE /api/v1/sessions/{sessionId}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(sessionHandler.DeleteSession),
					),
				),
			),
		),
	)

	// Session workspace file operations (require auth)
	// List files in session workspace
	mux.Handle("GET /api/v1/sessions/{sessionId}/files",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(workspaceHandler.ListFiles),
					),
				),
			),
		),
	)

	// Download file from session workspace (path is the file path within workspace)
	mux.Handle("GET /api/v1/sessions/{sessionId}/files/{path...}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(workspaceHandler.DownloadFile),
					),
				),
			),
		),
	)

	// User memory file operations (require auth, user_id from token)
	// List files in authenticated user's memory directory
	mux.Handle("GET /api/v1/memory/files",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(memoryHandler.ListMemoryFiles),
					),
				),
			),
		),
	)

	// Download file from authenticated user's memory directory
	mux.Handle("GET /api/v1/memory/files/{path...}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(memoryHandler.DownloadMemoryFile),
					),
				),
			),
		),
	)

	// Schedule endpoints (require auth)
	mux.Handle("POST /api/v1/schedules",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(scheduleHandler.CreateSchedule),
						),
					),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/schedules",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(scheduleHandler.ListSchedules),
					),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/schedules/{id}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(scheduleHandler.GetSchedule),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/schedules/{id}/runs",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					http.HandlerFunc(scheduleHandler.GetScheduleRuns),
				),
			),
		),
	)

	mux.Handle("PUT /api/v1/schedules/{id}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(scheduleHandler.UpdateSchedule),
						),
					),
				),
			),
		),
	)

	mux.Handle("POST /api/v1/schedules/{id}/pause",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(scheduleHandler.PauseSchedule),
						),
					),
				),
			),
		),
	)

	mux.Handle("POST /api/v1/schedules/{id}/resume",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(scheduleHandler.ResumeSchedule),
						),
					),
				),
			),
		),
	)

	mux.Handle("DELETE /api/v1/schedules/{id}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(scheduleHandler.DeleteSchedule),
					),
				),
			),
		),
	)

	// Agents endpoints (single-purpose deterministic agents)
	mux.Handle("GET /api/v1/agents",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(agentsHandler.ListAgents),
			),
		),
	)

	mux.Handle("GET /api/v1/agents/{id}",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(agentsHandler.GetAgent),
			),
		),
	)

	mux.Handle("POST /api/v1/agents/{id}",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						idempotencyMiddleware(
							http.HandlerFunc(agentsHandler.ExecuteAgent),
						),
					),
				),
			),
		),
	)

	logger.Info("Registered agents API endpoints",
		zap.String("endpoints", "/api/v1/agents, /api/v1/agents/{id}"),
	)

	// Skills endpoints (require auth)
	mux.Handle("GET /api/v1/skills",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(skillHandler.ListSkills),
			),
		),
	)

	mux.Handle("GET /api/v1/skills/{name}",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(skillHandler.GetSkill),
			),
		),
	)

	mux.Handle("GET /api/v1/skills/{name}/versions",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(skillHandler.GetSkillVersions),
			),
		),
	)

	logger.Info("Registered skills API endpoints",
		zap.String("endpoints", "/api/v1/skills, /api/v1/skills/{name}, /api/v1/skills/{name}/versions"),
	)

	// Tool execution endpoints (require auth)
	// NOTE: GET /api/v1/tools must be registered BEFORE GET /api/v1/tools/{name}
	// to avoid routing conflicts with Go 1.22 ServeMux.
	mux.Handle("GET /api/v1/tools",
		tracingMiddleware(
			authMiddleware(
				rateLimiter(
					http.HandlerFunc(toolsHandler.ListTools),
				),
			),
		),
	)

	mux.Handle("GET /api/v1/tools/{name}",
		tracingMiddleware(
			authMiddleware(
				rateLimiter(
					http.HandlerFunc(toolsHandler.GetTool),
				),
			),
		),
	)

	mux.Handle("POST /api/v1/tools/{name}/execute",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					rateLimiter(
						http.HandlerFunc(toolsHandler.ExecuteTool),
					),
				),
			),
		),
	)

	logger.Info("Registered tool execution API endpoints",
		zap.String("endpoints", "/api/v1/tools, /api/v1/tools/{name}, /api/v1/tools/{name}/execute"),
	)

	// OpenAI-compatible API endpoints (/v1/*)
	if openaiHandler != nil {
		// POST /v1/chat/completions - main chat completion endpoint
		mux.Handle("POST /v1/chat/completions",
			tracingMiddleware(
				authMiddleware(
					validationMiddleware(
						rateLimiter(
							http.HandlerFunc(openaiHandler.ChatCompletions),
						),
					),
				),
			),
		)

		// POST /v1/completions - passthrough proxy to LLM service (for CLI tool execution)
		mux.Handle("POST /v1/completions",
			tracingMiddleware(
				authMiddleware(
					validationMiddleware(
						rateLimiter(
							idempotencyMiddleware(
								http.HandlerFunc(openaiHandler.Completions),
							),
						),
					),
				),
			),
		)

		// GET /v1/models - list available models
		mux.Handle("GET /v1/models",
			tracingMiddleware(
				authMiddleware(
					http.HandlerFunc(openaiHandler.ListModels),
				),
			),
		)

		// GET /v1/models/{model} - get specific model info
		mux.Handle("GET /v1/models/{model}",
			tracingMiddleware(
				authMiddleware(
					http.HandlerFunc(openaiHandler.GetModel),
				),
			),
		)

		// GET /v1/llm-models - list available LLM provider models (from models.yaml)
		mux.Handle("GET /v1/llm-models",
			tracingMiddleware(
				authMiddleware(
					http.HandlerFunc(llmModelsHandler.ListLLMModels),
				),
			),
		)

		logger.Info("OpenAI-compatible API enabled",
			zap.String("endpoints", "/v1/chat/completions, /v1/completions, /v1/models, /v1/llm-models"),
		)
	}

	// SSE/WebSocket reverse proxy to admin server
	streamProxy, err := proxy.NewStreamingProxy(adminURL, logger)
	if err != nil {
		logger.Fatal("Failed to create streaming proxy", zap.Error(err))
	}

	// Proxy SSE and WebSocket endpoints
	mux.Handle("/api/v1/stream/sse",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					streamProxy,
				),
			),
		),
	)

	mux.Handle("/api/v1/stream/ws",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					streamProxy,
				),
			),
		),
	)

	// Daemon command channel
	daemonHub := daemon.NewHub(redisClient, logger)
	// Apply claim TTL from features config if loaded
	if featuresCfg, err := cfg.Load(); err == nil && featuresCfg.Daemon.HeartbeatTimeoutSeconds > 0 {
		daemonHub.Claims().SetClaimTTL(time.Duration(featuresCfg.Daemon.HeartbeatTimeoutSeconds) * time.Second)
	}
	// Subscribe to dispatch requests from orchestrator (Redis stream bridge)
	go daemonHub.SubscribeDispatches(ctx)
	approvalManager := daemon.NewApprovalManager(redisClient)
	// Token for internal admin server calls (/daemon/signal, /events).
	// Falls back to APPROVALS_AUTH_TOKEN to match orchestrator's daemon signal endpoint.
	eventsAuthToken := getEnvOrDefault("EVENTS_AUTH_TOKEN", getEnvOrDefault("APPROVALS_AUTH_TOKEN", ""))
	daemonHandler := handlers.NewDaemonHandler(daemonHub, approvalManager, adminURL, eventsAuthToken, logger)

	// WebSocket endpoint for shan CLI daemons (no method prefix for WS upgrade)
	mux.Handle("/v1/ws/messages",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(daemonHandler.HandleWebSocket),
			),
		),
	)

	// Daemon status API
	mux.Handle("GET /api/v1/daemon/status",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(daemonHandler.HandleStatus),
			),
		),
	)

	// Blob proxy: GET /api/v1/blob/{key} -> admin /blob/{key}
	// Used to fetch large base64 screenshots stored in Redis
	mux.Handle("GET /api/v1/blob/",
		tracingMiddleware(
			authMiddleware(
				validationMiddleware(
					streamProxy,
				),
			),
		),
	)

	// Timeline proxy: GET /api/v1/tasks/{id}/timeline -> admin /timeline
	mux.Handle("GET /api/v1/tasks/{id}/timeline",
		tracingMiddleware(
			authMiddleware(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id := r.PathValue("id")
					if id == "" {
						http.Error(w, "{\"error\":\"Task ID required\"}", http.StatusBadRequest)
						return
					}
					// Rebuild target URL
					target := strings.TrimRight(adminURL, "/") + "/timeline?workflow_id=" + id
					if raw := r.URL.RawQuery; raw != "" {
						target += "&" + raw
					}
					req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						logger.Error("timeline proxy error", zap.Error(err))
						http.Error(w, "{\"error\":\"upstream unavailable\"}", http.StatusBadGateway)
						return
					}
					defer resp.Body.Close()
					for k, v := range resp.Header {
						for _, vv := range v {
							w.Header().Add(k, vv)
						}
					}
					w.WriteHeader(resp.StatusCode)
					_, _ = io.Copy(w, resp.Body)
				}),
			),
		),
	)

	// CORS middleware for all routes (development friendly)
	corsHandler := corsMiddleware(mux)

	// Create HTTP server
	port := getEnvOrDefaultInt("PORT", 8080)
	server := &http.Server{
		Addr:         ":" + strconv.Itoa(port),
		Handler:      corsHandler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,                 // No write timeout for SSE/streaming support
		IdleTimeout:  300 * time.Second, // 5 minutes idle for long-lived SSE connections
	}

	// Start server in goroutine
	go func() {
		logger.Info("Gateway starting", zap.Int("port", port))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start gateway", zap.Error(err))
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Gateway shutting down...")

	// Stop background workers (prevents goroutine leaks)
	authService.Close()
	if openaiHandler != nil {
		openaiHandler.Close()
	}

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Gateway forced to shutdown", zap.Error(err))
	}

	logger.Info("Gateway stopped")
}

// corsMiddleware adds CORS headers for development
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isStreaming := strings.HasPrefix(r.URL.Path, "/api/v1/stream/")

		allowedHeaders := "Content-Type, Authorization, X-API-Key, X-User-Id, Idempotency-Key, traceparent, tracestate, Cache-Control, Last-Event-ID, If-Match"

		// Expose custom headers for frontend access (rate limits, tracing, session)
		exposedHeaders := "X-RateLimit-Limit-Minute, X-RateLimit-Remaining-Minute, X-RateLimit-Limit-Hour, X-RateLimit-Remaining-Hour, X-RateLimit-Reset, X-Trace-Id, X-Span-Id, X-Session-ID, X-Shannon-Session-ID"

		// Get allowed origins from environment (comma-separated) or default to wildcard
		allowedOrigins := getEnvOrDefault("CORS_ALLOWED_ORIGINS", "*")

		if !isStreaming {
			// Allow CORS - use specific origins in production, wildcard for dev
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigins)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
			w.Header().Set("Access-Control-Max-Age", "3600")
		} else {
			// Streaming endpoints also need CORS headers for GET requests
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigins)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
			w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
			w.Header().Set("Access-Control-Max-Age", "3600")
		}

		if r.Method == http.MethodOptions {
			// Handle preflight - headers already set above
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Helper functions
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvOrDefaultInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
