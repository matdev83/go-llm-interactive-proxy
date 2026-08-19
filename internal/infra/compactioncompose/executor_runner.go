package compactioncompose

import (
	"context"
	"errors"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
)

// GenerationExecutorRunner closes the scheduler/executor construction cycle.
type GenerationExecutorRunner struct{ exec *runtime.Executor }

func NewGenerationExecutorRunner() *GenerationExecutorRunner    { return &GenerationExecutorRunner{} }
func (r *GenerationExecutorRunner) Bind(exec *runtime.Executor) { r.exec = exec }
func (r *GenerationExecutorRunner) Execute(ctx context.Context, call *lipapi.Call) (lipapi.EventStream, error) {
	if r == nil || r.exec == nil {
		return nil, errors.New("compactioncompose: generation executor is not bound")
	}
	return r.exec.Execute(ctx, call)
}

var _ auxreq.ExecutorRunner = (*GenerationExecutorRunner)(nil)
