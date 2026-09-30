package extensions

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/controltool"
)

// ControlToolProjectionRequest is the generic input of the one bounded
// control-projection decision taken for a single candidate call. It carries no
// client-derived provenance: ProviderID is the generation-frozen identity the
// caller already pinned, and Provider is the provider that same generation
// admitted.
type ControlToolProjectionRequest struct {
	// ProviderID is the frozen identity of the generation control-tool provider.
	// It is validated here so an unnamed owner can never project a contract.
	ProviderID string
	// Provider is the generation provider. The caller must branch on snapshot
	// absence before calling, because this stage always calls the provider.
	Provider controltool.Provider
	// Call is the candidate call after ordinary request mutation.
	Call lipapi.Call
	// Caps are the candidate's own resolved backend capabilities. They are
	// eligibility input for the optional control feature, never authoritative
	// admission or accounting; the caller's own rederive stays authoritative.
	Caps lipapi.BackendCaps
}

// ControlToolProjection is the trusted, in-process result of one control
// projection. Everything it holds is deep-owned and request/attempt-local: it is
// never canonical Extensions, never serialized frontend or backend metadata, and
// never reconstructed from a tool name observed on the wire.
type ControlToolProjection struct {
	// ProviderID is the validated frozen identity of the pinned provider.
	ProviderID string
	// Provider is the pinned generation provider. It is retained so later
	// response handling addresses the same generation instance, never a
	// re-resolved one.
	Provider controltool.Provider
	// Projection is the immutable approved projection. An inactive projection
	// keeps its bounded reason and never claims a proxy-owned control call.
	Projection controltool.Projection
}

// Active reports whether the control tool and instruction were approved for the
// projected candidate.
func (p ControlToolProjection) Active() bool { return p.Projection.Active() }

// Reason returns the bounded, content-free reason an inactive projection carries.
func (p ControlToolProjection) Reason() string { return p.Projection.Reason() }

// ProjectControlTool runs the single generic control-projection decision for
// one candidate call and returns the backend-effective call plus the trusted
// activation the caller must own for the rest of the attempt.
//
// The provider spec is read exactly once per candidate behind the extension
// safety boundary, so a provider panic becomes a bounded, content-free error
// instead of a candidate mutation or a client-visible failure. The pure
// projection owns eligibility and the byte-stable contract, so this stage adds no
// tool-name, feature-name, or eligibility policy of its own.
//
// An invalid spec is a generation defect, not a per-request best effort: it is
// returned as an error and the call is returned unchanged. Ordinary
// ineligibility is not an error; the projection carries its bounded reason and
// the call is returned exactly as supplied.
func ProjectControlTool(in ControlToolProjectionRequest) (lipapi.Call, ControlToolProjection, error) {
	if in.Provider == nil {
		return in.Call, ControlToolProjection{}, fmt.Errorf("extensions: %w: control tool provider is required", controltool.ErrInvalidProvider)
	}
	if err := controltool.ValidateProviderID(in.ProviderID); err != nil {
		return in.Call, ControlToolProjection{}, err
	}
	spec, err := safety.CallValue(safety.BoundaryExtension, "control_tool_provider_spec", func() (controltool.Spec, error) {
		return in.Provider.Spec(), nil
	})
	if err != nil {
		return in.Call, ControlToolProjection{ProviderID: in.ProviderID}, err
	}
	projected, projection, err := controltool.Project(in.Call, spec, in.Caps)
	if err != nil {
		return in.Call, ControlToolProjection{ProviderID: in.ProviderID}, err
	}
	return projected, ControlToolProjection{
		ProviderID: in.ProviderID,
		Provider:   in.Provider,
		Projection: projection,
	}, nil
}
