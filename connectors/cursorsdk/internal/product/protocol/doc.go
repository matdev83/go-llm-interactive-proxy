// Package protocol defines the versioned bounded NDJSON bridge contract shared
// by the Go Cursor SDK backend and its companion Node bridge.
//
// Frames are one JSON object per line. Shared fixtures under
// ../testdata/fixtures are the source of truth for both language test suites.
// This package must not import lipapi types or Cursor SDK types.
package protocol
