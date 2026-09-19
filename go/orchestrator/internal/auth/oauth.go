// =============================================================================
// 文件: go/orchestrator/internal/auth/oauth.go
// -----------------------------------------------------------------------------
// 【一句话功能】 Google OAuth 令牌验证和授权码交换
// 【关键内容】 VerifyIDToken 验证 Google ID Token；ExchangeAuthCode 授权码→令牌→用户信息；支持 Web/Desktop/iOS 三种客户端
// 【协作关系】 被 auth service 调用用于 OAuth 登录/注册
// =============================================================================
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

// GoogleUser represents user info extracted from Google OAuth
type GoogleUser struct {
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	Sub           string // Google user ID
}

// iOSClientID is the Google OAuth client ID for iOS public client flows.
// Set via IOS_GOOGLE_CLIENT_ID env var. Empty disables iOS OAuth.
var iOSClientID = os.Getenv("IOS_GOOGLE_CLIENT_ID")

// GoogleOAuthVerifier handles Google OAuth token verification
type GoogleOAuthVerifier struct {
	clientID            string
	clientSecret        string
	desktopClientID     string
	desktopClientSecret string
	httpClient          *http.Client
}

// NewGoogleOAuthVerifier creates a new Google OAuth verifier
// desktopClientID and desktopClientSecret are optional - for desktop/native app OAuth flows
func NewGoogleOAuthVerifier(clientID, clientSecret, desktopClientID, desktopClientSecret string) *GoogleOAuthVerifier {
	return &GoogleOAuthVerifier{
		clientID:            clientID,
		clientSecret:        clientSecret,
		desktopClientID:     desktopClientID,
		desktopClientSecret: desktopClientSecret,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// VerifyIDToken verifies a Google ID token and extracts user info
// Used for web flow where frontend sends ID token directly
func (v *GoogleOAuthVerifier) VerifyIDToken(ctx context.Context, idToken string) (*GoogleUser, error) {
	return v.verifyIDTokenWithAudience(ctx, idToken, v.clientID)
}

// verifyIDTokenWithAudience verifies a Google ID token against a specific client ID (audience)
func (v *GoogleOAuthVerifier) verifyIDTokenWithAudience(ctx context.Context, idToken, expectedAudience string) (*GoogleUser, error) {
	if expectedAudience == "" {
		return nil, fmt.Errorf("Google OAuth client ID not configured")
	}

	// Verify the ID token using Google's library
	payload, err := idtoken.Validate(ctx, idToken, expectedAudience)
	if err != nil {
		return nil, fmt.Errorf("invalid Google ID token: %w", err)
	}

	// Extract claims with safe type assertions (avoid panics on missing fields)
	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return nil, fmt.Errorf("email claim missing from Google token")
	}
	emailVerified, _ := payload.Claims["email_verified"].(bool)
	name, _ := payload.Claims["name"].(string)
	picture, _ := payload.Claims["picture"].(string)

	return &GoogleUser{
		Email:         email,
		EmailVerified: emailVerified,
		Name:          name,
		Picture:       picture,
		Sub:           payload.Subject,
	}, nil
}

// ExchangeAuthCode exchanges an authorization code for user info
// Used for desktop/mobile flows where we get auth code from deep link
func (v *GoogleOAuthVerifier) ExchangeAuthCode(ctx context.Context, authCode, redirectURI string) (*GoogleUser, error) {
	// Select credentials based on redirect_uri pattern
	// Desktop apps use custom schemes (shannon://) or loopback
	// iOS uses com.googleusercontent.apps.* scheme (public client - no secret)
	clientID, clientSecret, isPublicClient := v.selectCredentials(redirectURI)

	if clientID == "" {
		return nil, fmt.Errorf("Google OAuth credentials not configured for redirect_uri: %s", redirectURI)
	}

	// For confidential clients (web/desktop), require client_secret
	if !isPublicClient && clientSecret == "" {
		return nil, fmt.Errorf("Google OAuth client secret not configured for redirect_uri: %s", redirectURI)
	}

	// Exchange authorization code for tokens
	tokenURL := "https://oauth2.googleapis.com/token"
	values := url.Values{}
	values.Set("code", authCode)
	values.Set("client_id", clientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("grant_type", "authorization_code")

	// Only include client_secret for confidential clients (not for iOS public client)
	if !isPublicClient && clientSecret != "" {
		values.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange auth code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, body)
	}

	// Parse token response
	var tokenResp struct {
		IDToken string `json:"id_token"`
	}
	if err := parseJSON(resp.Body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Verify the ID token using the same client ID that was used for exchange
	return v.verifyIDTokenWithAudience(ctx, tokenResp.IDToken, clientID)
}

// selectCredentials returns the appropriate client ID and secret based on redirect_uri
// Returns: clientID, clientSecret, isPublicClient
// - Desktop/native apps use custom schemes or loopback addresses
// - iOS uses com.googleusercontent.apps.* scheme (public client - no secret needed)
func (v *GoogleOAuthVerifier) selectCredentials(redirectURI string) (clientID, clientSecret string, isPublicClient bool) {
	// iOS pattern: com.googleusercontent.apps.{client_id}:/oauth2redirect/google
	// iOS is a public client - no client_secret required
	if strings.Contains(redirectURI, "googleusercontent.apps") {
		return iOSClientID, "", true
	}

	// Desktop patterns: custom schemes or loopback for native apps
	isDesktop := strings.HasPrefix(redirectURI, "shannon://") ||
		strings.HasPrefix(redirectURI, "http://localhost") ||
		strings.HasPrefix(redirectURI, "http://127.0.0.1")

	if isDesktop && v.desktopClientID != "" && v.desktopClientSecret != "" {
		return v.desktopClientID, v.desktopClientSecret, false
	}

	// Default to web credentials
	return v.clientID, v.clientSecret, false
}

// parseJSON is a helper to parse JSON response
func parseJSON(body io.Reader, v interface{}) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
