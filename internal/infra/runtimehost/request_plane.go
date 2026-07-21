package runtimehost

import "net/http"

// PublishedRequestPlane is the narrow immutable request-plane surface bound to a
// Generation for preparation, publication, and acquire. The future data-plane
// dispatcher only needs Handler(); Close remains on OwnedCloser.
//
// Implementations must not expose mutable config, runtime.App, mixed Built
// handles, ProcessServices ownership, or process closers. runtimehost never
// imports runtimebundle; concrete bundles satisfy this interface from outside.
type PublishedRequestPlane interface {
	QuiesceCloser
	Handler() http.Handler
}
