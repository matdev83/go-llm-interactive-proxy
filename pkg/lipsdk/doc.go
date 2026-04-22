// Package lipsdk defines stable plugin-facing contracts used by official and
// external plugins. Hook interfaces live in the nested package lipsdk/hooks.
//
// Backend, frontend, and feature factories for the reference distribution are registered in
// internal/pluginreg (RegisterBackend, RegisterFrontend, RegisterFeature) using opaque YAML nodes;
// StandardDistributionRequirements lists ids validated at startup.
//
// [BackendBuild] is intentionally opaque (see factory.go) so this package never depends on core runtime types.
package lipsdk
