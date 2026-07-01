package openaicodex

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://chatgpt.com/backend-api/codex"

const (
	DefaultOAuthTokenURL = "https://auth.openai.com/oauth/token"
	DefaultOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// Transport mode constants for the Codex backend.
const (
	TransportAuto      = "auto"
	TransportHTTPS     = "https"
	TransportWebSocket = "websocket"
)

// DefaultWebSocketFallbackCooldown is the negative-cache window used when an auto
// transport WebSocket attempt fails before the first canonical event. During the
// cooldown, auto mode skips WebSocket and goes straight to HTTPS to avoid
// repeated dial/handshake latency on known-broken environments.
const DefaultWebSocketFallbackCooldown = 300 * time.Second

type Config struct {
	BaseURL                               string
	AccessToken                           string
	RefreshToken                          string
	AccountID                             string
	AuthJSONPath                          string
	OAuthTokenURL                         string
	OAuthClientID                         string
	HTTPClient                            *http.Client
	Models                                []string
	DefaultReasoningEffort                string
	ManagedOAuthEnabled                   bool
	ManagedOAuthStoragePath               string
	ManagedOAuthAccounts                  []string
	ManagedOAuthSelectionStrategy         string
	ManagedOAuthAllowAuthJSONFallback     bool
	ManagedOAuthSessionAffinityTTL        time.Duration
	ManagedOAuthSessionAffinityMaxEntries int
	RateLimitFallback                     time.Duration
	GPT55DowngradeDisabled                bool
	GPT55DowngradeSourceModel             string
	GPT55DowngradeTargetModel             string
	PlanTypeHint                          string
	Transport                             string
	ExperimentalWebSocket                 bool
	WebSocketFallbackCooldown             time.Duration
}

// NormalizeTransport returns the effective transport mode for cfg. An empty
// transport defaults to HTTPS. WebSocket and auto probing are experimental and
// must be enabled explicitly so live clients do not hit the WS path by default.
// An unknown value is rejected with an error so it surfaces through the standard
// config-error path.
func NormalizeTransport(raw string, experimentalWebSocket bool) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return TransportHTTPS, nil
	}
	switch t {
	case TransportAuto, TransportHTTPS, TransportWebSocket:
		if (t == TransportAuto || t == TransportWebSocket) && !experimentalWebSocket {
			return "", fmt.Errorf("%s: transport %q requires experimental_websocket: true", ID, t)
		}
		return t, nil
	default:
		return "", fmt.Errorf("%s: unknown transport %q (want %s, %s, or %s)", ID, raw, TransportAuto, TransportHTTPS, TransportWebSocket)
	}
}
