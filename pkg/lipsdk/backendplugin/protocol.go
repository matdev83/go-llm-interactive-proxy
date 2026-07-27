package backendplugin

import (
	"fmt"
	"slices"
	"strings"
)

// Feature is a negotiable protocol feature advertisement.
type Feature struct {
	// Name is the feature identifier.
	Name string
	// Required means the peer must understand the feature or negotiation fails.
	Required bool
}

// ProtocolOffer is one side's protocol advertisement.
type ProtocolOffer struct {
	// Major is the protocol major version.
	Major uint32
	// Minor is the protocol minor version.
	Minor uint32
	// Features is the advertised feature set.
	Features []Feature
	// DisableTransportRetries must be true on both sides.
	DisableTransportRetries bool
}

// Negotiation is the deterministic outcome of Negotiate.
type Negotiation struct {
	// Compatible is true when majors match and required features are mutual.
	Compatible bool
	// NegotiatedMinor is min(host.Minor, plugin.Minor).
	NegotiatedMinor uint32
	// EnabledFeatures is the sorted intersection of host-requested features present on the plugin.
	EnabledFeatures []string
	// RejectReason explains incompatibility when Compatible is false.
	RejectReason string
	// TransportPolicy is the accepted transport policy.
	TransportPolicy TransportPolicy
	// PluginMajor is the plugin-advertised major carried on NegotiateResponse.
	PluginMajor uint32
	// PluginMinor is the plugin-advertised minor carried on NegotiateResponse.
	PluginMinor uint32
	// PluginFeatures is the plugin-advertised feature set carried on NegotiateResponse.
	PluginFeatures []Feature
	// NegotiationToken binds a compatible outcome to a later ConfigureRequest (wire field).
	NegotiationToken string
}

// Negotiate performs fail-closed domain protocol negotiation.
func Negotiate(host, plugin ProtocolOffer) (Negotiation, error) {
	policy := DefaultTransportPolicy()
	base := Negotiation{
		TransportPolicy: policy,
		PluginMajor:     plugin.Major,
		PluginMinor:     plugin.Minor,
		PluginFeatures:  append([]Feature(nil), plugin.Features...),
	}
	if !host.DisableTransportRetries || !plugin.DisableTransportRetries {
		base.Compatible = false
		base.RejectReason = ErrTransportRetriesRequiredDisabled.Error()
		return base, ErrTransportRetriesRequiredDisabled
	}
	if err := policy.Validate(); err != nil {
		base.Compatible = false
		base.RejectReason = err.Error()
		return base, err
	}
	if host.Major != plugin.Major || host.Major != ProtocolMajorV1 {
		reason := fmt.Sprintf("%v: host=%d plugin=%d", ErrIncompatibleMajor, host.Major, plugin.Major)
		base.Compatible = false
		base.RejectReason = reason
		return base, ErrIncompatibleMajor
	}

	hostByName, err := indexFeatures(host.Features)
	if err != nil {
		base.Compatible = false
		base.RejectReason = err.Error()
		return base, err
	}
	pluginByName, err := indexFeatures(plugin.Features)
	if err != nil {
		base.Compatible = false
		base.RejectReason = err.Error()
		return base, err
	}

	for name, pf := range pluginByName {
		if !pf.Required {
			continue
		}
		if _, ok := hostByName[name]; !ok {
			reason := fmt.Sprintf("%v: plugin requires %s", ErrUnknownRequiredFeature, name)
			base.Compatible = false
			base.RejectReason = reason
			return base, ErrUnknownRequiredFeature
		}
	}

	enabled := make([]string, 0)
	for name, hf := range hostByName {
		pf, ok := pluginByName[name]
		if !ok {
			if hf.Required {
				reason := fmt.Sprintf("%v: %s", ErrUnknownRequiredFeature, name)
				base.Compatible = false
				base.RejectReason = reason
				return base, ErrUnknownRequiredFeature
			}
			continue
		}
		enabled = append(enabled, pf.Name)
	}
	slices.Sort(enabled)

	minor := min(host.Minor, plugin.Minor)
	base.Compatible = true
	base.NegotiatedMinor = minor
	base.EnabledFeatures = enabled
	return base, nil
}

func indexFeatures(features []Feature) (map[string]Feature, error) {
	out := make(map[string]Feature, len(features))
	for _, f := range features {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			return nil, ErrEmptyFeatureName
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateFeature, name)
		}
		out[name] = Feature{Name: name, Required: f.Required}
	}
	return out, nil
}

// MustNegotiateBeforeConfigure rejects configure when negotiation was not compatible.
func MustNegotiateBeforeConfigure(neg Negotiation) error {
	if !neg.Compatible {
		if neg.RejectReason != "" {
			return fmt.Errorf("%w: %s", ErrConfigureBeforeNegotiate, neg.RejectReason)
		}
		return ErrConfigureBeforeNegotiate
	}
	return nil
}
