package configreload

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/config"
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

// Classify compares active and candidate configs. Mixed reloadable and
// restart-required diffs reject as one transaction (requirement 3.5).
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

	diffYAML(restart, "access", active.Access, candidate.Access)
	diffYAML(restart, "logging", active.Logging, candidate.Logging)
	diffYAML(restart, "diagnostics", active.Diagnostics, candidate.Diagnostics)
	diffYAML(restart, "observability", active.Observability, candidate.Observability)
	diffYAML(restart, "database", active.Database, candidate.Database)
	diffYAML(restart, "continuity", active.Continuity, candidate.Continuity)

	diffYAML(reload, "http_client", active.HTTPClient, candidate.HTTPClient)
	diffYAML(reload, "stream_recovery", active.StreamRecovery, candidate.StreamRecovery)
	diffYAML(reload, "hooks", active.Hooks, candidate.Hooks)
	diffYAML(reload, "interleaved", active.Interleaved, candidate.Interleaved)
	diffYAML(reload, "model_aliases", active.ModelAliases, candidate.ModelAliases)
	diffYAML(reload, "identity", active.Identity, candidate.Identity)

	classifyServer(active, candidate, reload, restart)
	classifyAuth(active, candidate, reload, restart)
	classifyRouting(active, candidate, reload, restart)
	classifySecureSession(active, candidate, reload, restart)
	classifyAccounting(active, candidate, reload, restart)
	classifyControlPlane(active, candidate, reload, restart)
	classifyMetering(active, candidate, reload, restart)
	diffYAML(reload, "plugins", active.Plugins, candidate.Plugins)
	classifyModelCatalog(active, candidate, reload, restart)
	classifyModelInventory(active, candidate, reload, restart)
	classifyCodexModelCatalog(active, candidate, reload, restart)

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

func classifyServer(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Server, candidate.Server
	diffStr(restart, "server.address", string(a.Address), string(c.Address))
	diffStr(restart, "server.auth_mode", string(a.AuthMode), string(c.AuthMode))
	diffStr(restart, "server.read_header_timeout", a.ReadHeaderTimeout, c.ReadHeaderTimeout)
	diffStr(restart, "server.read_timeout", a.ReadTimeout, c.ReadTimeout)
	diffStr(restart, "server.write_timeout", a.WriteTimeout, c.WriteTimeout)
	diffStr(restart, "server.idle_timeout", a.IdleTimeout, c.IdleTimeout)
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
	diffYAML(reload, "server.pre_request_keepalive", a.PreRequestKeepalive, c.PreRequestKeepalive)
}

func classifyAuth(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Auth, candidate.Auth
	diffStr(restart, "auth.handler", a.Handler, c.Handler)
	diffStr(restart, "auth.required_level", a.RequiredLevel, c.RequiredLevel)
	diffStr(restart, "auth.event_failure_policy", a.EventFailurePolicy, c.EventFailurePolicy)
	diffStr(restart, "auth.event_delivery", a.EventDelivery, c.EventDelivery)
	diffYAML(restart, "auth.remote", a.Remote, c.Remote)
	diffYAML(reload, "auth.local_api_keys", a.LocalAPIKeys, c.LocalAPIKeys)
}

func classifyRouting(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Routing, candidate.Routing
	diffStr(restart, "routing.affinity.store", a.Affinity.Store, c.Affinity.Store)
	if a.MaxAttempts != c.MaxAttempts {
		reload("routing.max_attempts")
	}
	diffStr(reload, "routing.default_route", a.DefaultRoute, c.DefaultRoute)
	diffYAML(reload, "routing.health", a.Health, c.Health)
	diffStr(reload, "routing.affinity.missing_identity", a.Affinity.MissingIdentity, c.Affinity.MissingIdentity)
	diffYAML(reload, "routing.transport", a.Transport, c.Transport)
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
	diffYAML(restart, "accounting.ledger", a.Ledger, c.Ledger)
	diffYAML(restart, "accounting.authority", a.Authority, c.Authority)
	diffYAML(restart, "accounting.concurrency", a.Concurrency, c.Concurrency)
	if a.Enabled != c.Enabled {
		reload("accounting.enabled")
	}
	diffStr(reload, "accounting.mode", a.Mode, c.Mode)
	diffStr(reload, "accounting.count_timeout", a.CountTimeout, c.CountTimeout)
	diffYAML(reload, "accounting.tokenizer", a.Tokenizer, c.Tokenizer)
	diffYAML(reload, "accounting.preflight", a.Preflight, c.Preflight)
	diffYAML(reload, "accounting.admin", a.Admin, c.Admin)
	diffYAML(reload, "accounting.observability", a.Observability, c.Observability)
	if a.StrictAuthoritative != c.StrictAuthoritative {
		reload("accounting.strict_authoritative")
	}
	diffYAML(reload, "accounting.pricing", a.Pricing, c.Pricing)
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
	diffYAML(reload, "control_plane.required_categories", a.RequiredCategories, c.RequiredCategories)
	diffYAML(reload, "control_plane.query", a.Query, c.Query)
	diffYAML(reload, "control_plane.retention", a.Retention, c.Retention)
	diffStr(reload, "control_plane.redaction_default", a.RedactionDefault, c.RedactionDefault)
}

func classifyMetering(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.Metering, candidate.Metering
	diffYAML(restart, "metering.journal", a.Journal, c.Journal)
	if a.Enabled != c.Enabled {
		reload("metering.enabled")
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
	diffYAML(reload, "model_catalog.model_overrides", a.ModelOverrides, c.ModelOverrides)
	diffYAML(reload, "model_catalog.backend_model_overrides", a.BackendModelOverrides, c.BackendModelOverrides)
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

func classifyCodexModelCatalog(active, candidate *config.Config, reload, restart noteFn) {
	a, c := active.CodexModelCatalog, candidate.CodexModelCatalog
	diffStr(restart, "codex_model_catalog.fallback_path", a.FallbackPath, c.FallbackPath)
	diffStr(restart, "codex_model_catalog.codex_binary_path", a.CodexBinaryPath, c.CodexBinaryPath)
	if !ptrBoolEqual(a.Enabled, c.Enabled) {
		reload("codex_model_catalog.enabled")
	}
	diffStr(reload, "codex_model_catalog.timeout", a.Timeout, c.Timeout)
}

func diffStr(note noteFn, path, left, right string) {
	if left != right {
		note(path)
	}
}

func diffYAML(note noteFn, path string, left, right any) {
	if !yamlEqual(left, right) {
		note(path)
	}
}

func yamlEqual(a, b any) bool {
	left, err := yaml.Marshal(a)
	if err != nil {
		return false
	}
	right, err := yaml.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func ptrBoolEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
