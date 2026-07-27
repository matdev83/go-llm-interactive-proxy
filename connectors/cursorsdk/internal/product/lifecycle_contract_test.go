package product

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

// RunStream must satisfy the public managed-stream contract used by the host
// over the executable-plugin boundary (Cancel/Close/Recv).
var _ lipapi.ManagedEventStream = (*RunStream)(nil)
