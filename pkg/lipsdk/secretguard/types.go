package secretguard

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/execview"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace"
)

// Outcome is the secret-guard decision for one Evaluate invocation.
type Outcome string

const (
	OutcomePass     Outcome = "pass"
	OutcomeLog      Outcome = "log"
	OutcomeRedacted Outcome = "redacted"
	OutcomeBlock    Outcome = "block"
)

// SourceCategory classifies where a matched secret reference originated.
type SourceCategory string

const (
	SourceCategoryProxyEnv    SourceCategory = "proxy_env"
	SourceCategoryPopularEnv  SourceCategory = "popular_env"
	SourceCategoryOperatorEnv SourceCategory = "operator_env"
	SourceCategoryRequestCred SourceCategory = "request_credential"
	SourceCategoryUnknown     SourceCategory = "unknown"
)

// Finding is safe match metadata. It must never carry a secret value or content excerpt.
type Finding struct {
	SecretRefName   string
	Aliases         []string
	SourceCategory  SourceCategory
	Location        string
	OccurrenceCount int
}

// Decision is the Evaluate result: outcome, safe findings, and scan metadata.
type Decision struct {
	Outcome       Outcome
	Findings      []Finding
	MutationCount int
	ScanLimitHit  bool
	// FailureKind is a bounded operator-safe classifier (e.g. scan_limit, provider_error).
	FailureKind string
	// FailureReason is a bounded operator-safe reason; never secret material or excerpts.
	FailureReason string
}

// Meta carries authoritative request context and sanitized ingress attribution.
// It must not include bearer tokens, resume tokens, or raw secret values.
type Meta struct {
	TraceID string

	Principal execview.PrincipalView
	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView

	PeerIP              string
	FrontendID          string
	Operation           string
	UserAgentDigest     string
	AgentIdentityDigest string
	DeviceID            string
	KeyID               string
	Fingerprint         string
}

// Services are opaque capabilities available to a Guard. No raw secret accessor is provided.
type Services struct {
	MatcherResolver MatcherResolver
}

// MatcherResolver resolves the request-scoped opaque Matcher. Implementations own secret bytes privately.
type MatcherResolver interface {
	Resolve(ctx context.Context) (Matcher, error)
}

// Matcher scans and redacts textual inputs, returning only safe findings.
type Matcher interface {
	ScanBytes(ctx context.Context, input []byte) ([]Finding, error)
	ScanString(ctx context.Context, input string) ([]Finding, error)
	RedactBytes(ctx context.Context, input []byte) (redacted []byte, findings []Finding, err error)
	RedactString(ctx context.Context, input string) (redacted string, findings []Finding, err error)
}

// Guard evaluates one secrets-guard feature instance against a canonical call.
type Guard interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	Evaluate(ctx context.Context, call *lipapi.Call, meta Meta, services Services) (Decision, error)
}
