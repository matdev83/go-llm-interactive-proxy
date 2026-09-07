package archtest

// CoreOwnershipCategory classifies why a top-level internal/core package may
// remain in the kernel after feature-ownership full closure (Req 12.3/13.2).
type CoreOwnershipCategory string

const (
	// CoreOwnershipKernelInvariant is required for correct base proxy behavior
	// with all optional standard features disabled, or is a universal invariant.
	CoreOwnershipKernelInvariant CoreOwnershipCategory = "kernel invariant"
	// CoreOwnershipGenericExtension is a feature-neutral extension or
	// orchestration mechanism with recorded independent consumers.
	CoreOwnershipGenericExtension CoreOwnershipCategory = "generic extension mechanism"
)

// CoreOwnershipEntry justifies one surviving top-level internal/core package.
// Consumers is required for generic extension mechanisms and names at least
// two independent consumers/producers or a universal invariant.
type CoreOwnershipEntry struct {
	Package   string
	Category  CoreOwnershipCategory
	Reason    string
	Consumers string
}

// CoreOwnershipManifest is the durable core admission table (Task 11.1). A new
// top-level internal/core package fails the admission test until it gains an
// entry here through explicit architecture review.
var CoreOwnershipManifest = []CoreOwnershipEntry{
	{Package: "accessmode", Category: CoreOwnershipKernelInvariant, Reason: "Base proxy execution-mode vocabulary independent of features."},
	{Package: "accounting", Category: CoreOwnershipKernelInvariant, Reason: "Generic token accounting types and ledger contracts."},
	{Package: "admin", Category: CoreOwnershipKernelInvariant, Reason: "Minimal admin command port definitions."},
	{Package: "affinity", Category: CoreOwnershipKernelInvariant, Reason: "Backend connection affinity keys and routing selectors."},
	{Package: "auth", Category: CoreOwnershipKernelInvariant, Reason: "Client authentication, credential verification, principal mapping."},
	{Package: "authorityattribution", Category: CoreOwnershipKernelInvariant, Reason: "Attribution metadata for usage and concurrency leases."},
	{Package: "authoritycoord", Category: CoreOwnershipKernelInvariant, Reason: "Distributed lease and quota coordination across nodes."},
	{Package: "auxreq", Category: CoreOwnershipKernelInvariant, Reason: "Auxiliary request lifecycle, sub-request pipeline, cancellation."},
	{Package: "b2bua", Category: CoreOwnershipKernelInvariant, Reason: "Back-to-back user agent state machine, attempt loop, upstream I/O."},
	{Package: "billing", Category: CoreOwnershipKernelInvariant, Reason: "Billing identity, quote/exposure policy, immutable usage contracts."},
	{Package: "capabilities", Category: CoreOwnershipKernelInvariant, Reason: "Model capability bitflags consumed by routing and backends."},
	{Package: "concurrencyauthority", Category: CoreOwnershipKernelInvariant, Reason: "Concurrency lease allocation and enforcement."},
	{Package: "config", Category: CoreOwnershipKernelInvariant, Reason: "Typed base proxy configuration; feature payloads stay opaque subtrees."},
	{Package: "configreload", Category: CoreOwnershipKernelInvariant, Reason: "Dynamic configuration reload and generation swap coordination."},
	{Package: "continuation", Category: CoreOwnershipKernelInvariant, Reason: "Request continuation token domain types."},
	{Package: "continuity", Category: CoreOwnershipKernelInvariant, Reason: "Session continuity contracts and replay markers."},
	{Package: "controlplane", Category: CoreOwnershipKernelInvariant, Reason: "Control-plane state management and cluster observers."},
	{Package: "conversationprojection", Category: CoreOwnershipKernelInvariant, Reason: "Pure backend-effective projection, never_backend exclusion, anchors."},
	{Package: "diag", Category: CoreOwnershipKernelInvariant, Reason: "Diagnostic telemetry, trace attributes, logging context."},
	{Package: "execbackend", Category: CoreOwnershipKernelInvariant, Reason: "Backend executor abstraction and invocation port."},
	{Package: "execctx", Category: CoreOwnershipKernelInvariant, Reason: "Execution context: deadlines, trace identity, cancellation."},
	{Package: "extensions", Category: CoreOwnershipGenericExtension, Reason: "Closed standard extension planes and completion gates.", Consumers: "runtime, traffic, diag, testkit conformance"},
	{Package: "geoip", Category: CoreOwnershipKernelInvariant, Reason: "Client IP geolocation lookup for regional policy routing."},
	{Package: "hooks", Category: CoreOwnershipGenericExtension, Reason: "Extension hook pipeline execution and phase ordering.", Consumers: "extensions, runtime, testkit conformance"},
	{Package: "http", Category: CoreOwnershipKernelInvariant, Reason: "HTTP protocol utilities, header normalization, status mapping."},
	{Package: "identity", Category: CoreOwnershipKernelInvariant, Reason: "Tenant, organization, user identity representation."},
	{Package: "interleavedstate", Category: CoreOwnershipKernelInvariant, Reason: "Routing-required thinker cycle state for route selection and continuity."},
	{Package: "jsonpresence", Category: CoreOwnershipKernelInvariant, Reason: "JSON empty-vs-null presence semantics."},
	{Package: "jsonshape", Category: CoreOwnershipKernelInvariant, Reason: "Structural JSON validation for streams and frontends."},
	{Package: "leglifecycle", Category: CoreOwnershipKernelInvariant, Reason: "Upstream attempt leg lifecycle tracking and abort handling."},
	{Package: "lineage", Category: CoreOwnershipKernelInvariant, Reason: "Request/response message causal lineage tracking."},
	{Package: "localstream", Category: CoreOwnershipKernelInvariant, Reason: "In-memory canonical stream for local execution loops."},
	{Package: "metering", Category: CoreOwnershipKernelInvariant, Reason: "Raw usage event metering ports and aggregation."},
	{Package: "modelcatalog", Category: CoreOwnershipKernelInvariant, Reason: "Static model definitions, tokenizer mappings, context limits."},
	{Package: "modelregistry", Category: CoreOwnershipKernelInvariant, Reason: "Dynamic runtime model registration and lookup."},
	{Package: "modelview", Category: CoreOwnershipKernelInvariant, Reason: "Filtered model visibility views for tenants."},
	{Package: "policy", Category: CoreOwnershipKernelInvariant, Reason: "Rate limit and tier policy definitions."},
	{Package: "routeoverride", Category: CoreOwnershipKernelInvariant, Reason: "Operator A-leg routing-override rules and latest-wins state."},
	{Package: "routing", Category: CoreOwnershipKernelInvariant, Reason: "Backend selection, fallback sequences, weighted health routing."},
	{Package: "runtime", Category: CoreOwnershipKernelInvariant, Reason: "Request/attempt orchestration, output commitment, immutable generations."},
	{Package: "safety", Category: CoreOwnershipKernelInvariant, Reason: "Prompt safety boundary assertions and stream guard invariants."},
	{Package: "securesession", Category: CoreOwnershipKernelInvariant, Reason: "Secure-session authority: creation, verification, rotation, stores."},
	{Package: "snapshotgen", Category: CoreOwnershipKernelInvariant, Reason: "Immutable runtime state snapshot generation."},
	{Package: "state", Category: CoreOwnershipKernelInvariant, Reason: "Core state container and epoch tracking."},
	{Package: "stream", Category: CoreOwnershipKernelInvariant, Reason: "Canonical streaming abstractions, backpressure, chunk multiplexing."},
	{Package: "streamrecovery", Category: CoreOwnershipKernelInvariant, Reason: "Mid-stream disconnect recovery and idempotent retry markers."},
	{Package: "terminal", Category: CoreOwnershipKernelInvariant, Reason: "Final generation completion state evaluation."},
	{Package: "terminalwork", Category: CoreOwnershipKernelInvariant, Reason: "Post-response async terminal work pipeline."},
	{Package: "tokenaccounting", Category: CoreOwnershipKernelInvariant, Reason: "Token counting, budget enforcement, ledger, preflight."},
	{Package: "traffic", Category: CoreOwnershipKernelInvariant, Reason: "Traffic shaping and admission tokens."},
	{Package: "usageauthority", Category: CoreOwnershipKernelInvariant, Reason: "Distributed usage quota coordination and lease renewal."},
	{Package: "workspace", Category: CoreOwnershipKernelInvariant, Reason: "Workspace isolation boundary contracts."},
}

func coreOwnershipByPackage() map[string]CoreOwnershipEntry {
	out := make(map[string]CoreOwnershipEntry, len(CoreOwnershipManifest))
	for _, e := range CoreOwnershipManifest {
		out[e.Package] = e
	}
	return out
}
