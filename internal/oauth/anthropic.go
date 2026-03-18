package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	// anthropicRefreshTokenSecretKey is the config_secrets key for the Anthropic refresh token.
	anthropicRefreshTokenSecretKey = "oauth.anthropic.refresh_token"

	// AnthropicProviderName is the provider name for Anthropic OAuth.
	AnthropicProviderName = "anthropic-oauth"
)

// AnthropicTokenResponse represents the response from the Anthropic token endpoint.
type AnthropicTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// AnthropicDBTokenSource provides a valid Anthropic access token backed by the llm_providers + config_secrets tables.
// Implements providers.TokenSource for Anthropic OAuth.
type AnthropicDBTokenSource struct {
	providerStore store.ProviderStore
	secretsStore  store.ConfigSecretsStore
	providerName  string

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// NewAnthropicDBTokenSource creates a DB-backed token source for Anthropic OAuth.
func NewAnthropicDBTokenSource(provStore store.ProviderStore, secretsStore store.ConfigSecretsStore, providerName string) *AnthropicDBTokenSource {
	return &AnthropicDBTokenSource{
		providerStore: provStore,
		secretsStore:  secretsStore,
		providerName:  providerName,
	}
}

// Token returns a valid Anthropic access token, refreshing if expired or about to expire.
func (ts *AnthropicDBTokenSource) Token() (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Use cached token if still valid
	if ts.cachedToken != "" && time.Until(ts.expiresAt) > refreshMargin {
		return ts.cachedToken, nil
	}

	ctx := context.Background()

	// Load from DB if not cached
	if ts.cachedToken == "" {
		p, err := ts.providerStore.GetProviderByName(ctx, ts.providerName)
		if err != nil {
			return "", fmt.Errorf("load anthropic oauth provider %q: %w", ts.providerName, err)
		}
		ts.cachedToken = p.APIKey

		var settings OAuthSettings
		if len(p.Settings) > 0 {
			_ = json.Unmarshal(p.Settings, &settings)
		}
		if settings.ExpiresAt > 0 {
			ts.expiresAt = time.Unix(settings.ExpiresAt, 0)
		}
	}

	// Refresh if expired or expiring soon
	if time.Until(ts.expiresAt) < refreshMargin {
		if err := ts.refreshAnthropic(ctx); err != nil {
			// If refresh fails but we still have a token, return it (might still work)
			if ts.cachedToken != "" {
				slog.Warn("anthropic oauth token refresh failed, using existing token", "error", err)
				return ts.cachedToken, nil
			}
			return "", fmt.Errorf("refresh anthropic oauth token: %w", err)
		}
	}

	return ts.cachedToken, nil
}

// refreshAnthropic gets the refresh token from config_secrets, calls RefreshAnthropicToken, and updates DB.
func (ts *AnthropicDBTokenSource) refreshAnthropic(ctx context.Context) error {
	refreshToken, err := ts.secretsStore.Get(ctx, anthropicRefreshTokenSecretKey)
	if err != nil {
		return fmt.Errorf("get anthropic refresh token: %w", err)
	}

	slog.Info("refreshing Anthropic OAuth token")
	newToken, err := RefreshAnthropicToken(refreshToken)
	if err != nil {
		return err
	}

	// Update cached values
	ts.cachedToken = newToken.AccessToken
	ts.expiresAt = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second)

	// Update provider api_key (access token) in DB
	p, err := ts.providerStore.GetProviderByName(ctx, ts.providerName)
	if err != nil {
		return fmt.Errorf("get anthropic provider for update: %w", err)
	}

	settings := OAuthSettings{
		ExpiresAt: ts.expiresAt.Unix(),
	}
	settingsJSON, _ := json.Marshal(settings)

	if err := ts.providerStore.UpdateProvider(ctx, p.ID, map[string]any{
		"api_key":  newToken.AccessToken,
		"settings": json.RawMessage(settingsJSON),
	}); err != nil {
		slog.Warn("failed to persist refreshed anthropic access token", "error", err)
	}

	// Update refresh token if a new one was issued
	if newToken.RefreshToken != "" {
		if err := ts.secretsStore.Set(ctx, anthropicRefreshTokenSecretKey, newToken.RefreshToken); err != nil {
			slog.Warn("failed to persist new anthropic refresh token", "error", err)
		}
	}

	return nil
}

// SaveAnthropicOAuthResult persists Anthropic OAuth tokens after a successful exchange.
// Creates or updates the provider in llm_providers and stores refresh token in config_secrets.
// Returns the provider ID.
func (ts *AnthropicDBTokenSource) SaveAnthropicOAuthResult(ctx context.Context, tokenResp *AnthropicTokenResponse) (uuid.UUID, error) {
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	settings := OAuthSettings{
		ExpiresAt: expiresAt.Unix(),
		Scopes:    tokenResp.Scope,
	}
	settingsJSON, _ := json.Marshal(settings)

	// Update cache
	ts.mu.Lock()
	ts.cachedToken = tokenResp.AccessToken
	ts.expiresAt = expiresAt
	ts.mu.Unlock()

	// Check if provider already exists
	existing, err := ts.providerStore.GetProviderByName(ctx, ts.providerName)
	if err == nil {
		// Update existing provider
		if err := ts.providerStore.UpdateProvider(ctx, existing.ID, map[string]any{
			"api_key":  tokenResp.AccessToken,
			"settings": json.RawMessage(settingsJSON),
			"enabled":  true,
		}); err != nil {
			return uuid.Nil, fmt.Errorf("update anthropic provider: %w", err)
		}

		// Save refresh token
		if tokenResp.RefreshToken != "" {
			if err := ts.secretsStore.Set(ctx, anthropicRefreshTokenSecretKey, tokenResp.RefreshToken); err != nil {
				return uuid.Nil, fmt.Errorf("save anthropic refresh token: %w", err)
			}
		}

		return existing.ID, nil
	}

	// Create new provider
	p := &store.LLMProviderData{
		Name:         ts.providerName,
		DisplayName:  "Claude (OAuth)",
		ProviderType: store.ProviderAnthropicOAuth,
		APIBase:      "https://api.anthropic.com/v1",
		APIKey:       tokenResp.AccessToken,
		Enabled:      true,
		Settings:     settingsJSON,
	}
	if err := ts.providerStore.CreateProvider(ctx, p); err != nil {
		return uuid.Nil, fmt.Errorf("create anthropic provider: %w", err)
	}

	// Save refresh token
	if tokenResp.RefreshToken != "" {
		if err := ts.secretsStore.Set(ctx, anthropicRefreshTokenSecretKey, tokenResp.RefreshToken); err != nil {
			return uuid.Nil, fmt.Errorf("save anthropic refresh token: %w", err)
		}
	}

	return p.ID, nil
}

// DeleteAnthropic removes the Anthropic OAuth provider from DB and its refresh token from config_secrets.
func (ts *AnthropicDBTokenSource) DeleteAnthropic(ctx context.Context) error {
	ts.mu.Lock()
	ts.cachedToken = ""
	ts.expiresAt = time.Time{}
	ts.mu.Unlock()

	// Delete refresh token from config_secrets
	_ = ts.secretsStore.Delete(ctx, anthropicRefreshTokenSecretKey)

	// Delete provider from llm_providers
	p, err := ts.providerStore.GetProviderByName(ctx, ts.providerName)
	if err != nil {
		return nil // already gone
	}
	return ts.providerStore.DeleteProvider(ctx, p.ID)
}

// ExistsAnthropic checks if an Anthropic OAuth provider exists and has a valid token.
func (ts *AnthropicDBTokenSource) ExistsAnthropic(ctx context.Context) bool {
	p, err := ts.providerStore.GetProviderByName(ctx, ts.providerName)
	return err == nil && p.APIKey != ""
}

// RefreshAnthropicToken refreshes an expired Anthropic access token using the refresh token.
// This is a placeholder implementation - the actual Anthropic OAuth flow would need to be implemented.
func RefreshAnthropicToken(refreshToken string) (*AnthropicTokenResponse, error) {
	// TODO: Implement actual Anthropic OAuth token refresh
	// For now, this is a placeholder that would need the actual Anthropic OAuth endpoints
	return nil, fmt.Errorf("anthropic oauth token refresh not implemented - placeholder for actual OAuth flow")
}