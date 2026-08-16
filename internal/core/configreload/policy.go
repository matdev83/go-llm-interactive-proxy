package configreload

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/identity"
	"gopkg.in/yaml.v3"
)

// ChangeDisposition is the Classify outcome for one safe field path.
type ChangeDisposition string

const (
	ChangeReloadable      ChangeDisposition = "reloadable"
	ChangeRestartRequired ChangeDisposition = "restart_required"
)

// SafeChange is a value-free description of a classified config difference.
type SafeChange struct {
	Path        string
	Disposition ChangeDisposition
}

// MaxRestartRequiredFields bounds restart_required_fields (requirement 7.6).
const MaxRestartRequiredFields = 32

// RestartRequiredError rejects a candidate before generation construction when
// any restart-required path is present (requirements 3.5, 7.5–7.6).
type RestartRequiredError struct {
	RestartRequiredFields []string
	TotalBlocked          int
}

func (e *RestartRequiredError) Error() string {
	if e == nil {
		return "restart_required"
	}
	if len(e.RestartRequiredFields) == 0 {
		return fmt.Sprintf("restart_required: %d blocked change(s)", e.TotalBlocked)
	}
	return fmt.Sprintf("restart_required: %d blocked change(s); fields=%s",
		e.TotalBlocked, strings.Join(e.RestartRequiredFields, ","))
}

type noteFn func(path string)

// Classify compares active and candidate configs using maintained typed
// section comparators (requirement 7.2). Mixed reloadable and restart-required
// diffs reject as one transaction (requirement 3.5).
func Classify(active, candidate *config.Config) ([]SafeChange, error) {
	if active == nil || candidate == nil {
		return nil, fmt.Errorf("configreload: active and candidate configs are required")
	}
	var reloadable []SafeChange
	var blocked []string
	reload := func(path string) {
		reloadable = append(reloadable, SafeChange{Path: path, Disposition: ChangeReloadable})
	}
	restart := func(path string) { blocked = append(blocked, path) }

	classifyAccess(active, candidate, restart)
	classifyLogging(active, candidate, restart)
	classifyDiagnostics(active, candidate, restart)
	classifyObservability(active, candidate, restart)
	classifyDatabase(active, candidate, restart)
	classifyContinuity(active, candidate, restart)
	classifyHTTPClient(active, candidate, reload)
	classifyHTTPHeaders(active, candidate, reload)
	classifyStreamRecovery(active, candidate, reload)
	classifyHooks(active, candidate, reload)
	classifyInterleaved(active, candidate, reload)
	classifyModelAliases(active, candidate, reload)
	classifyIdentity(active, candidate, reload)
	classifyServer(active, candidate, reload, restart)
	classifyAuth(active, candidate, reload, restart)
	classifyRouting(active, candidate, reload, restart)
	classifySecureSession(active, candidate, reload, restart)
	classifyAccounting(active, candidate, reload, restart)
	classifyControlPlane(active, candidate, reload, restart)
	classifyMetering(active, candidate, reload, restart)
	classifyPlugins(active, candidate, reload)
	classifyModelCatalog(active, candidate, reload, restart)
	classifyModelInventory(active, candidate, reload, restart)

	if len(blocked) > 0 {
		slices.Sort(blocked)
		blocked = slices.Compact(blocked)
		total := len(blocked)
		fields := blocked
		if len(fields) > MaxRestartRequiredFields {
			fields = fields[:MaxRestartRequiredFields]
		}
		return nil, &RestartRequiredError{
			RestartRequiredFields: slices.Clone(fields),
			TotalBlocked:          total,
		}
	}
	if len(reloadable) == 0 {
		return nil, nil
	}
	slices.SortFunc(reloadable, func(a, b SafeChange) int {
		return strings.Compare(a.Path, b.Path)
	})
	return reloadable, nil
}

// ClassifyEffective classifies two effective configs for coordinator use.
func ClassifyEffective(active, candidate *config.EffectiveConfig) ([]SafeChange, error) {
	if active == nil || candidate == nil {
		return nil, fmt.Errorf("configreload: active and candidate effective configs are required")
	}
	return Classify(active.Config, candidate.Config)
}

func classifyAccess(active, candidate *config.Config, restart noteFn) {
	if active.Access.Mode != candidate.Access.Mode {
		restart("access")
	}
}

func classifyLogging(active, candidate *config.Config, restart noteFn) {
	a, c := active.Logging, candidate.Logging
	if a.Level != c.Level {
		restart("logging.level")
	}
	if a.Format != c.Format {
		restart("logging.format")
	}
	if a.AddSource != c.AddSource {
		restart("logging.add_source")
	}
	if a.AccessLog != c.AccessLog {
		restart("logging.access_log")
	}
	if !slices.Equal(a.AccessLogSkipPaths, c.AccessLogSkipPaths) {
		restart("logging.access_log_skip_paths")
	}
	if a.AccessLogIncludeRawPath != c.AccessLogIncludeRawPath {
		restart("logging.access_log_include_raw_path")
	}
}

func classifyDiagnostics(active, candidate *config.Config, restart noteFn) {
	a, c := active.Diagnostics, candidate.Diagnostics
	if a.Enabled != c.Enabled || a.HealthPath != c.HealthPath || a.AttemptsPath != c.AttemptsPath ||
		a.InventoryPath != c.InventoryPath || a.RouteTracePath != c.RouteTracePath ||
		a.PprofPath != c.PprofPath || a.SharedSecret != c.SharedSecret {
		restart("diagnostics")
	}
}

func classifyObservability(active, candidate *config.Config, restart noteFn) {
	a, c := active.Observability, candidate.Observability
	if a.Metrics.Enabled != c.Metrics.Enabled || a.Metrics.Path != c.Metrics.Path ||
		a.Metrics.ExemplarsEnabled != c.Metrics.ExemplarsEnabled ||
		a.Tracing.Enabled != c.Tracing.Enabled || a.Tracing.ServiceName != c.Tracing.ServiceName ||
		!ptrFloat64Equal(a.Tracing.SampleRatio, c.Tracing.SampleRatio) {
		restart("observability")
	}
}

func classifyDatabase(active, candidate *config.Config, restart noteFn) {
	a, c := active.Database, candidate.Database
	if a.ConnectionMode != c.ConnectionMode || a.SchemaMode != c.SchemaMode ||
		a.MaxOpenConns != c.MaxOpenConns || a.MaxIdleConns != c.MaxIdleConns ||
		a.ConnMaxLifetime != c.ConnMaxLifetime || a.ConnMaxIdleTime != c.ConnMaxIdleTime {
		restart("database")
	}
}

func classifyContinuity(active, candidate *config.Config, restart noteFn) {
	a, c := active.Continuity, candidate.Continuity
	if a.InMemory != c.InMemory || a.Store != c.Store || a.SQLitePath != c.SQLitePath ||
		a.PostgresDSN != c.PostgresDSN || a.TTL != c.TTL || a.MaxLegs != c.MaxLegs {
		restart("continuity")
	}
}

func classifyHTTPClient(active, candidate *config.Config, reload noteFn) {
	a, c := active.HTTPClient, candidate.HTTPClient
	if !ptrBoolEqual(a.TrustEnvironmentProxy, c.TrustEnvironmentProxy) {
		reload("http_client.trust_environment_proxy")
	}
	if !ptrIntEqual(a.MaxIdleConns, c.MaxIdleConns) {
		reload("http_client.max_idle_conns")
	}
	if !ptrIntEqual(a.MaxIdleConnsPerHost, c.MaxIdleConnsPerHost) {
		reload("http_client.max_idle_conns_per_host")
	}
	diffStr(reload, "http_client.idle_conn_timeout", a.IdleConnTimeout, c.IdleConnTimeout)
	diffStr(reload, "http_client.response_header_timeout", a.ResponseHeaderTimeout, c.ResponseHeaderTimeout)
	diffStr(reload, "http_client.dial_timeout", a.DialTimeout, c.DialTimeout)
	diffStr(reload, "http_client.keep_alive", a.KeepAlive, c.KeepAlive)
	diffStr(reload, "http_client.tls_handshake_timeout", a.TLSHandshakeTimeout, c.TLSHandshakeTimeout)
	diffStr(reload, "http_client.expect_continue_timeout", a.ExpectContinueTimeout, c.ExpectContinueTimeout)
	diffStr(reload, "http_client.client_timeout", a.ClientTimeout, c.ClientTimeout)
}

func classifyHTTPHeaders(active, candidate *config.Config, reload noteFn) {
	a, c := active.HTTPHeaders, candidate.HTTPHeaders
	diffStrSlice(reload, "http_headers.api_key", a.APIKey, c.APIKey)
	diffStrSlice(reload, "http_headers.route", a.Route, c.Route)
	diffStrSlice(reload, "http_headers.session_id", a.SessionID, c.SessionID)
	diffStrSlice(reload, "http_headers.resume_token", a.ResumeToken, c.ResumeToken)
	diffStrSlice(reload, "http_headers.a_leg_id", a.ALegID, c.ALegID)
	diffStrSlice(reload, "http_headers.session_hint", a.SessionHint, c.SessionHint)
	diffStrSlice(reload, "http_headers.trace", a.Trace, c.Trace)
	diffStrSlice(reload, "http_headers.diagnostics_secret", a.DiagnosticsSecret, c.DiagnosticsSecret)
}

func classifyStreamRecovery(active, candidate *config.Config, reload noteFn) {
	a, c := active.StreamRecovery.AutoResume, candidate.StreamRecovery.AutoResume
	if !ptrBoolEqual(a.Enabled, c.Enabled) {
		reload("stream_recovery.auto_resume.enabled")
	}
	diffStr(reload, "stream_recovery.auto_resume.idle_timeout", a.IdleTimeout, c.IdleTimeout)
	diffStr(reload, "stream_recovery.auto_resume.grace_period", a.GracePeriod, c.GracePeriod)
	diffStr(reload, "stream_recovery.auto_resume.post_output_policy", a.PostOutputPolicy, c.PostOutputPolicy)
	if !ptrBoolEqual(a.EmitWarning, c.EmitWarning) {
		reload("stream_recovery.auto_resume.emit_warning")
	}
	diffStr(reload, "stream_recovery.auto_resume.keepalive_interval", a.KeepaliveInterval, c.KeepaliveInterval)
}

func classifyHooks(active, candidate *config.Config, reload noteFn) {
	diffStr(reload, "hooks.tool_reactor_error_policy",
		active.Hooks.ToolReactorErrorPolicy, candidate.Hooks.ToolReactorErrorPolicy)
}

func classifyInterleaved(active, candidate *config.Config, reload noteFn) {
	a, c := active.Interleaved, candidate.Interleaved
	if a.Enabled != c.Enabled {
		reload("interleaved.enabled")
	}
	diffStr(reload, "interleaved.instructions_file", a.InstructionsFile, c.InstructionsFile)
	diffStr(reload, "interleaved.stream_to_client", a.StreamToClient, c.StreamToClient)
	if a.RegularTurnsRemaining != c.RegularTurnsRemaining {
		reload("interleaved.regular_turns_remaining")
	}
	if a.MaxMemoBytes != c.MaxMemoBytes {
		reload("interleaved.max_memo_bytes")
	}
}

func classifyModelAliases(active, candidate *config.Config, reload noteFn) {
	if !equalModelAliases(active.ModelAliases, candidate.ModelAliases) {
		reload("model_aliases")
	}
}

func classifyIdentity(active, candidate *config.Config, reload noteFn) {
	if !equalIdentity(active.Identity, candidate.Identity) {
		reload("identity")
	}
}

func classifyServer(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Server, candidate.Server
	diffStr(restart, "server.address", string(a.Address), string(c.Address))
	diffStr(restart, "server.auth_mode", string(a.AuthMode), string(c.AuthMode))
	diffStr(restart, "server.read_header_timeout", a.ReadHeaderTimeout, c.ReadHeaderTimeout)
	diffStr(restart, "server.read_timeout", a.ReadTimeout, c.ReadTimeout)
	diffStr(restart, "server.write_timeout", a.WriteTimeout, c.WriteTimeout)
	diffStr(restart, "server.idle_timeout", a.IdleTimeout, c.IdleTimeout)
	diffStr(restart, "server.shutdown_timeout", a.ShutdownTimeout, c.ShutdownTimeout)
	if a.MaxConcurrentDecodes != c.MaxConcurrentDecodes {
		restart("server.max_concurrent_decodes")
	}
	if a.MaxInflightDecodeBytes != c.MaxInflightDecodeBytes {
		restart("server.max_inflight_decode_bytes")
	}
	if a.MaxRequestBodyBytes != c.MaxRequestBodyBytes {
		reload("server.max_request_body_bytes")
	}
	if a.MaxPendingWireEvents != c.MaxPendingWireEvents {
		reload("server.max_pending_wire_events")
	}
	if a.PreRequestKeepalive.Enabled != c.PreRequestKeepalive.Enabled ||
		a.PreRequestKeepalive.Interval != c.PreRequestKeepalive.Interval {
		reload("server.pre_request_keepalive")
	}
}

func classifyAuth(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Auth, candidate.Auth
	diffStr(restart, "auth.handler", a.Handler, c.Handler)
	diffStr(restart, "auth.required_level", a.RequiredLevel, c.RequiredLevel)
	diffStr(restart, "auth.event_failure_policy", a.EventFailurePolicy, c.EventFailurePolicy)
	diffStr(restart, "auth.event_delivery", a.EventDelivery, c.EventDelivery)
	if a.Remote.Endpoint != c.Remote.Endpoint || a.Remote.Handler != c.Remote.Handler {
		restart("auth.remote")
	}
	if !equalLocalAPIKeys(a.LocalAPIKeys, c.LocalAPIKeys) {
		reload("auth.local_api_keys")
	}
}

func classifyRouting(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Routing, candidate.Routing
	diffStr(restart, "routing.affinity.store", a.Affinity.Store, c.Affinity.Store)
	if a.MaxAttempts != c.MaxAttempts {
		reload("routing.max_attempts")
	}
	diffStr(reload, "routing.default_route", a.DefaultRoute, c.DefaultRoute)
	if a.Health.CircuitBreaker != c.Health.CircuitBreaker {
		reload("routing.health")
	}
	diffStr(reload, "routing.affinity.missing_identity", a.Affinity.MissingIdentity, c.Affinity.MissingIdentity)
	diffStr(reload, "routing.transport.fallback_policy", a.Transport.FallbackPolicy, c.Transport.FallbackPolicy)
	if a.OverrideAdmin.Enabled != c.OverrideAdmin.Enabled {
		reload("routing.override_admin.enabled")
	}
	diffStr(reload, "routing.override_admin.path_prefix", a.OverrideAdmin.PathPrefix, c.OverrideAdmin.PathPrefix)
	if a.OverrideAdmin.MaxBodyBytes != c.OverrideAdmin.MaxBodyBytes {
		reload("routing.override_admin.max_body_bytes")
	}
}

func classifySecureSession(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.SecureSession, candidate.SecureSession
	if !ptrBoolEqual(a.Enabled, c.Enabled) {
		restart("secure_session.enabled")
	}
	diffStr(restart, "secure_session.store", a.Store, c.Store)
	diffStr(restart, "secure_session.sqlite_path", a.SQLitePath, c.SQLitePath)
	diffStr(restart, "secure_session.postgres_dsn", a.PostgresDSN, c.PostgresDSN)
	diffStr(restart, "secure_session.token_fingerprint_key", a.TokenFingerprintKey, c.TokenFingerprintKey)
	diffStr(restart, "secure_session.audit_durability", a.AuditDurability, c.AuditDurability)
	if a.SQLQueryCacheTTL != c.SQLQueryCacheTTL || a.SQLQueryCacheMaxEntries != c.SQLQueryCacheMaxEntries {
		restart("secure_session.sql_query_cache")
	}
	diffStr(reload, "secure_session.resume_window", a.ResumeWindow, c.ResumeWindow)
	diffStr(reload, "secure_session.redaction_default", a.RedactionDefault, c.RedactionDefault)
	if a.DiagnosticsExposeSummaries != c.DiagnosticsExposeSummaries {
		reload("secure_session.diagnostics_expose_summaries")
	}
	diffStr(reload, "secure_session.diagnostics_path_prefix", a.DiagnosticsPathPrefix, c.DiagnosticsPathPrefix)
	diffStr(reload, "secure_session.non_durable_warning", a.NonDurableWarning, c.NonDurableWarning)
	if a.RequireWorkspaceID != c.RequireWorkspaceID {
		reload("secure_session.require_workspace_id")
	}
	diffStr(reload, "secure_session.workspace_resolve_on_error", a.WorkspaceResolveOnError, c.WorkspaceResolveOnError)
	if a.ResumeTokenBindPrincipalOnly != c.ResumeTokenBindPrincipalOnly {
		reload("secure_session.resume_token_bind_principal_only")
	}
}

func classifyAccounting(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Accounting, candidate.Accounting
	if a.Ledger != c.Ledger {
		restart("accounting.ledger")
	}
	if !equalAccountingAuthority(a.Authority, c.Authority) {
		restart("accounting.authority")
	}
	if !equalConcurrencyAuthority(a.Concurrency, c.Concurrency) {
		restart("accounting.concurrency")
	}
	if a.Enabled != c.Enabled {
		reload("accounting.enabled")
	}
	diffStr(reload, "accounting.mode", a.Mode, c.Mode)
	diffStr(reload, "accounting.count_timeout", a.CountTimeout, c.CountTimeout)
	if a.Tokenizer.DefaultEncoding != c.Tokenizer.DefaultEncoding || !maps.Equal(a.Tokenizer.ModelMappings, c.Tokenizer.ModelMappings) {
		reload("accounting.tokenizer")
	}
	if a.Preflight != c.Preflight {
		reload("accounting.preflight")
	}
	if a.Admin != c.Admin {
		reload("accounting.admin")
	}
	if a.Observability.Enabled != c.Observability.Enabled {
		reload("accounting.observability")
	}
	if a.StrictAuthoritative != c.StrictAuthoritative {
		reload("accounting.strict_authoritative")
	}
	if !equalAccountingPricing(a.Pricing, c.Pricing) {
		reload("accounting.pricing")
	}
	if a.Billing.Authoritative != c.Billing.Authoritative || a.Billing.ReportsPath != c.Billing.ReportsPath {
		restart("accounting.billing")
	}
}

func classifyControlPlane(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.ControlPlane, candidate.ControlPlane
	if a.Store != c.Store || a.SQLitePath != c.SQLitePath || a.PostgresDSN != c.PostgresDSN {
		restart("control_plane.store")
	}
	if a.Enabled != c.Enabled {
		reload("control_plane.enabled")
	}
	diffStr(reload, "control_plane.recording_policy", a.RecordingPolicy, c.RecordingPolicy)
	if !slices.Equal(a.RequiredCategories, c.RequiredCategories) {
		reload("control_plane.required_categories")
	}
	if a.Query != c.Query {
		reload("control_plane.query")
	}
	if a.Retention != c.Retention {
		reload("control_plane.retention")
	}
	diffStr(reload, "control_plane.redaction_default", a.RedactionDefault, c.RedactionDefault)
}

func classifyMetering(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Metering, candidate.Metering
	if a.Journal != c.Journal {
		restart("metering.journal")
	}
	if a.Enabled != c.Enabled {
		reload("metering.enabled")
	}
}

func classifyPlugins(active, candidate *config.Config, reload noteFn) {
	if !equalPluginRows(active.Plugins.Frontends, candidate.Plugins.Frontends) {
		reload("plugins.frontends")
	}
	if !equalPluginRows(active.Plugins.Backends, candidate.Plugins.Backends) {
		reload("plugins.backends")
	}
	if !equalPluginRows(active.Plugins.Features, candidate.Plugins.Features) {
		reload("plugins.features")
	}
}

func classifyModelCatalog(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.ModelCatalog, candidate.ModelCatalog
	diffStr(restart, "model_catalog.cache_path", a.CachePath, c.CachePath)
	if a.ExternalUpdatesEnabled != c.ExternalUpdatesEnabled {
		restart("model_catalog.external_updates_enabled")
	}
	diffStr(restart, "model_catalog.source_url", a.SourceURL, c.SourceURL)
	diffStr(restart, "model_catalog.update_interval", a.UpdateInterval, c.UpdateInterval)
	if a.Enabled != c.Enabled {
		reload("model_catalog.enabled")
	}
	diffStr(reload, "model_catalog.fetch_timeout", a.FetchTimeout, c.FetchTimeout)
	diffStr(reload, "model_catalog.diagnostics_path", a.DiagnosticsPath, c.DiagnosticsPath)
	if !equalModelCatalogOverrides(a.ModelOverrides, c.ModelOverrides) {
		reload("model_catalog.model_overrides")
	}
	if !equalModelCatalogBackendOverrides(a.BackendModelOverrides, c.BackendModelOverrides) {
		reload("model_catalog.backend_model_overrides")
	}
}

func classifyModelInventory(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.ModelInventory, candidate.ModelInventory
	diffStr(restart, "model_inventory.cache_path", a.CachePath, c.CachePath)
	if !ptrBoolEqual(a.RefreshEnabled, c.RefreshEnabled) {
		restart("model_inventory.refresh_enabled")
	}
	diffStr(restart, "model_inventory.refresh_interval", a.RefreshInterval, c.RefreshInterval)
	diffStr(reload, "model_inventory.fetch_timeout", a.FetchTimeout, c.FetchTimeout)
	diffStr(reload, "model_inventory.diagnostics_path", a.DiagnosticsPath, c.DiagnosticsPath)
}

func diffStr(note noteFn, path, left, right string) {
	if left != right {
		note(path)
	}
}

func diffStrSlice(note noteFn, path string, left, right []string) {
	if !slices.Equal(left, right) {
		note(path)
	}
}

func ptrBoolEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrIntEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrInt64Equal(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func ptrFloat64Equal(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func equalModelAliases(a, b []config.ModelAliasConfig) bool {
	return slices.Equal(a, b)
}

func equalIdentity(a, b identity.Config) bool {
	return a.Upstream.UserAgent == b.Upstream.UserAgent &&
		a.Upstream.OpenRouter == b.Upstream.OpenRouter &&
		a.Downstream.Server == b.Downstream.Server
}

func equalLocalAPIKeys(a, b []config.AuthLocalAPIKeyRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].KeyID != b[i].KeyID || a[i].PrincipalID != b[i].PrincipalID || a[i].Key != b[i].Key {
			return false
		}
		at, bt := a[i].Attribution, b[i].Attribution
		if at.DisplayName != bt.DisplayName || at.AuthMethod != bt.AuthMethod ||
			at.TenantID != bt.TenantID || at.OrganizationID != bt.OrganizationID ||
			at.WorkspaceID != bt.WorkspaceID || at.ProjectID != bt.ProjectID ||
			at.DepartmentID != bt.DepartmentID || at.CostCenterID != bt.CostCenterID ||
			!slices.Equal(at.Roles, bt.Roles) || !maps.Equal(at.SafeClaims, bt.SafeClaims) ||
			!maps.Equal(at.PolicyLabels, bt.PolicyLabels) {
			return false
		}
	}
	return true
}

func equalAccountingPricing(a, b config.AccountingPricingConfig) bool {
	return a.Currency == b.Currency && a.CatalogVersion == b.CatalogVersion && slices.Equal(a.Models, b.Models)
}

func equalAccountingAuthority(a, b config.AccountingAuthorityConfig) bool {
	if a.Enabled != b.Enabled || a.Mode != b.Mode || a.Store != b.Store ||
		a.SQLitePath != b.SQLitePath || a.PostgresDSN != b.PostgresDSN ||
		a.StartupPosture != b.StartupPosture || a.UnknownAttribution != b.UnknownAttribution ||
		a.EvaluationTimeout != b.EvaluationTimeout || a.CleanupTimeout != b.CleanupTimeout ||
		a.SnapshotVersion != b.SnapshotVersion || a.Query != b.Query || len(a.Rules) != len(b.Rules) {
		return false
	}
	for i := range a.Rules {
		if !equalAuthorityRule(a.Rules[i], b.Rules[i]) {
			return false
		}
	}
	return true
}

func equalAuthorityRule(a, b config.AccountingAuthorityRuleConfig) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.Mode == b.Mode && a.Unit == b.Unit &&
		a.Limit == b.Limit && a.Currency == b.Currency &&
		a.AuthorityRequirement == b.AuthorityRequirement && a.FailureBehavior == b.FailureBehavior &&
		a.Perspective == b.Perspective && a.LifecycleScope == b.LifecycleScope && a.Basis == b.Basis &&
		a.Namespace == b.Namespace && a.Version == b.Version && a.Window == b.Window &&
		equalAuthorityDimensions(a.Match, b.Match)
}

func equalAuthorityDimensions(a, b config.AccountingAuthorityDimensionsConfig) bool {
	return equalAuthorityMatcher(a.Principal, b.Principal) &&
		equalAuthorityMatcher(a.Credential, b.Credential) &&
		equalAuthorityMatcher(a.Tenant, b.Tenant) &&
		equalAuthorityMatcher(a.Organization, b.Organization) &&
		equalAuthorityMatcher(a.Workspace, b.Workspace) &&
		equalAuthorityMatcher(a.Project, b.Project) &&
		equalAuthorityMatcher(a.Department, b.Department) &&
		equalAuthorityMatcher(a.CostCenter, b.CostCenter) &&
		equalAuthorityMatcher(a.Backend, b.Backend) &&
		equalAuthorityMatcher(a.Model, b.Model) &&
		equalAuthorityMatcher(a.Route, b.Route) &&
		equalAuthorityLabelMatchers(a.Labels, b.Labels)
}

func equalAuthorityMatcher(a, b config.AccountingAuthorityDimensionMatcherConfig) bool {
	return a.MatchUnknown == b.MatchUnknown && a.Value.Equal(b.Value)
}

func equalAuthorityLabelMatchers(a, b map[string]config.AccountingAuthorityDimensionMatcherConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !equalAuthorityMatcher(av, bv) {
			return false
		}
	}
	return true
}

func equalConcurrencyAuthority(a, b config.ConcurrencyAuthorityConfig) bool {
	if a.Enabled != b.Enabled || a.Store != b.Store || a.StoreID != b.StoreID ||
		a.SQLitePath != b.SQLitePath || a.PostgresDSN != b.PostgresDSN ||
		a.LeaseTTL != b.LeaseTTL || a.RenewBefore != b.RenewBefore ||
		a.SnapshotVersion != b.SnapshotVersion || a.AuxiliaryLeasePolicy != b.AuxiliaryLeasePolicy ||
		len(a.Rules) != len(b.Rules) {
		return false
	}
	for i := range a.Rules {
		ar, br := a.Rules[i], b.Rules[i]
		if ar.ID != br.ID || ar.Mode != br.Mode || ar.MaxActiveRequests != br.MaxActiveRequests ||
			ar.LeaseTTL != br.LeaseTTL || ar.RenewBefore != br.RenewBefore ||
			ar.FailureBehavior != br.FailureBehavior || ar.Namespace != br.Namespace ||
			ar.Version != br.Version || !equalAuthorityDimensions(ar.Match, br.Match) {
			return false
		}
	}
	return true
}

func equalPluginRows(a, b []config.PluginConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind || a[i].ID != b[i].ID || a[i].Enabled != b[i].Enabled ||
			!opaqueNodeEqual(&a[i].Config, &b[i].Config) {
			return false
		}
	}
	return true
}

// opaqueNodeEqual compares plugin-private yaml.Node trees by typed node fields.
func opaqueNodeEqual(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value {
		return false
	}
	if (a.Alias == nil) != (b.Alias == nil) {
		return false
	}
	if a.Alias != nil && !opaqueNodeEqual(a.Alias, b.Alias) {
		return false
	}
	if len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !opaqueNodeEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

func equalModelCatalogOverrides(a, b []config.ModelCatalogModelOverrideEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Model != b[i].Model ||
			!ptrBoolEqual(a[i].Tools, b[i].Tools) ||
			!ptrBoolEqual(a[i].StructuredOutputs, b[i].StructuredOutputs) ||
			!ptrBoolEqual(a[i].Reasoning, b[i].Reasoning) ||
			!ptrBoolEqual(a[i].Vision, b[i].Vision) ||
			!ptrBoolEqual(a[i].Documents, b[i].Documents) ||
			!ptrInt64Equal(a[i].ContextLimitTokens, b[i].ContextLimitTokens) ||
			!ptrInt64Equal(a[i].InputLimitTokens, b[i].InputLimitTokens) ||
			!ptrInt64Equal(a[i].OutputLimitTokens, b[i].OutputLimitTokens) {
			return false
		}
	}
	return true
}

func equalModelCatalogBackendOverrides(a, b []config.ModelCatalogBackendModelOverrideEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Backend != b[i].Backend || a[i].Model != b[i].Model ||
			!ptrBoolEqual(a[i].Tools, b[i].Tools) ||
			!ptrBoolEqual(a[i].StructuredOutputs, b[i].StructuredOutputs) ||
			!ptrBoolEqual(a[i].Reasoning, b[i].Reasoning) ||
			!ptrBoolEqual(a[i].Vision, b[i].Vision) ||
			!ptrBoolEqual(a[i].Documents, b[i].Documents) ||
			!ptrInt64Equal(a[i].ContextLimitTokens, b[i].ContextLimitTokens) ||
			!ptrInt64Equal(a[i].InputLimitTokens, b[i].InputLimitTokens) ||
			!ptrInt64Equal(a[i].OutputLimitTokens, b[i].OutputLimitTokens) {
			return false
		}
	}
	return true
}
