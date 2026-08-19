package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/auxreq"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/compactiondetect"
)

type BackgroundAuxScheduler = auxreq.BackgroundScheduler

func releaseProcessInputOwnership(in *ProcessServicesInput, release func()) {
	release()
	if in.BackgroundAux != nil {
		_ = in.BackgroundAux.Close()
		in.BackgroundAux = nil
	}
}

func adoptBackgroundAuxAndDetector(in *ProcessServicesInput, ps *ProcessServices, register func(func() error)) {
	ps.BackgroundAux, in.BackgroundAux = in.BackgroundAux, nil
	if ps.BackgroundAux != nil {
		register(ps.BackgroundAux.Close)
	}
	// The detector is process-owned observational state shared by generations.
	ps.CompactionDetector = compactiondetect.New(compactiondetect.Config{})
}
