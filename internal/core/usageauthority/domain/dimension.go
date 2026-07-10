package domain

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/scope"
)

type DimensionKey string

type Dimensions struct {
	Principal    scope.Value
	Tenant       scope.Value
	Organization scope.Value
	Workspace    scope.Value
	Project      scope.Value
	Department   scope.Value
	CostCenter   scope.Value
	Backend      scope.Value
	Model        scope.Value
	Route        scope.Value
	PolicyLabels map[string]scope.Value
}

func (d Dimensions) Key() DimensionKey {
	key, _ := d.KeyErr()
	return key
}

func (d Dimensions) KeyErr() (DimensionKey, error) {
	var b strings.Builder
	writeDimension := func(name string, value scope.Value) {
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(encodeScopeValue(value))
		b.WriteByte('|')
	}

	writeDimension("principal", d.Principal)
	writeDimension("tenant", d.Tenant)
	writeDimension("organization", d.Organization)
	writeDimension("workspace", d.Workspace)
	writeDimension("project", d.Project)
	writeDimension("department", d.Department)
	writeDimension("cost_center", d.CostCenter)
	writeDimension("backend", d.Backend)
	writeDimension("model", d.Model)
	writeDimension("route", d.Route)

	labelKeys := make([]string, 0, len(d.PolicyLabels))
	for key := range d.PolicyLabels {
		if !isSafeLabelKey(key) {
			return "", fmt.Errorf("%w: invalid policy label key %q", ErrInvalidDimension, key)
		}
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		b.WriteString("label.")
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(encodeScopeValue(d.PolicyLabels[key]))
		b.WriteByte('|')
	}

	return DimensionKey(strings.TrimSuffix(b.String(), "|")), nil
}

func encodeScopeValue(v scope.Value) string {
	if v.IsUnknown() {
		return "u"
	}
	return "k:" + url.QueryEscape(v.String())
}

func isSafeLabelKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

type DimensionMatcher struct {
	Value        scope.Value
	MatchUnknown bool
}

type MatchValue = DimensionMatcher

func (m DimensionMatcher) Matches(actual scope.Value) bool {
	if m.MatchUnknown {
		return actual.IsUnknown()
	}
	if m.Value.IsUnknown() {
		return true
	}
	return m.Value.Equal(actual)
}

func (m DimensionMatcher) configured() bool {
	return m.MatchUnknown || m.Value.IsKnown()
}

type DimensionsMatcher struct {
	Principal    DimensionMatcher
	Tenant       DimensionMatcher
	Organization DimensionMatcher
	Workspace    DimensionMatcher
	Project      DimensionMatcher
	Department   DimensionMatcher
	CostCenter   DimensionMatcher
	Backend      DimensionMatcher
	Model        DimensionMatcher
	Route        DimensionMatcher
	Labels       map[string]DimensionMatcher
}

func (m DimensionsMatcher) Matches(actual Dimensions) bool {
	if !m.Principal.Matches(actual.Principal) {
		return false
	}
	if !m.Tenant.Matches(actual.Tenant) {
		return false
	}
	if !m.Organization.Matches(actual.Organization) {
		return false
	}
	if !m.Workspace.Matches(actual.Workspace) {
		return false
	}
	if !m.Project.Matches(actual.Project) {
		return false
	}
	if !m.Department.Matches(actual.Department) {
		return false
	}
	if !m.CostCenter.Matches(actual.CostCenter) {
		return false
	}
	if !m.Backend.Matches(actual.Backend) {
		return false
	}
	if !m.Model.Matches(actual.Model) {
		return false
	}
	if !m.Route.Matches(actual.Route) {
		return false
	}
	for key, matcher := range m.Labels {
		if !isSafeLabelKey(key) {
			return false
		}
		if !matcher.Matches(actual.PolicyLabels[key]) {
			return false
		}
	}
	return true
}

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
	return out
}

// NormalizeDimensions applies the configured unknown-attribution mode to
// attribution dimensions used for rule matching and store projection.
func (m UnknownAttribution) NormalizeDimensions(d Dimensions) Dimensions {
	out := d
	out.Principal = m.NormalizeValue(d.Principal)
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
