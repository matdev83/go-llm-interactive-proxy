// Package lipsdk defines stable plugin-facing contracts used by official and
// external plugins. Hook interfaces live in the nested package lipsdk/hooks.
//
// Authentication and backend security: [auth] has protocol-neutral request metadata, decisions,
// and event DTOs; [BackendSecurityProfile] declares backend credential posture for registry startup
// validation. Neither carries plugin-private config blobs—only registered metadata and stable enums.
//
// Ownership: this tree holds plugin registration, hook contracts, and SDK-facing types,
// not application orchestration. Routing, recovery, and extension-stage policy stay in
// internal/core. Do not import internal packages, internal/stdhttp, or composition assembly
// packages from here; pkg/lipsdk/execview holds the canonical principal context key;
// pkg/lipsdk/transport/httpauth holds HTTP-native auth provider and error-render contracts.
// Factory registration for the standard distribution still happens in internal/pluginreg at the
// composition root, not from types defined here.
//
// Backend, frontend, and feature factories for the reference distribution are registered in
// internal/pluginreg (RegisterBackend, RegisterFrontend, RegisterFeature) using opaque YAML nodes
// and [FrontendMountOptions] for frontend HTTP wiring;
// StandardDistributionRequirements lists ids validated at startup.
//
// [BackendBuild] is intentionally opaque (see factory.go) so this package never depends on core runtime types.
package lipsdk
