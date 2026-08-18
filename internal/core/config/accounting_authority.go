package config

import (
	"fmt"
	"strings"
	"time"

	authoritydomain "github.com/matdev83/go-llm-interactive-proxy/internal/core/usageauthority/domain"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

// AccountingAuthorityConfig controls the optional usage-authority capability.
// It is disabled by default and only becomes visible when explicitly enabled.
type AccountingAuthorityConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Mode               string `yaml:"mode"`
	Store              string `yaml:"store"`
	SQLitePath         string `yaml:"sqlite_path"`
	PostgresDSN        string `yaml:"postgres_dsn"`
	StartupPosture     string `yaml:"startup_posture"`
	UnknownAttribution string `yaml:"unknown_attribution"`
	EvaluationTimeout  string `yaml:"evaluation_timeout"`
	CleanupTimeout     string `yaml:"cleanup_timeout"`
	// SnapshotVersion is the immutable config-backed policy version (requirement 11.5).
	// Empty defaults to "static" at source construction.
	SnapshotVersion string                          `yaml:"snapshot_version"`
	Query           AccountingAuthorityQueryConfig  `yaml:"query"`
	Rules           []AccountingAuthorityRuleConfig `yaml:"rules"`
}

const (
	DefaultAccountingAuthorityEvaluationTimeout = 250 * time.Millisecond
	DefaultAccountingAuthorityCleanupTimeout    = 2 * time.Second
)

// EvaluationTimeoutDuration returns the bounded admission evaluation budget.
// The zero configuration value is deliberately normalized to the conservative
// default so enabling authority cannot accidentally restore unbounded waits.
func (a AccountingAuthorityConfig) EvaluationTimeoutDuration() (time.Duration, error) {
	raw := strings.TrimSpace(a.EvaluationTimeout)
	if raw == "" {
		return DefaultAccountingAuthorityEvaluationTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("accounting.authority.evaluation_timeout: invalid duration %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("accounting.authority.evaluation_timeout: duration must be positive")
	}
	return d, nil
}

// CleanupTimeoutDuration returns the bounded budget for detached settlement,
// release, reconciliation, advisory usage, and compensation work.
func (a AccountingAuthorityConfig) CleanupTimeoutDuration() (time.Duration, error) {
	raw := strings.TrimSpace(a.CleanupTimeout)
	if raw == "" {
		return DefaultAccountingAuthorityCleanupTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("accounting.authority.cleanup_timeout: invalid duration %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("accounting.authority.cleanup_timeout: duration must be positive")
	}
	return d, nil
}

// AccountingAuthorityQueryConfig controls the protected status and bounded
// query routes for authority state.
type AccountingAuthorityQueryConfig struct {
	Enabled         bool   `yaml:"enabled"`
	PathPrefix      string `yaml:"path_prefix"`
	DefaultPageSize int    `yaml:"default_page_size"`
	MaxPageSize     int    `yaml:"max_page_size"`
}

// AccountingAuthorityRuleConfig mirrors the pure domain rule shape with
// YAML-friendly primitives.
type AccountingAuthorityRuleConfig struct {
	ID                   string                              `yaml:"id"`
	Kind                 string                              `yaml:"kind"`
	Mode                 string                              `yaml:"mode"`
	Unit                 string                              `yaml:"unit"`
	Limit                int64                               `yaml:"limit"`
	Currency             string                              `yaml:"currency"`
	AuthorityRequirement string                              `yaml:"authority_requirement"`
	FailureBehavior      string                              `yaml:"failure_behavior"`
	Perspective          string                              `yaml:"perspective"`
	LifecycleScope       string                              `yaml:"lifecycle_scope"`
	Basis                string                              `yaml:"basis"`
	Namespace            string                              `yaml:"namespace"`
	Version              string                              `yaml:"version"`
	Window               AccountingAuthorityWindowConfig     `yaml:"window"`
	Match                AccountingAuthorityDimensionsConfig `yaml:"match"`
}

// AccountingAuthorityWindowConfig defines a fixed window using config-native
// duration and timestamp strings.
type AccountingAuthorityWindowConfig struct {
	Algorithm string `yaml:"algorithm"`
	Size      string `yaml:"size"`
	Anchor    string `yaml:"anchor"`
}

// AccountingAuthorityDimensionMatcherConfig preserves unknown versus known-empty
// attribution semantics for one dimension.
type AccountingAuthorityDimensionMatcherConfig struct {
	Value        scope.Value `yaml:"value"`
	MatchUnknown bool        `yaml:"match_unknown"`
}

// AccountingAuthorityDimensionsConfig carries all safe scope dimensions the
// authority may match against.
type AccountingAuthorityDimensionsConfig struct {
	Principal    AccountingAuthorityDimensionMatcherConfig            `yaml:"principal"`
	Credential   AccountingAuthorityDimensionMatcherConfig            `yaml:"credential"`
	Tenant       AccountingAuthorityDimensionMatcherConfig            `yaml:"tenant"`
	Organization AccountingAuthorityDimensionMatcherConfig            `yaml:"organization"`
	Workspace    AccountingAuthorityDimensionMatcherConfig            `yaml:"workspace"`
	Project      AccountingAuthorityDimensionMatcherConfig            `yaml:"project"`
	Department   AccountingAuthorityDimensionMatcherConfig            `yaml:"department"`
	CostCenter   AccountingAuthorityDimensionMatcherConfig            `yaml:"cost_center"`
	Backend      AccountingAuthorityDimensionMatcherConfig            `yaml:"backend"`
	Model        AccountingAuthorityDimensionMatcherConfig            `yaml:"model"`
	Route        AccountingAuthorityDimensionMatcherConfig            `yaml:"route"`
	Labels       map[string]AccountingAuthorityDimensionMatcherConfig `yaml:"labels"`
}

// AuthorityQueryEffectivelyExposed reports whether the protected authority
// surface is configured to mount.
func AuthorityQueryEffectivelyExposed(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.Accounting.Authority.Enabled &&
		cfg.Accounting.Authority.Query.Enabled &&
		strings.TrimSpace(cfg.Accounting.Authority.Query.PathPrefix) != ""
}

// DomainConfig converts the validated config surface into the pure domain
// authority config consumed by the app layer.
func (a AccountingAuthorityConfig) DomainConfig() (authoritydomain.AuthorityConfig, error) {
	out := authoritydomain.AuthorityConfig{
		Enabled:            a.Enabled,
		Backing:            accountingAuthorityBacking(a.StartupPosture),
		UnknownAttribution: accountingAuthorityUnknownAttribution(a.UnknownAttribution),
	}
	if !a.Enabled {
		return out, nil
	}
	rules := make([]authoritydomain.Rule, 0, len(a.Rules))
	for _, cfgRule := range a.Rules {
		rule, err := cfgRule.DomainRule(a.Mode)
		if err != nil {
			return authoritydomain.AuthorityConfig{}, err
		}
		rules = append(rules, rule)
	}
	out.Rules = rules
	return out, nil
}

func (r AccountingAuthorityRuleConfig) DomainRule(defaultMode string) (authoritydomain.Rule, error) {
	mode := strings.ToLower(strings.TrimSpace(r.Mode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(defaultMode))
	}
	if mode == "" {
		mode = string(authoritydomain.RuleModeStrict)
	}
	window, err := r.Window.DomainWindow()
	if err != nil {
		return authoritydomain.Rule{}, err
	}
	return authoritydomain.Rule{
		ID:                   strings.TrimSpace(r.ID),
		Kind:                 authoritydomain.RuleKind(strings.ToLower(strings.TrimSpace(r.Kind))),
		Mode:                 authoritydomain.RuleMode(mode),
		Unit:                 authoritydomain.AmountUnit(strings.ToLower(strings.TrimSpace(r.Unit))),
		Limit:                authoritydomain.Amount{Unit: authoritydomain.AmountUnit(strings.ToLower(strings.TrimSpace(r.Unit))), Value: r.Limit},
		AuthorityRequirement: authoritydomain.AuthorityRequirement(strings.ToLower(strings.TrimSpace(r.AuthorityRequirement))),
		FailureBehavior:      authoritydomain.FailureBehavior(strings.ToLower(strings.TrimSpace(r.FailureBehavior))),
		Perspective:          metering.EconomicPerspective(strings.ToLower(strings.TrimSpace(r.Perspective))),
		LifecycleScope:       metering.LifecycleScope(strings.ToLower(strings.TrimSpace(r.LifecycleScope))),
		Basis:                authoritydomain.MeteringBasis(strings.ToLower(strings.TrimSpace(r.Basis))),
		Namespace:            strings.TrimSpace(r.Namespace),
		Version:              strings.TrimSpace(r.Version),
		Window:               window,
		Match:                r.Match.DomainMatcher(),
	}, nil
}

func (w AccountingAuthorityWindowConfig) DomainWindow() (authoritydomain.WindowSpec, error) {
	size := strings.TrimSpace(w.Size)
	anchor := strings.TrimSpace(w.Anchor)
	if size == "" && anchor == "" && strings.TrimSpace(w.Algorithm) == "" {
		return authoritydomain.WindowSpec{}, nil
	}
	out := authoritydomain.WindowSpec{
		Algorithm: authoritydomain.WindowAlgorithm(strings.ToLower(strings.TrimSpace(w.Algorithm))),
	}
	if out.Algorithm == "" {
		out.Algorithm = authoritydomain.WindowAlgorithmFixed
	}
	if size != "" {
		d, err := time.ParseDuration(size)
		if err != nil {
			return authoritydomain.WindowSpec{}, fmt.Errorf("accounting.authority.window.size: invalid duration %q", size)
		}
		if d <= 0 {
			return authoritydomain.WindowSpec{}, fmt.Errorf("accounting.authority.window.size: duration must be positive")
		}
		out.Size = d
	}
	if anchor != "" {
		t, err := time.Parse(time.RFC3339, anchor)
		if err != nil {
			return authoritydomain.WindowSpec{}, fmt.Errorf("accounting.authority.window.anchor: invalid timestamp %q", anchor)
		}
		out.Anchor = t.UTC()
	}
	return out, nil
}

func (m AccountingAuthorityDimensionsConfig) DomainMatcher() authoritydomain.DimensionsMatcher {
	out := authoritydomain.DimensionsMatcher{
		Principal:    m.Principal.DomainMatcher(),
		Credential:   m.Credential.DomainMatcher(),
		Tenant:       m.Tenant.DomainMatcher(),
		Organization: m.Organization.DomainMatcher(),
		Workspace:    m.Workspace.DomainMatcher(),
		Project:      m.Project.DomainMatcher(),
		Department:   m.Department.DomainMatcher(),
		CostCenter:   m.CostCenter.DomainMatcher(),
		Backend:      m.Backend.DomainMatcher(),
		Model:        m.Model.DomainMatcher(),
		Route:        m.Route.DomainMatcher(),
	}
	if len(m.Labels) > 0 {
		out.Labels = make(map[string]authoritydomain.DimensionMatcher, len(m.Labels))
		for key, matcher := range m.Labels {
			out.Labels[key] = matcher.DomainMatcher()
		}
	}
	return out
}

func (m AccountingAuthorityDimensionMatcherConfig) DomainMatcher() authoritydomain.DimensionMatcher {
	return authoritydomain.DimensionMatcher{
		Value:        m.Value,
		MatchUnknown: m.MatchUnknown,
	}
}

func accountingAuthorityUnknownAttribution(mode string) authoritydomain.UnknownAttribution {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "preserve":
		return authoritydomain.UnknownAttributionPreserve
	case "unknown":
		return authoritydomain.UnknownAttributionUnknown
	case "known_empty":
		return authoritydomain.UnknownAttributionKnownEmpty
	default:
		return authoritydomain.UnknownAttributionPreserve
	}
}

func accountingAuthorityBacking(posture string) authoritydomain.BackingCapability {
	switch strings.ToLower(strings.TrimSpace(posture)) {
	case "", "fail_closed":
		return authoritydomain.BackingCapabilityAtomic
	case "fail_open":
		return authoritydomain.BackingCapabilityAdvisoryOnly
	default:
		return authoritydomain.BackingCapabilityDisabled
	}
}

func validateAccountingAuthority(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	auth := &cfg.Accounting.Authority
	accountingCategoryRequired := false
	for _, category := range cfg.ControlPlane.RequiredCategories {
		if strings.EqualFold(strings.TrimSpace(category), "accounting_authority") {
			accountingCategoryRequired = true
			break
		}
	}
	if accountingCategoryRequired && !cfg.ControlPlane.Enabled {
		return fmt.Errorf("control_plane.enabled: must be true when accounting_authority is a required category")
	}
	if accountingCategoryRequired && strings.EqualFold(strings.TrimSpace(cfg.ControlPlane.RecordingPolicy), "required_pre_work") && !auth.Enabled {
		return fmt.Errorf("accounting.authority.enabled: must be true when accounting_authority is required under required_pre_work")
	}
	if !auth.Enabled {
		if auth.Query.Enabled {
			return fmt.Errorf("accounting.authority.enabled: must be true when accounting.authority.query.enabled is true")
		}
		return nil
	}
	store := strings.ToLower(strings.TrimSpace(auth.Store))
	if store == "" {
		store = "memory"
		auth.Store = store
	}
	switch store {
	case "memory", "sqlite", "postgres":
	default:
		return fmt.Errorf("accounting.authority.store: want memory, sqlite, or postgres, got %q", auth.Store)
	}
	if store == "sqlite" && strings.TrimSpace(auth.SQLitePath) == "" {
		return fmt.Errorf("accounting.authority.sqlite_path: required when store is \"sqlite\"")
	}
	if store == "postgres" && strings.TrimSpace(auth.PostgresDSN) == "" {
		return fmt.Errorf("accounting.authority.postgres_dsn: required when store is \"postgres\"")
	}
	if store != "postgres" && strings.TrimSpace(auth.PostgresDSN) != "" {
		return fmt.Errorf("accounting.authority.postgres_dsn: may only be set when store is \"postgres\" (got %q)", store)
	}
	mode := strings.ToLower(strings.TrimSpace(auth.Mode))
	if mode == "" {
		mode = "strict"
		auth.Mode = mode
	}
	switch mode {
	case "strict", "advisory":
	default:
		return fmt.Errorf("accounting.authority.mode: want strict or advisory, got %q", auth.Mode)
	}
	posture := strings.ToLower(strings.TrimSpace(auth.StartupPosture))
	if posture == "" {
		posture = "fail_closed"
		auth.StartupPosture = posture
	}
	switch posture {
	case "fail_open", "fail_closed":
	default:
		return fmt.Errorf("accounting.authority.startup_posture: want fail_open or fail_closed, got %q", auth.StartupPosture)
	}
	unknown := strings.ToLower(strings.TrimSpace(auth.UnknownAttribution))
	if unknown == "" {
		unknown = "preserve"
		auth.UnknownAttribution = unknown
	}
	switch unknown {
	case "preserve", "unknown", "known_empty":
	default:
		return fmt.Errorf("accounting.authority.unknown_attribution: want preserve, unknown, or known_empty, got %q", auth.UnknownAttribution)
	}
	evaluationTimeout, err := auth.EvaluationTimeoutDuration()
	if err != nil {
		return err
	}
	if strings.TrimSpace(auth.EvaluationTimeout) == "" {
		auth.EvaluationTimeout = evaluationTimeout.String()
	}
	cleanupTimeout, err := auth.CleanupTimeoutDuration()
	if err != nil {
		return err
	}
	if strings.TrimSpace(auth.CleanupTimeout) == "" {
		auth.CleanupTimeout = cleanupTimeout.String()
	}
	if auth.Query.Enabled {
		path := strings.TrimSpace(auth.Query.PathPrefix)
		if path == "" {
			return fmt.Errorf("accounting.authority.query.path_prefix: required when query is enabled")
		}
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("accounting.authority.query.path_prefix: must start with /")
		}
		// Admin is only mounted when enabled; a leftover disabled path must not
		// block an authority query prefix that would otherwise be free.
		if cfg.Accounting.Admin.Enabled && strings.TrimSpace(cfg.Accounting.Admin.Path) != "" {
			adminPath := strings.TrimSuffix(strings.TrimSpace(cfg.Accounting.Admin.Path), "/")
			queryPath := strings.TrimSuffix(path, "/")
			if queryPath == adminPath || strings.HasPrefix(queryPath, adminPath+"/") {
				return fmt.Errorf("accounting.authority.query.path_prefix: overlap with accounting.admin.path")
			}
		}
		if strings.TrimSpace(cfg.Diagnostics.HealthPath) != "" && strings.TrimSuffix(path, "/") == strings.TrimSuffix(strings.TrimSpace(cfg.Diagnostics.HealthPath), "/") {
			return fmt.Errorf("accounting.authority.query.path_prefix: overlap with diagnostics.health_path")
		}
		if ControlPlaneQueryEffectivelyExposed(cfg) {
			cpPath := strings.TrimSuffix(strings.TrimSpace(cfg.ControlPlane.Query.PathPrefix), "/")
			queryPath := strings.TrimSuffix(path, "/")
			if queryPath == cpPath || strings.HasPrefix(queryPath, cpPath+"/") || strings.HasPrefix(cpPath, queryPath+"/") {
				return fmt.Errorf("accounting.authority.query.path_prefix: overlap with control_plane.query.path_prefix")
			}
		}
		if auth.Query.DefaultPageSize == 0 {
			auth.Query.DefaultPageSize = 100
		}
		if auth.Query.MaxPageSize == 0 {
			auth.Query.MaxPageSize = 100
		}
		if auth.Query.DefaultPageSize <= 0 {
			return fmt.Errorf("accounting.authority.query.default_page_size: must be positive")
		}
		if auth.Query.MaxPageSize <= 0 {
			return fmt.Errorf("accounting.authority.query.max_page_size: must be positive")
		}
		if auth.Query.MaxPageSize < auth.Query.DefaultPageSize {
			return fmt.Errorf("accounting.authority.query.max_page_size: must be >= default_page_size (%d)", auth.Query.DefaultPageSize)
		}
		if auth.Query.MaxPageSize > 100 {
			return fmt.Errorf("accounting.authority.query.max_page_size: must not exceed 100")
		}
		if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
			return fmt.Errorf("accounting.authority.query.enabled: requires diagnostics.shared_secret")
		}
	}
	for i := range auth.Rules {
		rule := &auth.Rules[i]
		if retiredMonetaryAuthorityRule(*rule) {
			return fmt.Errorf("accounting.authority.rules[%d]: migration-required: monetary UsageAuthority rules are retired; use billing admission", i)
		}
		domainRule, err := rule.DomainRule(auth.Mode)
		if err != nil {
			return err
		}
		if err := domainRule.Validate(); err != nil {
			return fmt.Errorf("accounting.authority.rules[%d]: %w", i, err)
		}
		if mode == "advisory" && strings.ToLower(strings.TrimSpace(rule.Mode)) == "strict" {
			return fmt.Errorf("accounting.authority.rules[%d].mode: strict rules require accounting.authority.mode=strict", i)
		}
	}
	// Per-rule validation cannot see the backing capability derived from
	// startup_posture, so delegate the AuthorityConfig-level invariants (e.g.
	// strict rules require atomic backing) to the domain Validate().
	domainCfg, err := auth.DomainConfig()
	if err != nil {
		return fmt.Errorf("accounting.authority: %w", err)
	}
	if err := domainCfg.Validate(); err != nil {
		return fmt.Errorf("accounting.authority: %w", err)
	}
	return nil
}

func retiredMonetaryAuthorityRule(rule AccountingAuthorityRuleConfig) bool {
	// Keep the legacy spellings explicit: budget, spend_cap, and money_nano
	// must fail migration-required rather than being reinterpreted as quota.
	normalize := func(value string) string {
		return strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	}
	kind := normalize(rule.Kind)
	unit := normalize(rule.Unit)
	return kind == "budget" || kind == "spendcap" || unit == "moneynano"
}
