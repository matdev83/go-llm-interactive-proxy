package runtimebundle

import "github.com/matdev83/go-llm-interactive-proxy/internal/infra/runtimehost"

// CandidateRuntime satisfies runtimehost.OwnedCloser / QuiesceCloser so a
// generation may own candidate teardown without receiving ProcessServices
// (task 3.1 / req 4.9).
var (
	_ runtimehost.OwnedCloser   = (*CandidateRuntime)(nil)
	_ runtimehost.QuiesceCloser = (*CandidateRuntime)(nil)
)
