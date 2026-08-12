package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

func TestValidateAccountingAuthorityDisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate disabled default: %v", err)
	}
	if cfg.Accounting.Authority.Enabled {
		t.Fatal("authority should remain disabled by default")
	}
	if cfg.Accounting.Authority.Query.Enabled {
		t.Fatal("authority query should remain disabled by default")
	}
}

func TestAccountingAuthorityEvaluationTimeoutDefaultsAndValidates(t *testing.T) {
	t.Parallel()
	defaultCfg := config.AccountingAuthorityConfig{}
	got, err := defaultCfg.EvaluationTimeoutDuration()
	if err != nil {
		t.Fatalf("default EvaluationTimeoutDuration: %v", err)
	}
	if got != config.DefaultAccountingAuthorityEvaluationTimeout {
		t.Fatalf("default evaluation timeout = %v, want %v", got, config.DefaultAccountingAuthorityEvaluationTimeout)
	}
	valid := config.AccountingAuthorityConfig{EvaluationTimeout: "17ms"}
	got, err = valid.EvaluationTimeoutDuration()
	if err != nil || got != 17*time.Millisecond {
		t.Fatalf("valid evaluation timeout = %v, err=%v, want 17ms", got, err)
	}
	for _, raw := range []string{"nope", "0s", "-1ms"} {
		if _, err := (config.AccountingAuthorityConfig{EvaluationTimeout: raw}).EvaluationTimeoutDuration(); err == nil {
			t.Fatalf("EvaluationTimeoutDuration(%q) must fail", raw)
		}
	}
}

func TestAccountingAuthorityCleanupTimeoutDefaultsAndValidates(t *testing.T) {
	t.Parallel()
	got, err := (config.AccountingAuthorityConfig{}).CleanupTimeoutDuration()
	if err != nil || got != config.DefaultAccountingAuthorityCleanupTimeout {
		t.Fatalf("default cleanup timeout = %v, err=%v", got, err)
	}
	got, err = (config.AccountingAuthorityConfig{CleanupTimeout: "17ms"}).CleanupTimeoutDuration()
	if err != nil || got != 17*time.Millisecond {
		t.Fatalf("valid cleanup timeout = %v, err=%v", got, err)
	}
	for _, raw := range []string{"nope", "0s", "-1ms"} {
		if _, err := (config.AccountingAuthorityConfig{CleanupTimeout: raw}).CleanupTimeoutDuration(); err == nil {
			t.Fatalf("CleanupTimeoutDuration(%q) must fail", raw)
		}
	}
}

func TestValidateAccountingAuthorityNormalizesEvaluationTimeout(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Accounting: config.AccountingConfig{Authority: config.AccountingAuthorityConfig{
		Enabled: true, Store: "memory", Mode: "advisory", StartupPosture: "fail_open",
	}}}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Accounting.Authority.EvaluationTimeout == "" {
		t.Fatal("Validate must normalize a missing evaluation_timeout")
	}
	if got, err := cfg.Accounting.Authority.EvaluationTimeoutDuration(); err != nil || got != config.DefaultAccountingAuthorityEvaluationTimeout {
		t.Fatalf("normalized evaluation timeout = %v, err=%v", got, err)
	}
	if cfg.Accounting.Authority.CleanupTimeout == "" {
		t.Fatal("Validate must normalize a missing cleanup_timeout")
	}
	if got, err := cfg.Accounting.Authority.CleanupTimeoutDuration(); err != nil || got != config.DefaultAccountingAuthorityCleanupTimeout {
		t.Fatalf("normalized cleanup timeout = %v, err=%v", got, err)
	}
}

func TestValidateAccountingAuthorityRejectsStrictRuleInAdvisoryMode(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("x", 12)},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Mode:    "advisory",
				Store:   "memory",
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Basis: "legacy_provider_preferred_attempt",
						Match: config.AccountingAuthorityDimensionsConfig{Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")}},
					},
				},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected advisory/strict mismatch to fail validation")
	}
	if !strings.Contains(err.Error(), "strict rules require accounting.authority.mode=strict") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccountingAuthorityRejectsQueryWithoutSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Store:   "memory",
				Query:   config.AccountingAuthorityQueryConfig{Enabled: true, PathPrefix: "/authority"},
				Rules:   []config.AccountingAuthorityRuleConfig{},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected authority query without shared secret to fail")
	}
	if !strings.Contains(err.Error(), "diagnostics.shared_secret") && !strings.Contains(err.Error(), "accounting.authority.query.enabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func authorityQueryPathConfig(path string) config.Config {
	return config.Config{
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("x", 12)},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Store:   "memory",
				Query:   config.AccountingAuthorityQueryConfig{Enabled: true, PathPrefix: path},
				Rules:   []config.AccountingAuthorityRuleConfig{},
			},
		},
	}
}

func TestValidateAccountingAuthorityQueryOverlapWithEnabledAdminRejected(t *testing.T) {
	t.Parallel()
	cfg := authorityQueryPathConfig("/accounting")
	cfg.Accounting.Admin = config.AccountingAdminConfig{Enabled: true, Path: "/accounting"}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error vs enabled accounting.admin.path, got %v", err)
	}
}

func TestValidateAccountingAuthorityQueryPrefixOverlapWithEnabledAdminRejected(t *testing.T) {
	t.Parallel()
	cfg := authorityQueryPathConfig("/accounting/authority")
	cfg.Accounting.Admin = config.AccountingAdminConfig{Enabled: true, Path: "/accounting"}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected prefix overlap error vs enabled accounting.admin.path, got %v", err)
	}
}

func TestValidateAccountingAuthorityQueryOverlapWithControlPlaneRejected(t *testing.T) {
	t.Parallel()
	cfg := authorityQueryPathConfig("/ops")
	cfg.ControlPlane = config.ControlPlaneConfig{
		Enabled: true,
		Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/ops"},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error vs control_plane.query.path_prefix, got %v", err)
	}
}

func TestValidateAccountingAuthorityQueryPrefixOverlapWithControlPlaneRejected(t *testing.T) {
	t.Parallel()
	cfg := authorityQueryPathConfig("/ops/authority")
	cfg.ControlPlane = config.ControlPlaneConfig{
		Enabled: true,
		Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/ops"},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected prefix overlap error vs control_plane.query.path_prefix, got %v", err)
	}
}

func TestValidateAccountingAuthorityQueryAllowsDisabledAdminLeftoverPath(t *testing.T) {
	t.Parallel()
	// Disabled admin is not mounted, so a leftover path must not block an
	// authority query prefix nested under it.
	cfg := authorityQueryPathConfig("/accounting/authority")
	cfg.Accounting.Admin = config.AccountingAdminConfig{Enabled: false, Path: "/accounting"}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("disabled admin leftover path must not block authority query, got %v", err)
	}
}

func TestValidateAccountingAuthorityRejectsLargeMaxPageSize(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("x", 12)},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled: true,
				Store:   "memory",
				Query: config.AccountingAuthorityQueryConfig{
					Enabled:     true,
					PathPrefix:  "/authority",
					MaxPageSize: 150,
				},
				Rules: []config.AccountingAuthorityRuleConfig{},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected authority query with max page size > 100 to fail")
	}
	if !strings.Contains(err.Error(), "max_page_size: must not exceed 100") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateAccountingAuthorityRejectsStrictModeWithFailOpen reproduces the
// config-validation gap: accounting.authority.mode=strict combined with
// startup_posture=fail_open (advisory-only backing) must be rejected because
// strict rules require atomic backing. Per-rule validation alone cannot catch
// this; the AuthorityConfig-level invariant must also run.
func TestValidateAccountingAuthorityRejectsStrictModeWithFailOpen(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_open",
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Basis: "legacy_provider_preferred_attempt",
						Match: config.AccountingAuthorityDimensionsConfig{
							Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
						},
					},
				},
			},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected strict mode with fail_open posture to be rejected (strict rules require atomic backing)")
	}
	if !strings.Contains(err.Error(), "strict rules require atomic backing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateAccountingAuthorityAcceptsStrictModeWithAtomicBacking guards the
// valid counterpart: strict mode backed by fail_closed (atomic) startup posture
// with a strict rule must continue to validate.
func TestValidateAccountingAuthorityAcceptsStrictModeWithAtomicBacking(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Basis: "legacy_provider_preferred_attempt",
						Match: config.AccountingAuthorityDimensionsConfig{
							Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
						},
					},
				},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("strict mode with atomic backing should validate, got: %v", err)
	}
}

func TestValidateAccountingAuthorityRequiredEvidenceNeedsAuthorityAndControlPlane(t *testing.T) {
	t.Parallel()

	withoutControlPlane := &config.Config{ControlPlane: config.ControlPlaneConfig{
		RequiredCategories: []string{"accounting_authority"},
	}}
	if err := config.Validate(withoutControlPlane); err == nil || !strings.Contains(err.Error(), "control_plane.enabled") {
		t.Fatalf("required accounting evidence without control plane must fail validation, got %v", err)
	}

	withoutAuthority := &config.Config{
		ControlPlane: config.ControlPlaneConfig{
			Enabled:            true,
			Store:              "sqlite",
			SQLitePath:         "/tmp/control-plane.db",
			RecordingPolicy:    "required_pre_work",
			RequiredCategories: []string{"accounting_authority"},
		},
	}
	if err := config.Validate(withoutAuthority); err == nil || !strings.Contains(err.Error(), "accounting.authority.enabled") {
		t.Fatalf("required accounting evidence without authority must fail validation, got %v", err)
	}
}

// TestAccountingAuthorityDimensionsConfigCredentialMapsToDomain pins
// requirement 1.2: the credential matcher config must map into the domain
// DimensionsMatcher so rules can target the credential authority dimension,
// while an unconfigured credential stays a zero (wildcard) matcher.
func TestAccountingAuthorityDimensionsConfigCredentialMapsToDomain(t *testing.T) {
	t.Parallel()

	withCredential := config.AccountingAuthorityDimensionsConfig{
		Credential: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("cred-1")},
	}.DomainMatcher()
	if !withCredential.Credential.Value.Equal(scope.Known("cred-1")) {
		t.Fatalf("credential matcher value = %v, want cred-1", withCredential.Credential.Value)
	}
	if withCredential.Credential.MatchUnknown {
		t.Fatal("credential matcher MatchUnknown should be false for a known-value config")
	}

	withMatchUnknown := config.AccountingAuthorityDimensionsConfig{
		Credential: config.AccountingAuthorityDimensionMatcherConfig{MatchUnknown: true},
	}.DomainMatcher()
	if !withMatchUnknown.Credential.MatchUnknown {
		t.Fatal("credential matcher MatchUnknown should map to domain matcher")
	}

	zero := config.AccountingAuthorityDimensionsConfig{}.DomainMatcher()
	if zero.Credential.Value.IsKnown() || zero.Credential.MatchUnknown {
		t.Fatalf("unconfigured credential matcher must map to a zero (wildcard) matcher: %#v", zero.Credential)
	}
	if !zero.Credential.Matches(scope.Known("any")) {
		t.Fatal("unconfigured credential matcher must match any credential (backward compat)")
	}
}

// TestValidateAccountingAuthorityAcceptsCredentialMatcher guards that a rule
// configuring a credential matcher validates cleanly under strict atomic
// backing (requirement 1.2, 10.8).
func TestValidateAccountingAuthorityAcceptsCredentialMatcher(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        true,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.credential",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Basis: "legacy_provider_preferred_attempt",
						Match: config.AccountingAuthorityDimensionsConfig{
							Credential: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("cred-1")},
						},
					},
				},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("rule with credential matcher should validate, got: %v", err)
	}
}

func TestValidateAccountingAuthorityRejectsMonetaryRules(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		rule config.AccountingAuthorityRuleConfig
	}{
		{
			name: "budget kind",
			rule: config.AccountingAuthorityRuleConfig{
				ID:       "tenant.budget",
				Kind:     "budget",
				Mode:     "advisory",
				Unit:     "money_nano",
				Limit:    100,
				Currency: "usd",
				Basis:    "legacy_provider_preferred_attempt",
			},
		},
		{
			name: "spend_cap kind",
			rule: config.AccountingAuthorityRuleConfig{
				ID:       "tenant.spend_cap",
				Kind:     "spend_cap",
				Mode:     "advisory",
				Unit:     "money_nano",
				Limit:    100,
				Currency: "usd",
				Basis:    "legacy_provider_preferred_attempt",
			},
		},
		{
			name: "quota with money_nano unit",
			rule: config.AccountingAuthorityRuleConfig{
				ID:    "tenant.money_quota",
				Kind:  "quota",
				Mode:  "advisory",
				Unit:  "money_nano",
				Limit: 100,
				Basis: "legacy_provider_preferred_attempt",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Accounting: config.AccountingConfig{
					Authority: config.AccountingAuthorityConfig{
						Enabled:        true,
						Mode:           "advisory",
						Store:          "memory",
						StartupPosture: "fail_open",
						Rules:          []config.AccountingAuthorityRuleConfig{tt.rule},
					},
				},
			}
			err := config.Validate(cfg)
			if err == nil {
				t.Fatal("expected monetary authority rule to fail validation")
			}
			if !strings.Contains(err.Error(), "accounting.authority.rules[0]") {
				t.Fatalf("error should name the rule index, got %v", err)
			}
			if !strings.Contains(err.Error(), "retired") && !strings.Contains(err.Error(), "money_nano") && !strings.Contains(err.Error(), "budget") {
				t.Fatalf("error should reject retired monetary authority rules, got %v", err)
			}
		})
	}
}
