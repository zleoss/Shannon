// =============================================================================
// 文件: go/orchestrator/internal/auth/service.go
// -----------------------------------------------------------------------------
// 【一句话功能】 认证核心服务，提供注册/登录/OAuth/API Key/ProvisionalUser 全流程
// 【关键内容】 Register / Login / Refresh / ValidateAPIKey / RegisterFromOAuth / ProvisionUser；异步更新；配额管理
// 【协作关系】 依赖 db、jwt、oauth 等子模块，为全系统提供身份认证
// =============================================================================
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// QuotaDefaults defines token quota defaults for a plan
type QuotaDefaults struct {
	MonthlyTokens        int     `yaml:"monthly_tokens"`
	DailyTokens          int     `yaml:"daily_tokens"`
	HardCapMonthlyTokens int     `yaml:"hard_cap_monthly_tokens"` // 0 = unlimited; absolute ceiling even with overage
	OverageEnabled       bool    `yaml:"overage_enabled"`
	OveragePer1MTokens   float64 `yaml:"overage_per_1m_tokens"`
	OverageGraceTokens   int     `yaml:"overage_grace_tokens"` // One-time free overage tokens for first overage event
	MaxSchedules         int     `yaml:"max_schedules"`        // 0 = unlimited
}

// asyncUpdate represents a background update task
type asyncUpdate struct {
	query string
	args  []interface{}
}

// Service handles authentication operations
type Service struct {
	db            *sqlx.DB
	logger        *zap.Logger
	jwtManager    *JWTManager
	quotaDefaults map[string]QuotaDefaults
	updateCh      chan asyncUpdate // Bounded channel for async DB updates
	stopCh        chan struct{}    // Signal to stop background workers
}

// NewService creates a new authentication service
func NewService(db *sqlx.DB, logger *zap.Logger, jwtSecret string) *Service {
	quotaDefaults := defaultQuotaDefaults()

	s := &Service{
		db:            db,
		logger:        logger,
		quotaDefaults: quotaDefaults,
		jwtManager: NewJWTManager(
			jwtSecret,
			1*time.Hour,     // Access token expiry
			30*24*time.Hour, // Refresh token expiry
		),
		updateCh: make(chan asyncUpdate, 100), // Bounded buffer of 100 pending updates
		stopCh:   make(chan struct{}),
	}

	// Start background worker pool for async updates (2 workers)
	for i := 0; i < 2; i++ {
		go s.asyncUpdateWorker(i)
	}

	return s
}

// asyncUpdateWorker processes async DB updates from the channel
func (s *Service) asyncUpdateWorker(workerID int) {
	for {
		select {
		case update := <-s.updateCh:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := s.db.ExecContext(ctx, update.query, update.args...)
			cancel()
			if err != nil {
				s.logger.Warn("Async update failed",
					zap.Int("worker", workerID),
					zap.String("query", update.query),
					zap.Error(err))
			}
		case <-s.stopCh:
			return
		}
	}
}

// Close gracefully shuts down the service
func (s *Service) Close() {
	close(s.stopCh)
}

// enqueueUpdate adds an update to the async queue (non-blocking, drops if full)
func (s *Service) enqueueUpdate(query string, args ...interface{}) {
	select {
	case s.updateCh <- asyncUpdate{query: query, args: args}:
		// Successfully queued
	default:
		// Channel full, log and drop (better than unbounded goroutines)
		s.logger.Warn("Async update queue full, dropping update",
			zap.String("query", query))
	}
}

// defaultQuotaDefaults returns the built-in tenant quota defaults for the OSS repo.
// Rate limits still live in rate_limits.yaml, but quota defaults are intentionally
// code-owned so startup does not depend on optional YAML sections.
func defaultQuotaDefaults() map[string]QuotaDefaults {
	return map[string]QuotaDefaults{
		"free": {
			MonthlyTokens:        1_000_000,
			DailyTokens:          500_000,
			HardCapMonthlyTokens: 1_000_000,
			OverageEnabled:       false,
			OveragePer1MTokens:   0,
		},
		"pro": {
			MonthlyTokens:        10_000_000,
			DailyTokens:          3_000_000,
			HardCapMonthlyTokens: 100_000_000,
			OverageEnabled:       true,
			OveragePer1MTokens:   10.00,
			OverageGraceTokens:   1_000_000,
		},
		"max": {
			MonthlyTokens:        50_000_000,
			DailyTokens:          10_000_000,
			HardCapMonthlyTokens: 500_000_000,
			OverageEnabled:       true,
			OveragePer1MTokens:   8.00,
			OverageGraceTokens:   1_000_000,
		},
		"enterprise": {
			MonthlyTokens:        200_000_000,
			DailyTokens:          50_000_000,
			HardCapMonthlyTokens: 0,
			OverageEnabled:       true,
			OveragePer1MTokens:   6.00,
		},
	}
}

// Register creates a new user account
func (s *Service) Register(ctx context.Context, req *RegisterRequest) (*User, error) {
	// Check if email already exists
	var exists bool
	err := s.db.GetContext(ctx, &exists,
		"SELECT EXISTS(SELECT 1 FROM auth.users WHERE email = $1)", req.Email)
	if err != nil {
		return nil, fmt.Errorf("failed to check email existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("email already registered")
	}

	// Check if username already exists
	err = s.db.GetContext(ctx, &exists,
		"SELECT EXISTS(SELECT 1 FROM auth.users WHERE username = $1)", req.Username)
	if err != nil {
		return nil, fmt.Errorf("failed to check username existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("username already taken")
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Determine tenant ID
	var tenantID uuid.UUID
	if req.TenantID != "" {
		tenantID, err = uuid.Parse(req.TenantID)
		if err != nil {
			return nil, fmt.Errorf("invalid tenant ID: %w", err)
		}
	} else {
		// Create new tenant for the user
		tenantID, err = s.createTenant(ctx, req.Username, "shannon")
		if err != nil {
			return nil, fmt.Errorf("failed to create tenant: %w", err)
		}
	}

	// Create user
	user := &User{
		ID:           uuid.New(),
		Email:        req.Email,
		Username:     req.Username,
		PasswordHash: string(hashedPassword),
		FullName:     &req.FullName,
		TenantID:     tenantID,
		Role:         RoleUser,
		IsActive:     true,
		IsVerified:   false,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	query := `
		INSERT INTO auth.users (id, email, username, password_hash, full_name, tenant_id, role, is_active, is_verified)
		VALUES (:id, :email, :username, :password_hash, :full_name, :tenant_id, :role, :is_active, :is_verified)
	`

	_, err = s.db.NamedExecContext(ctx, query, user)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	// Log audit event
	s.logAuditEvent(ctx, AuditEventAccountCreated, user.ID, tenantID, nil)

	s.logger.Info("User registered successfully",
		zap.String("user_id", user.ID.String()),
		zap.String("email", user.Email),
		zap.String("tenant_id", tenantID.String()))

	return user, nil
}

// Login authenticates a user and returns tokens
func (s *Service) Login(ctx context.Context, req *LoginRequest) (*TokenPair, error) {
	// Find user by email
	var user User
	query := `SELECT * FROM auth.users WHERE email = $1 AND is_active = true`
	err := s.db.GetContext(ctx, &user, query, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			// Log failed attempt
			s.logAuditEvent(ctx, AuditEventLoginFailed, uuid.Nil, uuid.Nil,
				map[string]interface{}{"email": req.Email})
			return nil, fmt.Errorf("invalid email or password")
		}
		return nil, fmt.Errorf("failed to find user: %w", err)
	}

	// Verify password
	err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
	if err != nil {
		// Log failed attempt
		s.logAuditEvent(ctx, AuditEventLoginFailed, user.ID, user.TenantID, nil)
		return nil, fmt.Errorf("invalid email or password")
	}

	// Generate token pair
	tokens, refreshTokenHash, err := s.jwtManager.GenerateTokenPair(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// Store refresh token
	err = s.storeRefreshToken(ctx, &user, refreshTokenHash)
	if err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	// Update last login
	_, err = s.db.ExecContext(ctx,
		"UPDATE auth.users SET last_login = NOW() WHERE id = $1", user.ID)
	if err != nil {
		s.logger.Warn("Failed to update last login", zap.Error(err))
	}

	// Log audit event
	s.logAuditEvent(ctx, AuditEventLogin, user.ID, user.TenantID, nil)

	s.logger.Info("User logged in successfully",
		zap.String("user_id", user.ID.String()),
		zap.String("email", user.Email))

	return tokens, nil
}

// Refresh issues a new access token from a valid refresh token
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token required")
	}

	oldHash := hashToken(refreshToken)

	var user User
	err := s.db.GetContext(ctx, &user, `
		SELECT u.*
		FROM auth.refresh_tokens rt
		JOIN auth.users u ON u.id = rt.user_id
		WHERE rt.token_hash = $1
		  AND rt.revoked = false
		  AND rt.expires_at > NOW()
		LIMIT 1
	`, oldHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("invalid refresh token")
		}
		return nil, fmt.Errorf("failed to validate refresh token: %w", err)
	}

	if !user.IsActive {
		return nil, fmt.Errorf("user account is inactive")
	}

	// Generate new token pair first (no side effects)
	newPair, newHash, err := s.jwtManager.GenerateTokenPair(&user)
	if err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}

	// Use transaction to ensure atomicity: store new token BEFORE revoking old one
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Store new refresh token first
	if err := s.storeRefreshTokenTx(ctx, tx, &user, newHash); err != nil {
		return nil, fmt.Errorf("failed to store new refresh token: %w", err)
	}

	// Only revoke old token after new one is stored
	if _, err := tx.ExecContext(ctx, `
		UPDATE auth.refresh_tokens
		SET revoked = true, revoked_at = NOW()
		WHERE token_hash = $1
	`, oldHash); err != nil {
		s.logger.Warn("Failed to revoke old refresh token", zap.Error(err), zap.String("user_id", user.ID.String()))
		return nil, fmt.Errorf("failed to revoke old token: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Update last login best-effort (outside transaction)
	if _, err := s.db.ExecContext(ctx, "UPDATE auth.users SET last_login = NOW() WHERE id = $1", user.ID); err != nil {
		s.logger.Warn("Failed to update last_login", zap.Error(err), zap.String("user_id", user.ID.String()))
	}

	s.logAuditEvent(ctx, AuditEventTokenRefresh, user.ID, user.TenantID, nil)

	return newPair, nil
}

// ValidateAccessToken validates a JWT access token and enriches the user context
// with tenant plan information for rate limiting.
func (s *Service) ValidateAccessToken(ctx context.Context, token string) (*UserContext, error) {
	userCtx, err := s.jwtManager.ValidateAccessToken(token)
	if err != nil {
		return nil, err
	}

	var tenant Tenant
	if err := s.db.GetContext(ctx, &tenant,
		"SELECT id, plan FROM auth.tenants WHERE id = $1", userCtx.TenantID); err != nil {
		s.logger.Warn("Failed to get tenant for JWT auth, defaulting to free",
			zap.Error(err),
			zap.String("tenant_id", userCtx.TenantID.String()))
		userCtx.TenantPlan = PlanFree
		return userCtx, nil
	}

	userCtx.TenantPlan = tenant.Plan
	return userCtx, nil
}

// ValidateAPIKey validates an API key and returns user context
func (s *Service) ValidateAPIKey(ctx context.Context, apiKey string) (*UserContext, error) {
	// Extract key prefix (first 8 chars)
	if len(apiKey) < 8 {
		return nil, fmt.Errorf("invalid API key format")
	}
	keyPrefix := apiKey[:8]
	keyHash := hashToken(apiKey)

	var keys []APIKey
	query := `
		SELECT id, key_hash, key_prefix, user_id, tenant_id, name, description,
		       scopes, rate_limit_per_hour, last_used, expires_at, is_active, created_at
		FROM auth.api_keys
		WHERE key_prefix = $1 AND is_active = true
	`
	err := s.db.SelectContext(ctx, &keys, query, keyPrefix)
	if err != nil {
		s.logger.Error("Failed to query API keys",
			zap.String("key_prefix", keyPrefix),
			zap.Error(err))
		return nil, fmt.Errorf("failed to query API keys: %w", err)
	}

	s.logger.Debug("Found API keys",
		zap.String("key_prefix", keyPrefix),
		zap.Int("count", len(keys)))

	// Find matching key with constant-time comparison
	var key *APIKey
	for _, k := range keys {
		if compareTokenHash(k.KeyHash, keyHash) {
			key = &k
			break
		}
	}

	if key == nil {
		return nil, fmt.Errorf("invalid API key")
	}

	// Check expiration
	if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("API key expired")
	}

	// Update last used via bounded worker pool (prevents unbounded goroutines)
	s.enqueueUpdate("UPDATE auth.api_keys SET last_used = NOW() WHERE id = $1", key.ID)

	// Get user details
	var user User
	err = s.db.GetContext(ctx, &user,
		"SELECT * FROM auth.users WHERE id = $1", key.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user for API key: %w", err)
	}

	// Get tenant details for plan info (enterprise rate limiting)
	var tenant Tenant
	err = s.db.GetContext(ctx, &tenant,
		"SELECT id, plan FROM auth.tenants WHERE id = $1", user.TenantID)
	if err != nil {
		s.logger.Warn("Failed to get tenant for API key, using default plan",
			zap.Error(err),
			zap.String("tenant_id", user.TenantID.String()))
		tenant.Plan = PlanFree
	}

	// Log audit event
	s.logAuditEvent(ctx, AuditEventAPIKeyUsed, user.ID, user.TenantID,
		map[string]interface{}{"api_key_id": key.ID.String()})

	return &UserContext{
		UserID:     user.ID,
		TenantID:   user.TenantID,
		Username:   user.Username,
		Email:      user.Email,
		Role:       user.Role,
		Scopes:     []string(key.Scopes),
		IsAPIKey:   true,
		TokenType:  "api_key",
		APIKeyID:   key.ID,
		APIKeyTier: "free",
		TenantPlan: tenant.Plan,
	}, nil
}

// CreateAPIKey creates a new API key for a user
func (s *Service) CreateAPIKey(ctx context.Context, userID uuid.UUID, req *CreateAPIKeyRequest) (string, *APIKey, error) {
	// Get user to verify they exist and get tenant ID
	var user User
	err := s.db.GetContext(ctx, &user,
		"SELECT * FROM auth.users WHERE id = $1", userID)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get user: %w", err)
	}

	// Generate API key
	apiKey, keyHash, keyPrefix, err := generateAPIKey()
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate API key: %w", err)
	}

	// Set default scopes if not provided
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = []string{
			ScopeWorkflowsRead, ScopeWorkflowsWrite,
			ScopeAgentsExecute,
			ScopeSessionsRead, ScopeSessionsWrite,
		}
	}

	// Create API key record
	// Handle optional description
	var description *string
	if req.Description != "" {
		description = &req.Description
	}

	key := &APIKey{
		ID:               uuid.New(),
		KeyHash:          keyHash,
		KeyPrefix:        keyPrefix,
		UserID:           userID,
		TenantID:         user.TenantID,
		Name:             req.Name,
		Description:      description,
		Scopes:           pq.StringArray(scopes),
		RateLimitPerHour: 1000,
		ExpiresAt:        req.ExpiresAt,
		IsActive:         true,
		CreatedAt:        time.Now(),
	}

	query := `
		INSERT INTO auth.api_keys
		(id, key_hash, key_prefix, user_id, tenant_id, name, description, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	_, err = s.db.ExecContext(ctx, query,
		key.ID, key.KeyHash, key.KeyPrefix, key.UserID, key.TenantID,
		key.Name, key.Description, key.Scopes, key.ExpiresAt)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create API key: %w", err)
	}

	// Log audit event
	s.logAuditEvent(ctx, AuditEventAPIKeyCreated, userID, user.TenantID,
		map[string]interface{}{"api_key_id": key.ID.String(), "name": key.Name})

	s.logger.Info("API key created successfully",
		zap.String("key_id", key.ID.String()),
		zap.String("user_id", userID.String()),
		zap.String("name", key.Name))

	// Return the actual API key (only shown once)
	return apiKey, key, nil
}

// RegisterFromOAuth creates or retrieves a user from OAuth provider
// Returns JWT tokens for web session. API key is only created for NEW users.
// Existing users should use their previously created API keys for SDK access.
func (s *Service) RegisterFromOAuth(ctx context.Context, googleUser *GoogleUser) (*OAuthLoginResult, error) {
	// Check if user already exists by email
	var existingUser User
	query := `SELECT * FROM auth.users WHERE email = $1`
	err := s.db.GetContext(ctx, &existingUser, query, googleUser.Email)

	if err == nil {
		// User exists - return JWT tokens only (no new API key)
		// Users should use their existing API keys for SDK/programmatic access
		s.logger.Info("Existing user logging in via OAuth",
			zap.String("email", googleUser.Email),
			zap.String("user_id", existingUser.ID.String()))

		// Generate JWT tokens for web session
		tokens, refreshTokenHash, err := s.jwtManager.GenerateTokenPair(&existingUser)
		if err != nil {
			return nil, fmt.Errorf("failed to generate tokens: %w", err)
		}

		// Store refresh token (fail if unable - consistent with Login())
		if err := s.storeRefreshToken(ctx, &existingUser, refreshTokenHash); err != nil {
			return nil, fmt.Errorf("failed to store refresh token: %w", err)
		}

		// Update last login time
		s.db.ExecContext(ctx, "UPDATE auth.users SET last_login = NOW() WHERE id = $1", existingUser.ID)

		return &OAuthLoginResult{
			Tokens:    tokens,
			User:      &existingUser,
			APIKey:    "", // No new API key for existing users
			IsNewUser: false,
		}, nil
	}

	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to check user existence: %w", err)
	}

	// User doesn't exist - create new user, tenant, and API key
	s.logger.Info("Creating new user from OAuth",
		zap.String("email", googleUser.Email),
		zap.String("name", googleUser.Name))

	// Generate username from email (before @ symbol)
	username := googleUser.Email
	if atIdx := strings.Index(googleUser.Email, "@"); atIdx > 0 {
		username = googleUser.Email[:atIdx]
	}

	// Ensure username is unique
	baseUsername := username
	suffix := 0
	for {
		var exists bool
		err = s.db.GetContext(ctx, &exists,
			"SELECT EXISTS(SELECT 1 FROM auth.users WHERE username = $1)", username)
		if err != nil {
			return nil, fmt.Errorf("failed to check username uniqueness: %w", err)
		}
		if !exists {
			break
		}
		suffix++
		username = fmt.Sprintf("%s%d", baseUsername, suffix)
	}

	// Create tenant for new user
	tenantID, err := s.createTenant(ctx, username, "shannon")
	if err != nil {
		return nil, fmt.Errorf("failed to create tenant: %w", err)
	}

	// Create user (no password since OAuth-only)
	newUser := &User{
		ID:              uuid.New(),
		Email:           googleUser.Email,
		Username:        username,
		PasswordHash:    "", // OAuth users don't have passwords
		FullName:        &googleUser.Name,
		TenantID:        tenantID,
		Role:            RoleUser,
		IsActive:        true,
		IsVerified:      googleUser.EmailVerified,
		EmailVerifiedAt: func() *time.Time { t := time.Now(); return &t }(),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
		Metadata: JSONMap{
			"oauth_provider": "google",
			"oauth_sub":      googleUser.Sub,
			"picture":        googleUser.Picture,
		},
	}

	insertQuery := `
		INSERT INTO auth.users (id, email, username, password_hash, full_name, tenant_id, role, is_active, is_verified, email_verified_at, metadata)
		VALUES (:id, :email, :username, :password_hash, :full_name, :tenant_id, :role, :is_active, :is_verified, :email_verified_at, :metadata)
	`

	_, err = s.db.NamedExecContext(ctx, insertQuery, newUser)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	// Log audit event
	s.logAuditEvent(ctx, AuditEventAccountCreated, newUser.ID, tenantID, map[string]interface{}{
		"oauth_provider": "google",
		"email":          googleUser.Email,
	})

	s.logger.Info("User registered successfully via OAuth",
		zap.String("user_id", newUser.ID.String()),
		zap.String("email", newUser.Email),
		zap.String("tenant_id", tenantID.String()))

	// Create initial API key for new user (for SDK/programmatic access)
	apiKey, _, err := s.CreateAPIKey(ctx, newUser.ID, &CreateAPIKeyRequest{
		Name:        "Default API Key",
		Description: "Auto-generated from Google OAuth registration",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	// Generate JWT tokens for web session
	tokens, refreshTokenHash, err := s.jwtManager.GenerateTokenPair(newUser)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// Store refresh token (fail if unable - consistent with Login())
	if err := s.storeRefreshToken(ctx, newUser, refreshTokenHash); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	return &OAuthLoginResult{
		Tokens:    tokens,
		User:      newUser,
		APIKey:    apiKey, // Only returned for new users
		IsNewUser: true,
	}, nil
}

// Helper functions

// GetQuotaDefaults returns a copy of the built-in quota defaults for all plans.
func (s *Service) GetQuotaDefaults() map[string]QuotaDefaults {
	result := make(map[string]QuotaDefaults, len(s.quotaDefaults))
	for plan, defaults := range s.quotaDefaults {
		result[plan] = defaults
	}
	return result
}

// ResolveQuotaDefaults looks up quota defaults for a product+plan combination.
// Tries "{product}_{plan}" first, falls back to "{plan}".
func (s *Service) ResolveQuotaDefaults(product, plan string) QuotaDefaults {
	if product != "" && product != "shannon" {
		if q, ok := s.quotaDefaults[product+"_"+plan]; ok {
			return q
		}
	}
	if q, ok := s.quotaDefaults[plan]; ok {
		return q
	}
	// Hardcoded fallback (Model C free tier)
	s.logger.Warn("Quota defaults not found, using fallback",
		zap.String("product", product), zap.String("plan", plan))
	return QuotaDefaults{MonthlyTokens: 1_000_000, DailyTokens: 500_000, HardCapMonthlyTokens: 1_000_000}
}

func (s *Service) createTenant(ctx context.Context, username, product string) (uuid.UUID, error) {
	if product == "" {
		product = "shannon"
	}

	quotas := s.ResolveQuotaDefaults(product, "free")

	tenant := &Tenant{
		ID:               uuid.New(),
		Name:             fmt.Sprintf("%s's Workspace", username),
		Slug:             fmt.Sprintf("%s-%s", username, generateRandomString(6)),
		Plan:             PlanFree,
		TokenLimit:       quotas.MonthlyTokens,
		RateLimitPerHour: 1000, // From rate_limits.yaml free tier: requests_per_hour
		IsActive:         true,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}

	// Store product and max_schedules in tenant metadata
	metadata := map[string]interface{}{"product": product}
	if quotas.MaxSchedules > 0 {
		metadata["max_schedules"] = quotas.MaxSchedules
	}
	metadataJSON, _ := json.Marshal(metadata)

	query := `
		INSERT INTO auth.tenants (id, name, slug, plan, token_limit, rate_limit_per_hour, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`

	_, err := s.db.ExecContext(ctx, query,
		tenant.ID, tenant.Name, tenant.Slug, tenant.Plan, tenant.TokenLimit, tenant.RateLimitPerHour, metadataJSON)
	if err != nil {
		return uuid.Nil, err
	}

	s.logger.Info("Created tenant with product-aware quotas",
		zap.String("product", product),
		zap.Int("monthly_tokens", quotas.MonthlyTokens),
		zap.Int("max_schedules", quotas.MaxSchedules))

	return tenant.ID, nil
}

func (s *Service) storeRefreshToken(ctx context.Context, user *User, tokenHash string) error {
	query := `
		INSERT INTO auth.refresh_tokens (user_id, tenant_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`

	expiresAt := time.Now().Add(s.jwtManager.refreshTokenExpiry)
	_, err := s.db.ExecContext(ctx, query, user.ID, user.TenantID, tokenHash, expiresAt)
	return err
}

// storeRefreshTokenTx stores a refresh token within a transaction
func (s *Service) storeRefreshTokenTx(ctx context.Context, tx *sqlx.Tx, user *User, tokenHash string) error {
	query := `
		INSERT INTO auth.refresh_tokens (user_id, tenant_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
	`

	expiresAt := time.Now().Add(s.jwtManager.refreshTokenExpiry)
	_, err := tx.ExecContext(ctx, query, user.ID, user.TenantID, tokenHash, expiresAt)
	return err
}

func (s *Service) logAuditEvent(ctx context.Context, eventType string, userID, tenantID uuid.UUID, details map[string]interface{}) {
	query := `
		INSERT INTO auth.audit_logs (event_type, user_id, tenant_id, details)
		VALUES ($1, $2, $3, $4)
	`

	// Convert nil UUIDs to NULL
	var userIDPtr, tenantIDPtr *uuid.UUID
	if userID != uuid.Nil {
		userIDPtr = &userID
	}
	if tenantID != uuid.Nil {
		tenantIDPtr = &tenantID
	}

	// Serialize details map to JSON for JSONB column
	var detailsJSON []byte
	if details != nil {
		var err error
		detailsJSON, err = json.Marshal(details)
		if err != nil {
			s.logger.Warn("Failed to serialize audit event details",
				zap.String("event_type", eventType),
				zap.Error(err))
			detailsJSON = []byte("{}")
		}
	} else {
		detailsJSON = []byte("{}")
	}

	_, err := s.db.ExecContext(ctx, query, eventType, userIDPtr, tenantIDPtr, detailsJSON)
	if err != nil {
		s.logger.Warn("Failed to log audit event",
			zap.String("event_type", eventType),
			zap.Error(err))
	}
}

// ProvisionUser creates or retrieves a user under a specific tenant.
// It is idempotent: calling with the same username returns the existing user.
// By default a fresh API key is issued (prior "default" keys are revoked).
// Pass rotateKey=false to skip rotation for existing users; api_key will be empty.
// This is designed for BFF services (e.g. Sagasu) that authenticate users via their
// own OAuth (e.g. LINE Login) and then provision a per-user identity in Shannon.
//
// Schema constraints (from migrations/postgres/003_authentication.sql):
//   - username is UNIQUE globally (not per-tenant) and VARCHAR(100).
//   - Callers must use a stable, globally unique identifier as the username
//     (e.g. LINE user sub is U + 32 hex chars = 33 chars — safe).
//   - If the username is already registered under a different tenant, this returns
//     ErrUsernameTakenByOtherTenant.
//
// Concurrency: uses INSERT … ON CONFLICT DO NOTHING to make the create-or-return
// operation atomic at the DB level, eliminating the check-then-insert race.
//
// Partial failure: if user creation succeeds but API key creation fails, the user
// record is left without a key. A subsequent call will return the existing user
// with is_new_user=false and api_key="" — callers should handle this by retrying
// or prompting re-registration.
func (s *Service) ProvisionUser(ctx context.Context, tenantID uuid.UUID, username, displayName string, metadata JSONMap, rotateKey bool) (userID uuid.UUID, apiKey string, isNewUser bool, err error) {
	// Verify the target tenant exists before attempting any user work.
	var tenantExists bool
	if err = s.db.GetContext(ctx, &tenantExists,
		"SELECT EXISTS(SELECT 1 FROM auth.tenants WHERE id = $1 AND is_active = true)", tenantID); err != nil {
		return uuid.Nil, "", false, fmt.Errorf("failed to verify tenant: %w", err)
	}
	if !tenantExists {
		return uuid.Nil, "", false, ErrTenantNotFound
	}

	// Build synthetic, non-routable email. Format: provisioned+{username}@{tenantID}.internal
	// Deterministic (same user → same email), clearly non-real, satisfies UNIQUE constraint.
	syntheticEmail := fmt.Sprintf("provisioned+%s@%s.internal", username, tenantID.String())

	if metadata == nil {
		metadata = JSONMap{}
	}

	var fullName *string
	if displayName != "" {
		fullName = &displayName
	}
	now := time.Now()

	newUser := &User{
		ID:              uuid.New(),
		Email:           syntheticEmail,
		Username:        username,
		PasswordHash:    "", // no password — service-provisioned account
		FullName:        fullName,
		TenantID:        tenantID,
		Role:            RoleUser,
		IsActive:        true,
		IsVerified:      true, // trusted: BFF has already verified the external identity
		EmailVerifiedAt: &now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Metadata:        metadata,
	}

	// Wrap auth.users + public.users inserts in a transaction to eliminate partial
	// state: a crash between the two inserts would leave a user missing their
	// public.users row, causing silent FK failures on scheduled_tasks.
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return uuid.Nil, "", false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Atomic create-or-skip: ON CONFLICT DO NOTHING eliminates the check-then-insert
	// race. RowsAffected == 0 means another request already inserted this username.
	insertQuery := `
		INSERT INTO auth.users
			(id, email, username, password_hash, full_name, tenant_id, role, is_active, is_verified, email_verified_at, metadata)
		VALUES
			(:id, :email, :username, :password_hash, :full_name, :tenant_id, :role, :is_active, :is_verified, :email_verified_at, :metadata)
		ON CONFLICT (username) DO NOTHING
	`
	result, insertErr := tx.NamedExecContext(ctx, insertQuery, newUser)
	if insertErr != nil {
		err = insertErr
		return uuid.Nil, "", false, fmt.Errorf("failed to create user: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		// Username already exists (either pre-existing or lost a concurrent race).
		// Roll back the no-op transaction and fall through to the existing-user path.
		tx.Rollback()

		var existing User
		if err = s.db.GetContext(ctx, &existing,
			"SELECT * FROM auth.users WHERE username = $1", username); err != nil {
			return uuid.Nil, "", false, fmt.Errorf("failed to look up existing user: %w", err)
		}
		// Guard against cross-tenant username collision. Username is globally unique
		// in the schema; if another tenant holds it, that is an unresolvable conflict.
		if existing.TenantID != tenantID {
			return uuid.Nil, "", false, ErrUsernameTakenByOtherTenant
		}
		// Ensure public.users row exists — it may be missing if a previous provision
		// call succeeded on auth.users but failed before reaching this insert.
		if _, err = s.db.ExecContext(ctx, `
			INSERT INTO users (id, external_id, tenant_id, auth_user_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
			ON CONFLICT (id) DO NOTHING
		`, existing.ID, username, tenantID, existing.ID); err != nil {
			s.logger.Warn("Failed to ensure public.users row for existing user",
				zap.String("user_id", existing.ID.String()), zap.Error(err))
		}

		// If caller does not need a new key (e.g. logging in on another device
		// where the BFF already holds a valid key), skip rotation to avoid
		// invalidating sessions on other devices.
		if !rotateKey {
			return existing.ID, "", false, nil
		}

		// Issue an additional API key without revoking existing ones.
		// Each device gets its own key so multi-device sessions coexist.
		freshKey, _, err := s.CreateAPIKey(ctx, existing.ID, &CreateAPIKeyRequest{Name: "default"})
		if err != nil {
			return uuid.Nil, "", false, fmt.Errorf("failed to issue API key: %w", err)
		}
		return existing.ID, freshKey, false, nil
	}

	// New user: mirror into public.users inside the same transaction.
	// external_id = username (BFF's stable opaque identity, e.g. LINE sub).
	// auth_user_id links back to auth.users per migration 010_auth_user_link.
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO users (id, external_id, tenant_id, auth_user_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (id) DO NOTHING
	`, newUser.ID, username, tenantID, newUser.ID); err != nil {
		return uuid.Nil, "", false, fmt.Errorf("failed to create public user record: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return uuid.Nil, "", false, fmt.Errorf("failed to commit user creation: %w", err)
	}

	// Issue the initial API key (only returned once).
	// API key name is fixed at "default" to avoid overflowing api_keys.name VARCHAR(100)
	// regardless of username length (username can be up to 100 chars per schema).
	apiKeyStr, _, err := s.CreateAPIKey(ctx, newUser.ID, &CreateAPIKeyRequest{
		Name: "default",
	})
	if err != nil {
		// User record exists but has no API key. Caller will receive is_new_user=false
		// on a retry (rowsAffected will be 0) and must handle the missing-key case.
		s.logger.Error("API key creation failed after user insert — partial state",
			zap.String("user_id", newUser.ID.String()),
			zap.String("username", username),
			zap.Error(err))
		return uuid.Nil, "", false, fmt.Errorf("failed to create API key: %w", err)
	}

	s.logAuditEvent(ctx, AuditEventAccountCreated, newUser.ID, tenantID, map[string]interface{}{
		"provisioned_by": "admin_api",
		"username":       username,
	})

	s.logger.Info("User provisioned successfully",
		zap.String("user_id", newUser.ID.String()),
		zap.String("username", username),
		zap.String("tenant_id", tenantID.String()))

	return newUser.ID, apiKeyStr, true, nil
}

// ProvisionUserAutoTenant provisions a user with automatic tenant creation.
// If tenantID is uuid.Nil, a new tenant is created for the user (1 user = 1 tenant).
// If the user already exists (re-login), their existing tenant is reused regardless
// of the tenantID parameter — this makes it safe for callers to always omit tenantID.
func (s *Service) ProvisionUserAutoTenant(ctx context.Context, tenantID uuid.UUID, username, displayName string, metadata JSONMap, rotateKey bool) (userID uuid.UUID, resolvedTenantID uuid.UUID, apiKey string, isNewUser bool, err error) {
	// Check if user already exists — if so, use their existing tenant.
	var existing User
	existsErr := s.db.GetContext(ctx, &existing,
		"SELECT id, tenant_id FROM auth.users WHERE username = $1", username)
	if existsErr == nil {
		// User exists — delegate to standard ProvisionUser with their actual tenant.
		userID, apiKey, isNewUser, err = s.ProvisionUser(ctx, existing.TenantID, username, displayName, metadata, rotateKey)
		return userID, existing.TenantID, apiKey, isNewUser, err
	}
	if !errors.Is(existsErr, sql.ErrNoRows) {
		return uuid.Nil, uuid.Nil, "", false, fmt.Errorf("failed to check existing user: %w", existsErr)
	}

	// New user — create a tenant if none provided.
	autoCreatedTenant := false
	if tenantID == uuid.Nil {
		name := displayName
		if name == "" {
			name = username
		}
		// Extract product from metadata (e.g., frontend sends "product": "myapp")
		product := "shannon"
		if metadata != nil {
			if p, ok := metadata["product"].(string); ok && p != "" {
				product = p
			}
		}
		tenantID, err = s.createTenant(ctx, name, product)
		if err != nil {
			return uuid.Nil, uuid.Nil, "", false, fmt.Errorf("failed to create tenant for user: %w", err)
		}
		autoCreatedTenant = true
		s.logger.Info("Auto-created tenant for new user",
			zap.String("username", username),
			zap.String("tenant_id", tenantID.String()))
	}

	userID, apiKey, isNewUser, err = s.ProvisionUser(ctx, tenantID, username, displayName, metadata, rotateKey)
	if err != nil {
		if autoCreatedTenant {
			// Clean up orphaned tenant if user creation failed.
			if _, delErr := s.db.ExecContext(ctx, "DELETE FROM auth.tenants WHERE id = $1", tenantID); delErr != nil {
				s.logger.Error("Failed to clean up orphaned tenant — manual cleanup required",
					zap.String("tenant_id", tenantID.String()), zap.Error(delErr))
			}
		}
		// TOCTOU: if a concurrent request already created this user under a different
		// auto-created tenant, ProvisionUser returns ErrUsernameTakenByOtherTenant.
		// Retry the existing-user path.
		if errors.Is(err, ErrUsernameTakenByOtherTenant) {
			if retryErr := s.db.GetContext(ctx, &existing,
				"SELECT id, tenant_id FROM auth.users WHERE username = $1", username); retryErr == nil {
				userID, apiKey, isNewUser, err = s.ProvisionUser(ctx, existing.TenantID, username, displayName, metadata, rotateKey)
				return userID, existing.TenantID, apiKey, isNewUser, err
			}
		}
		return uuid.Nil, uuid.Nil, "", false, err
	}
	return userID, tenantID, apiKey, isNewUser, err
}

func generateAPIKey() (key, hash, prefix string, err error) {
	// Generate 32 random bytes
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	key = "sk_" + hex.EncodeToString(b)
	hash = hashToken(key)
	prefix = key[:8]
	return key, hash, prefix, nil
}

func generateRandomString(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}
