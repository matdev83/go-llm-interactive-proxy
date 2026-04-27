package modelcatalog

import (
	"net/url"
	"strings"
	"time"
)

// CatalogDiagnosticsStatus is the high-level operator-visible catalog state (requirements 9.1).
type CatalogDiagnosticsStatus string

const (
	CatalogDiagDisabled    CatalogDiagnosticsStatus = "disabled"
	CatalogDiagUnavailable CatalogDiagnosticsStatus = "unavailable"
	CatalogDiagStale       CatalogDiagnosticsStatus = "stale"
	CatalogDiagEnabled     CatalogDiagnosticsStatus = "enabled"
)

// CatalogDiagnosticsJSON is the operator JSON for GET model_catalog.diagnostics_path (not a stable public API).
type CatalogDiagnosticsJSON struct {
	UsageEnabled bool `json:"usage_enabled"`

	// Status is disabled when model_catalog.enabled is false; otherwise reflects snapshot presence and freshness.
	Status CatalogDiagnosticsStatus `json:"status"`

	// Snapshot is set when a valid local/remote catalog snapshot is active in the runtime.
	Snapshot *CatalogSnapshotDiagnostics `json:"snapshot,omitempty"`

	// LastRefreshErrorCategory is empty when the last refresh completed successfully.
	LastRefreshErrorCategory RefreshFailureCategory `json:"last_refresh_error_category,omitempty"`

	// SourceURLRedacted never includes userinfo (requirement 10.4).
	SourceURLRedacted string `json:"source_url_redacted,omitempty"`

	ExternalUpdatesEnabled bool `json:"external_updates_enabled"`
	// UpdateIntervalSeconds is 0 when unset or invalid in config.
	UpdateIntervalSeconds float64 `json:"update_interval_seconds,omitempty"`
}

// CatalogSnapshotDiagnostics is non-request content: generation and fetch metadata only.
type CatalogSnapshotDiagnostics struct {
	Generation  string    `json:"generation"`
	FetchedAt   time.Time `json:"fetched_at"`
	ContentHash string    `json:"content_hash,omitempty"`
}

// CatalogStatusHandlerConfig configures [BuildCatalogDiagnosticsJSON] for the operator HTTP handler
// (internal/stdhttp: NewCatalogStatusHandler).
type CatalogStatusHandlerConfig struct {
	Runtime *CatalogRuntime

	UsageEnabled           bool
	ExternalUpdatesEnabled bool
	UpdateInterval         time.Duration
	SourceURL              string

	// Now defaults to time.Now if nil (tests may inject a fixed clock).
	Now func() time.Time
}

// RedactSourceURL returns a userinfo-free URL string for operator display, or empty when input is empty.
func RedactSourceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "redacted:invalid"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSpace(u.String())
}

// BuildCatalogDiagnosticsJSON builds the JSON DTO for the current moment (no prompt/session content).
func BuildCatalogDiagnosticsJSON(cfg CatalogStatusHandlerConfig) CatalogDiagnosticsJSON {
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	out := CatalogDiagnosticsJSON{
		UsageEnabled:           cfg.UsageEnabled,
		SourceURLRedacted:      RedactSourceURL(cfg.SourceURL),
		ExternalUpdatesEnabled: cfg.ExternalUpdatesEnabled,
	}
	if cfg.UpdateInterval > 0 {
		out.UpdateIntervalSeconds = cfg.UpdateInterval.Seconds()
	}

	if !cfg.UsageEnabled {
		out.Status = CatalogDiagDisabled
		if cfg.Runtime != nil {
			if snap, ok := cfg.Runtime.Active(); ok {
				out.Snapshot = snapshotDiagFrom(&snap)
			}
			out.LastRefreshErrorCategory = cfg.Runtime.LastRefreshFailure()
		}
		return out
	}

	if cfg.Runtime == nil {
		out.Status = CatalogDiagUnavailable
		return out
	}
	snap, active := cfg.Runtime.Active()
	if !active || snap.Index == nil {
		out.Status = CatalogDiagUnavailable
		out.LastRefreshErrorCategory = cfg.Runtime.LastRefreshFailure()
		return out
	}

	out.Snapshot = snapshotDiagFrom(&snap)
	out.LastRefreshErrorCategory = cfg.Runtime.LastRefreshFailure()

	if catalogSnapshotIsStale(now(), snap, cfg) {
		out.Status = CatalogDiagStale
		return out
	}
	out.Status = CatalogDiagEnabled
	return out
}

func snapshotDiagFrom(s *Snapshot) *CatalogSnapshotDiagnostics {
	if s == nil {
		return nil
	}
	return &CatalogSnapshotDiagnostics{
		Generation:  s.Generation,
		FetchedAt:   s.FetchedAt,
		ContentHash: s.ContentHash,
	}
}

func catalogSnapshotIsStale(now time.Time, snap Snapshot, cfg CatalogStatusHandlerConfig) bool {
	if cfg.Runtime == nil {
		return false
	}
	// Any non-empty failure category after the last attempt means the active snapshot is not fresh.
	if lf := cfg.Runtime.LastRefreshFailure(); lf != RefreshFailureNone {
		return true
	}
	if cfg.ExternalUpdatesEnabled && cfg.UpdateInterval > 0 {
		if now.Sub(snap.FetchedAt) > 2*cfg.UpdateInterval {
			return true
		}
	}
	return false
}
