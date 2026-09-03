package runtimebundle

import (
	"context"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactioncompose"
	compactiondetect "github.com/matdev83/go-llm-interactive-proxy/internal/infra/compactiondetect"
)

type BackgroundAuxScheduler = auxreq.BackgroundScheduler

func releaseProcessInputOwnership(in *ProcessServicesInput, release func()) {
	release()
	if in.BackgroundAux != nil {
		_ = in.BackgroundAux.Close()
		in.BackgroundAux = nil
	}
}

func adoptBackgroundAuxAndDetector(ctx context.Context, in *ProcessServicesInput, ps *ProcessServices, register func(func() error)) {
	if in.BackgroundAux == nil {
		in.BackgroundAux = compactioncompose.NewProductionBackgroundScheduler(ctx, in.Cfg)
	}
	ps.BackgroundAux, in.BackgroundAux = in.BackgroundAux, nil
	if ps.BackgroundAux != nil {
		register(ps.BackgroundAux.Close)
	}
	ps.CompactionDetector = compactiondetect.New(compactiondetect.Config{})
}
