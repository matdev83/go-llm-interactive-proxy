// Package toolcallrepair holds YAML config decode for the standard-distribution
// tool-call-repair feature (ADR 0007 / issue #152).
//
// The SDK Finalizer adapter and Engine live in internal/core/toolcallrepair;
// standardplugins.featureToolCallRepair maps DecodeConfig output into that
// adapter and contributes ToolCallFinalizationMaxArgsBytes on FeatureBundle.
// Enablement is config.PluginConfig.Enabled only (plain bool: omit means false /
// opt-out when a matching features row is present).
package toolcallrepair
