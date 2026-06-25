package openaicodex

import (
	"net/http"
	"time"
)

const DefaultBaseURL = "https://chatgpt.com/backend-api/codex"

const (
	DefaultOAuthTokenURL = "https://auth.openai.com/oauth/token"
	DefaultOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

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
	DefaultTemperature                    *float64
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
}
