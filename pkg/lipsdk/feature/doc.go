// Package feature holds stable contracts for feature plugins beyond raw hook lists.
//
// FeatureBundle is the versioned unit a feature factory contributes: hook chains,
// optional lifecycles, and schema metadata. Empty or nil hook slices mean that
// extension chain is absent for this plugin; composition must not synthesize handlers.
// Use [FeatureBundle.Validate] before merge once factories can return arbitrary bundles.
//
// Legal extension pipeline stage ids and descriptors live in this package (R2); the
// core owns ordering and diagnostics surfaces them alongside hook-derived occupancy.
package feature
