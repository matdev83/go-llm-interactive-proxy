// Package feature defines typed extension planes and the contribution lifecycle for
// feature plugins in Go-LIP.
//
// # Contribution Lifecycle
//
// A feature plugin contributes capabilities by assembling typed extension plane values
// into a [ContributionSet]. The lifecycle proceeds as follows:
//
//  1. Construct a mutable set using [NewContributionSet].
//  2. Add typed values using the package-level [Contribute] function. [Contribute] tags
//     contributions with [SourceFeature] and enforces fail-before-mutate semantics: if
//     validation or combination fails, the set remains unmodified and an [AttributedError]
//     attributing the contributor ID and plane ID is returned.
//  3. Once all contributions are accumulated, call [ContributionSet.Freeze] to produce an
//     immutable [FrozenPlaneSet].
//  4. Wrap the frozen planes and any optional plugin lifecycles into a [FeatureBundle] by
//     calling [BundleFromPlanes]. [BundleFromPlanes] assigns [SchemaVersionV1] and
//     defensively clones the frozen plane set and lifecycle slices, preserving nil vs
//     explicit empty slice semantics without validation side effects.
//
// Plugin lifecycles ([plugin.Lifecycle]) are managed on a dedicated runtime side channel
// and are distinct from extension planes. (Historical note: named extension plane fields
// on [FeatureBundle] were removed in favor of [FrozenPlaneSet].)
//
// # Reading Values
//
// Plane values are read from a [FrozenPlaneSet] using the generic package-level function
// [Get]:
//
//	values := feature.Get(bundle.PlaneSet, feature.PlaneRequestTransforms)
//
// Package-level generic functions are used because Go methods on structs cannot introduce
// type parameters. Key read semantics include:
//
//   - If a plane was not contributed or is absent from the set, [Get] returns the zero value
//     of the plane's type (e.g. nil for slice-valued planes, 0 for scalar planes).
//   - Slice-valued planes return defensive copies on ordinary [Get] calls to guarantee that
//     caller modifications do not corrupt the frozen snapshot.
//   - Runtime borrowed request execution views ([RequestExecutionView]) are internal generated
//     optimizations used on executor hot paths and must not be used by plugin authors.
//
// # Plane Declarations
//
// An extension plane is declared as a [Plane] specification defining its types, combination
// rules, validation, and diagnostics metadata:
//
//   - ID: A globally unique, non-empty, stable string identifier.
//   - Multiplicity: Must be [MultOrdered] (multiple ordered contributions combined in
//     registration order) or [MultExclusive] (at most one occupant permitted, failing fast
//     on conflict).
//   - Rules ([SourceRules]): Explicit per-source combination rules for [SourceFeature],
//     [SourceHost], and [SourceGenerationBinder]. Unsupported sources remain the zero value
//     [CombUnsupported].
//   - NilPolicy: Defines handling of nil contributions ([NilNotApplicable], [NilReject],
//     [NilSkip]), evaluated before validation or combination. For interface-valued planes,
//     an explicit IsNil predicate must be provided to detect typed-nil pointers boxed in
//     interfaces without runtime reflection.
//   - Validate: Optional validator function for incoming contribution values.
//   - Combine: Folding function that combines incoming values with the accumulated state
//     and returns the retained value for the specified source rule. It must not mutate
//     caller-owned or current stored state on failure; [ContributionSet] supplies defensive
//     clones so failed combination leaves the set unchanged.
//   - Identity & ValidateIdentity: Required for exclusive planes and replace-by-identity
//     sources to extract and validate stable identity strings.
//   - ExclusiveConflictError: Optional compatibility error; valid only when at least one
//     source rule is [CombExclusive]; conflict still preserves generic exclusive conflict
//     classification ([ErrExclusiveConflict]).
//   - Diagnostics ([DiagnosticDescriptor]): Configures operator inventory and privilege
//     projection. Descriptor fields include:
//   - StageID: Identifies a legal lifecycle stage ([ValidateStageID]).
//   - Materialize: Creates diagnostic occupants from plane values.
//   - Privileges: Projects privilege flags from plane values.
//   - CoalesceGroup: Groups compatible stage occupancy across planes.
//   - Order: Gives deterministic ordering across stage diagnostics.
//     When a StageID is set, Materialize must be non-nil, Order must be > 0, and the stage ID
//     must be valid. If StageID is absent, descriptor metadata must be empty.
//   - RequestMaterializer & RequestBorrow: An optional sorting/materialization transform for
//     request execution snapshots. When RequestBorrow is enabled, a non-nil RequestMaterializer
//     is required.
//   - [Plane.ValidateDeclaration] and [ValidateManifest] enforce these rules and check catalog
//     consistency across all declarations.
//
// # Generated-File Policy
//
// The extension plane catalog and dispatch machinery follow a strict code generation policy:
//
//   - pkg/lipsdk/feature/plane_manifest.go is the canonical hand-authored catalog of standard
//     plane declarations.
//
//   - pkg/lipsdk/feature/plane_generated.go is checked in and must not be edited manually.
//
//   - After modifying or adding plane declarations, run:
//
//     go run ./scripts/generate-feature-planes.go
//
//   - Verify that generated files are up to date with:
//
//     go run ./scripts/generate-feature-planes.go -check
//
// The generator produces typed storage and ordinal dispatch adapters that eliminate runtime
// reflection, unsafe type conversions, and request-path key lookup.
//
// # Standard Distribution Registration
//
// Adding a standard in-process feature implementation to Go-LIP involves:
//
//  1. The feature implementation package lives under internal/plugins/features/<feature>.
//  2. The factory function that decodes YAML configuration, constructs a [ContributionSet],
//     adds contributions via [Contribute], freezes the set, and returns a [FeatureBundle]
//     via [BundleFromPlanes] is implemented in internal/standardplugins/features_install.go.
//  3. The sole registration table edit is adding exactly one FeatureRegistration row to
//     internal/standardplugins/standard_table.go in StandardBundle().Features.
//  4. Do not add feature-specific branches or types to core/runtime or any other registry.
//  5. Optional executable backend connectors use an independent gRPC manifest discovery
//     mechanism and are out of this feature-registration path.
package feature
