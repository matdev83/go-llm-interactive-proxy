// Package conversationview implements canonical conversation-view projection policy
// for proxy-owned content at the A-leg/B-leg boundary, including replay-stable
// semantic message identity, exclusion classification, and persistent steering.
//
// Persistent steering is client-hidden but model-visible. The remote provider
// receives it and may reveal it, so it must not carry secrets or credentials.
// Diagnostics are bounded and content-free: no plaintext, digest, or OverlayID
// becomes a metric label.
package conversationview
