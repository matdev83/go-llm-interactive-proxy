// Package steering defines the trusted producer contract for persistent
// backend-only steering overlays.
//
// The Writer is an explicitly constructed trusted service bound to an
// authoritative A-leg scope via construction time application context. It is
// never a global registry or map[string]any locator and never exposed through
// client frontends. Each overlay carries a bounded stable OverlayID, bounded
// reason, bounded model-visible message payload (role+text, ≤64KiB), placement
// and anchor-missing policy. No client/data-plane transport API is provided.
package steering
