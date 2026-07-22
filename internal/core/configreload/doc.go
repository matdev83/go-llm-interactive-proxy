// Package configreload owns typed field-level reloadability policy for runtime
// configuration changes and the shared reload trigger/result vocabulary used by
// the serialized reload coordinator. It classifies every supported field
// explicitly through maintained section comparators (no reflection and no YAML
// section marshaling) and returns secret-safe restart requirements without
// configuration values. Trigger envelopes never carry paths, YAML, or URLs.
package configreload
