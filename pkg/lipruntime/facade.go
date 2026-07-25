package lipruntime

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
)

func (r *Runtime) api() hostAPI {
	if r == nil {
		return nil
	}
	return r.host
}

// ExecutorView returns the stable generation-dispatching executor facade.
func (r *Runtime) ExecutorView() lipsdk.ExecutorView {
	if h := r.api(); h != nil {
		return h.ExecutorView()
	}
	return nil
}

// Ready reports whether the runtime has a usable active-generation executor.
func (r *Runtime) Ready() bool { h := r.api(); return h != nil && h.Ready() }
