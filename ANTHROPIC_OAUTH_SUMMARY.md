# Anthropic OAuth Token Authentication Support

## Summary

Successfully modified GoClaw to support Anthropic OAuth token authentication alongside the existing API key mode. This implementation follows the same pattern used for OpenAI OAuth (CodexProvider) and maintains full backward compatibility.

## What Was Implemented

### 1. Enhanced AnthropicProvider (`internal/providers/anthropic.go`)

- **Added `tokenSource TokenSource` field** - Optional OAuth token source
- **New constructor**: `NewAnthropicProviderWithToken(tokenSource TokenSource, opts...)` 
- **Updated authentication logic** in `doRequest()`:
  - When `tokenSource` is set: uses `Authorization: Bearer <token>` header
  - When `tokenSource` is nil: uses `x-api-key: <api_key>` header (existing behavior)
- **Backward compatibility**: Existing `NewAnthropicProvider(apiKey, opts...)` unchanged

### 2. Configuration Support (`internal/config/config_channels.go`)

- **Added `Mode` field to `ProviderConfig`**: 
  - `"api_key"` (default) - use API key authentication
  - `"token"` - use OAuth token authentication

### 3. OAuth Token Management (`internal/oauth/anthropic.go` - NEW FILE)

- **`AnthropicDBTokenSource`** - Database-backed token source
- **Token refresh logic** - Automatic refresh when tokens expire
- **Database persistence** - Stores access tokens in `llm_providers`, refresh tokens in `config_secrets`
- **CRUD operations** - Save, delete, and check existence of OAuth providers
- **Placeholder for OAuth flow** - `RefreshAnthropicToken()` ready for actual Anthropic OAuth implementation

### 4. Provider Registration Logic (`cmd/gateway_providers.go`)

- **Config-based registration**: 
  - `mode: "api_key"` → registers `NewAnthropicProvider()` with API key
  - `mode: "token"` → defers to database-based registration
- **Database registration**: 
  - `ProviderAnthropicOAuth` type → uses `NewAnthropicProviderWithToken()`

### 5. Provider Store (`internal/store/provider_store.go`)

- **Added `ProviderAnthropicOAuth`** constant and validation

## Usage Examples

### Configuration File (API Key Mode - Existing)
```json
{
  "providers": {
    "anthropic": {
      "api_key": "sk-ant-...",
      "api_base": "https://api.anthropic.com/v1"
    }
  }
}
```

### Configuration File (OAuth Token Mode - NEW)
```json
{
  "providers": {
    "anthropic": {
      "mode": "token"
    }
  }
}
```

### Programmatic Usage (NEW)
```go
// Create OAuth token source
tokenSource := oauth.NewAnthropicDBTokenSource(providerStore, secretStore, "anthropic-oauth")

// Create provider with OAuth
provider := providers.NewAnthropicProviderWithToken(tokenSource, 
    providers.WithAnthropicBaseURL("https://api.anthropic.com/v1"))

// Use provider normally - authentication handled automatically
response, err := provider.Chat(ctx, request)
```

## Architecture

```
┌─────────────────────┐    ┌─────────────────────┐
│  AnthropicProvider  │    │   TokenSource       │
│                     │    │   Interface         │
│  - apiKey: string   │    │                     │
│  - tokenSource: TS  │◄───┤  - Token() string   │
│  - doRequest()      │    └─────────────────────┘
└─────────────────────┘              ▲
           │                         │
           ▼                         │
┌─────────────────────┐              │
│   Authentication    │              │
│                     │              │
│ if tokenSource:     │              │
│   Bearer <token>    │              │
│ else:               │              │
│   x-api-key <key>   │              │
└─────────────────────┘              │
                                     │
                       ┌─────────────────────┐
                       │ AnthropicDBTokenSrc │
                       │                     │
                       │ - DB-backed         │
                       │ - Auto-refresh      │
                       │ - Secure storage    │
                       └─────────────────────┘
```

## Database Schema

### llm_providers table (OAuth provider)
```sql
{
  "name": "anthropic-oauth",
  "display_name": "Claude (OAuth)", 
  "provider_type": "anthropic_oauth",
  "api_base": "https://api.anthropic.com/v1",
  "api_key": "<access_token>",  -- OAuth access token
  "enabled": true,
  "settings": {
    "expires_at": 1234567890,
    "scopes": "..."
  }
}
```

### config_secrets table (refresh token)
```sql
{
  "key": "oauth.anthropic.refresh_token",
  "value": "<encrypted_refresh_token>"
}
```

## What's Missing (For Full OAuth Implementation)

1. **Anthropic OAuth Flow**: Actual OAuth authorization/token exchange endpoints
2. **CLI Commands**: `goclaw auth add anthropic --oauth` command
3. **Web UI**: OAuth setup in provider management interface
4. **Token Refresh**: Real Anthropic token refresh API implementation

## Testing

To verify the changes work:

1. **API Key Mode** (should work unchanged):
```bash
# Set config with api_key, start gateway, test Anthropic requests
```

2. **OAuth Mode** (placeholder):
```bash
# Set config with mode: "token"
# OAuth provider would need to be added to DB manually for testing
```

## Migration Path

1. **Existing deployments**: No changes required - API key mode is default
2. **New OAuth deployments**: Set `mode: "token"` in config
3. **Migration**: Can switch modes by updating config and re-registering provider

## Files Modified

- `cmd/gateway_providers.go` (13 lines changed)
- `internal/config/config_channels.go` (1 line added)
- `internal/providers/anthropic.go` (34 lines changed)
- `internal/store/provider_store.go` (2 lines added)
- `internal/oauth/anthropic.go` (236 lines, new file)

**Total: 286 lines added, 5 lines removed**

## Backward Compatibility

✅ **Fully backward compatible**
- Existing API key configurations work unchanged
- No breaking changes to AnthropicProvider interface
- Default behavior is API key mode
- OAuth is opt-in only

This implementation provides a solid foundation for Anthropic OAuth authentication in GoClaw while maintaining all existing functionality.