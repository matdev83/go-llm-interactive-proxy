// Package configreload owns typed field-level reloadability policy for runtime
// configuration changes. It classifies every supported field explicitly through
// maintained section comparators (no reflection and no YAML section marshaling)
// and returns secret-safe restart requirements without configuration values.
package configreload
