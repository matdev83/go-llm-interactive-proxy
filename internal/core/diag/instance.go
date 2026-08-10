package diag

import (
	"fmt"
	"sort"
	"strings"
)

// InstanceDiagnostic is the bounded common operator projection for an extension
// instance. Protocol-specific data belongs in Details, supplied by its owner.
type InstanceDiagnostic struct {
	ID                string      `json:"id"`
	InstanceID        string      `json:"instance_id,omitempty"`
	FactoryKind       string      `json:"factory_kind"`
	Origin            string      `json:"origin"`
	Enabled           bool        `json:"enabled"`
	Family            string      `json:"family,omitempty"`
	Profile           string      `json:"profile,omitempty"`
	Capabilities      []string    `json:"capabilities,omitempty"`
	RouteClaims       []string    `json:"route_claims,omitempty"`
	InventoryState    string      `json:"inventory_state,omitempty"`
	Conformance       string      `json:"conformance,omitempty"`
	ConfigError       string      `json:"config_error,omitempty"`
	Details           []SafeField `json:"details,omitempty"`
	BasePath          string      `json:"base_path,omitempty"`
	WebSocketEnabled  bool        `json:"websocket_enabled,omitempty"`
	ContinuationStore string      `json:"continuation_store,omitempty"`
	ContinuationTTL   string      `json:"continuation_ttl,omitempty"`
	AllowedOrigins    []string    `json:"allowed_origins,omitempty"`
}

type SafeField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

const (
	maxDiagnosticDetails = 16
	maxDiagnosticBytes   = 256
)

// SanitizeInstanceDiagnostic applies the common cardinality, byte, and control
// character bounds before a contribution-owned projection reaches HTTP JSON.
func SanitizeInstanceDiagnostic(in InstanceDiagnostic) (InstanceDiagnostic, error) {
	out := in
	for name, value := range map[string]string{
		"id": in.ID, "factory_kind": in.FactoryKind, "origin": in.Origin,
		"family": in.Family, "profile": in.Profile, "inventory_state": in.InventoryState,
		"conformance": in.Conformance, "config_error": in.ConfigError,
	} {
		if strings.ContainsAny(value, "\r\n\t") {
			return InstanceDiagnostic{}, fmt.Errorf("diag: %s contains control characters", name)
		}
	}
	if len(in.Details) > maxDiagnosticDetails {
		return InstanceDiagnostic{}, fmt.Errorf("diag: too many extension details")
	}
	out.Details = make([]SafeField, 0, len(in.Details))
	for _, field := range in.Details {
		if field.Key == "" || len(field.Key) > maxDiagnosticBytes || len(field.Value) > maxDiagnosticBytes || strings.ContainsAny(field.Key+field.Value, "\r\n\t") {
			return InstanceDiagnostic{}, fmt.Errorf("diag: invalid extension detail")
		}
		out.Details = append(out.Details, field)
	}
	sort.Slice(out.Details, func(i, j int) bool { return out.Details[i].Key < out.Details[j].Key })
	out.Capabilities = sortedCopy(in.Capabilities)
	out.RouteClaims = sortedCopy(in.RouteClaims)
	return out, nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
