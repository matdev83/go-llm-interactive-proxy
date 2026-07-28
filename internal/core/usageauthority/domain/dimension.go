package domain

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/authorityattribution"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type DimensionKey = authorityattribution.DimensionKey
type Dimensions = authorityattribution.Dimensions
type DimensionMatcher = authorityattribution.DimensionMatcher
type DimensionsMatcher = authorityattribution.DimensionsMatcher
type MatchValue = DimensionMatcher

var IsSafeLabelKey = authorityattribution.IsSafeLabelKey

// NormalizeScope applies the configured unknown-attribution mode to a scope view.
func (m UnknownAttribution) NormalizeScope(view scope.PrincipalScopeView) scope.PrincipalScopeView {
	out := view
	out.PrincipalID = m.NormalizeValue(view.PrincipalID)
	out.DisplayName = m.NormalizeValue(view.DisplayName)
	out.AuthMethod = m.NormalizeValue(view.AuthMethod)
	out.CredentialID = m.NormalizeValue(view.CredentialID)
	out.TenantID = m.NormalizeValue(view.TenantID)
	out.OrganizationID = m.NormalizeValue(view.OrganizationID)
	out.WorkspaceID = m.NormalizeValue(view.WorkspaceID)
	out.ProjectID = m.NormalizeValue(view.ProjectID)
	out.DepartmentID = m.NormalizeValue(view.DepartmentID)
	out.CostCenterID = m.NormalizeValue(view.CostCenterID)
	out.ParentTraceID = m.NormalizeValue(view.ParentTraceID)
	if len(view.PolicyLabels) > 0 {
		out.PolicyLabels = make(map[string]string, len(view.PolicyLabels))
		for key, value := range view.PolicyLabels {
			if !IsSafeLabelKey(key) {
				continue
			}
			normalized := m.NormalizeValue(scope.Known(value))
			if normalized.IsKnown() {
				out.PolicyLabels[key] = normalized.String()
			}
		}
		if len(out.PolicyLabels) == 0 {
			out.PolicyLabels = nil
		}
	}
	return out
}

// NormalizeDimensions applies the configured unknown-attribution mode to
// attribution dimensions used for rule matching and store projection.
func (m UnknownAttribution) NormalizeDimensions(d Dimensions) Dimensions {
	out := d
	out.Principal = m.NormalizeValue(d.Principal)
	out.Credential = m.NormalizeValue(d.Credential)
	out.Tenant = m.NormalizeValue(d.Tenant)
	out.Organization = m.NormalizeValue(d.Organization)
	out.Workspace = m.NormalizeValue(d.Workspace)
	out.Project = m.NormalizeValue(d.Project)
	out.Department = m.NormalizeValue(d.Department)
	out.CostCenter = m.NormalizeValue(d.CostCenter)
	out.Backend = m.NormalizeValue(d.Backend)
	out.Model = m.NormalizeValue(d.Model)
	out.Route = m.NormalizeValue(d.Route)
	if len(d.PolicyLabels) > 0 {
		out.PolicyLabels = make(map[string]scope.Value, len(d.PolicyLabels))
		for key, value := range d.PolicyLabels {
			out.PolicyLabels[key] = m.NormalizeValue(value)
		}
	}
	return out
}

// NormalizeValue maps unknown attribution according to the configured mode.
func (m UnknownAttribution) NormalizeValue(v scope.Value) scope.Value {
	switch m {
	case UnknownAttributionKnownEmpty:
		if v.IsUnknown() {
			return scope.Known("")
		}
		return v
	case UnknownAttributionUnknown:
		if v.IsKnownEmpty() {
			return scope.Unknown()
		}
		return v
	default:
		return v
	}
}
