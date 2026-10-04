package bundle

// This file is the composition seam of the B-leg path virtualization feature: the
// ONE place that turns a compiled configuration into the three components
// requirements.md 5.x and 8.x ask for, and the ONE place that decides which
// existing extension planes carry them.
//
// Four decisions shape it, and each is forced rather than chosen:
//
//  1. THREE COMPONENTS, THREE ALREADY-REGISTERED PLANES. design.md
//     "Configuration and Composition" prescribes the attempt transform, the
//     request-part hook, and the path-expansion finalizer, and adds a new generic
//     plane only if the mandatory buffering contract demonstrably requires one.
//     Task 7.3 proved it does not: the registered finalizer plane carries the whole
//     declaration, because the completeness requirement travels as the SDK's own
//     BufferingRequirement capability on the finalizer value rather than as a
//     separate plane contribution. So this file contributes to PlaneAttemptTransforms,
//     PlaneRequestPartHooks, and PlaneToolCallFinalizers, and to nothing else. A
//     fourth contribution would be a new plane by another name.
//
//  2. THE MODE IS PASSED, NEVER INHERITED. rewrite.Mode's zero value is audit, so a
//     component built without an explicit mode measures where the operator asked to
//     mutate, and does so silently. Both outbound constructors and the finalizer
//     constructor therefore take the resolved mode as a required argument, and this
//     file supplies it from the one compiled resolution. There is no field to leave
//     unset and no default to fall into.
//
//  3. ONE RESOLUTION, THREE CONSUMERS, ONE WORKSPACE AUTHORITY. The compiled
//     exact-name policy is a pointer, so all three components share the single
//     resolver the configuration published; it is not recompiled per component,
//     because a second compilation would be a second policy with its own lookup
//     state and the three passes could then disagree about which surface is
//     path-bearing. The workspace authority is the runtime's own per-turn PIN, and
//     this file neither supplies nor manufactures one: the early pass reads it out of
//     the pinned AttemptMeta.Workspace the runtime projects onto that stage, and the
//     late pass reads the very same pinned snapshot out of the public SDK context
//     projection the runtime publishes alongside session, scope, and principal.
//     Neither pass resolves anything, so no second workspace tag can reach one
//     request (requirements.md 5.6).
//
//  4. A DISABLED RESOLUTION CONSTRUCTS NOTHING. The published value carries no
//     resolver, no bound, and no counts, so this file has nothing to build from and
//     returns an empty bundle. That makes requirements.md 7.1's "disabled by
//     default" a property of the compiled value rather than of a caller's
//     discipline, and it is why the mode is read only after the enablement gate.
//
// The workspace authority deserves one more word, because it used to be the one input
// this file could not decide for itself. It is now the runtime's, and deliberately so:
// the late pass used to be handed a lipworkspace.Resolver, which made this file depend
// on a composition root having one to hand over and left the shipped registration
// binding the SDK's unbound default - i.e. inert - while the late pass re-resolved the
// turn's workspace independently of the pin the early pass used. Reading the pinned
// projection removes both halves of that problem at once: this file needs no authority,
// and the two passes cannot disagree because they read one value.

import (
	"fmt"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/config"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/expansion"
	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization/outbound"
	lipfeature "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/feature"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/hooks"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/request"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/toolcall"
)

// FeatureBundle builds the complete bundle from one compiled resolution.
//
// A disabled resolution returns an empty bundle with only the schema version set:
// no plane is contributed, so a stock deployment that never enables the feature
// builds no attempt transform, no request-part hook, and no expansion finalizer
// (requirements.md 7.1, 8.1).
//
// The workspace authority is not a parameter, because it is not this bundle's to
// choose: the runtime pins one per logical turn and projects it onto both stages the
// two outbound passes read - the attempt metadata and the public SDK context seam
// [github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/workspace.WithWorkspaceView]
// publishes - so a host needs no workspace view of its own to arm the late pass. A
// generation whose runtime projected none has the late pass publish nothing, keep the
// real path, and record a bounded reason (requirements.md 8.1, 8.2).
//
// It returns an error when the declared mandatory bound is outside the range the
// SDK validator accepts, which is the last refusal available at bundle-build time
// and keeps an unusable completeness requirement from reaching a request. Every
// other validation decision belongs to the configuration decoder, which owns the
// operator-facing reasons.
func FeatureBundle(resolved config.Resolved) (lipfeature.FeatureBundle, error) {
	if resolved.Disabled() {
		return lipfeature.FeatureBundle{SchemaVersion: lipfeature.SchemaVersionV1}, nil
	}

	// The mode is read once, here, and handed to all three constructors as an
	// explicit value. Reading it in each call site instead would be three chances to
	// leave a component on the engine's zero value.
	mode := resolved.Mode()

	finalizer, err := expansion.NewFinalizer(resolved.Resolver, mode, resolved.ExpansionPolicy())
	if err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", config.ID, err)
	}

	cs := lipfeature.NewContributionSet()
	if err := lipfeature.Contribute(cs, lipfeature.PlaneAttemptTransforms, config.ID,
		[]request.AttemptTransform{outbound.NewAttemptTransform(mode, resolved.Resolver)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", config.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneRequestPartHooks, config.ID,
		[]hooks.RequestPartHook{outbound.NewRequestPartHook(mode, resolved.Resolver)}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", config.ID, err)
	}
	if err := lipfeature.Contribute(cs, lipfeature.PlaneToolCallFinalizers, config.ID,
		[]toolcall.Finalizer{finalizer}); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", config.ID, err)
	}

	b := lipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		return lipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", config.ID, err)
	}
	return b, nil
}
