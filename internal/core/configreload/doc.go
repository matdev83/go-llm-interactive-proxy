// Package configreload owns typed field-level reloadability policy for runtime
// configuration changes. The secret-safe reload trigger/result/status vocabulary
// is declared once in pkg/lipsdk/configreload; this package aliases those types for
// transitional internal call sites and owns history, sanitization, and policy
// algorithms. Trigger envelopes never carry paths, YAML, or URLs.
package configreload
