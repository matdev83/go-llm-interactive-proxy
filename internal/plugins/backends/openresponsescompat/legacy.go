package openresponsescompat

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// orderedItemProjectionTarget derives the explicit legacy→ordered-items projector
// target from the instance's declared capabilities and dialect support. Call-level
// extensions may be carried only when the backend declares the exact extension type.
func orderedItemProjectionTarget(spec BackendSpec) lipapi.OrderedItemProjectionTarget {
	target := lipapi.OrderedItemProjectionTargetFromCaps(spec.Caps)
	target.SupportedExtensions = append([]lipapi.ExtensionRequirement(nil), spec.DialectSupport.ExtensionTypes...)
	return target
}

// normalizeLegacyAuthority maps a legacy message-authority call onto an
// item-authority call through the explicit canonical legacy→ordered-items
// projector (Requirement 9.13, Task 5.3). Item-authority calls pass through
// unchanged. The source call is never mutated: the projected call is a deep
// clone with the ordered items replacing the legacy Instructions/Messages.
func normalizeLegacyAuthority(id string, spec BackendSpec, call lipapi.Call) (lipapi.Call, error) {
	if call.HasItemAuthority() {
		return call, nil
	}
	items, _, err := lipapi.ProjectLegacyToOrderedItems(call, orderedItemProjectionTarget(spec))
	if err != nil {
		return lipapi.Call{}, fmt.Errorf("%s: %w", id, err)
	}
	out := lipapi.CloneCall(call)
	out.Instructions = nil
	out.Messages = nil
	out.Items = items
	return out, nil
}
