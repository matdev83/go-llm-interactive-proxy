package main

import (
	"context"
	"errors"

	sdkreload "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/configreload"
)

var errNilReloadSink = errors.New("lipstd: nil reload trigger sink")

// ReloadTriggerSink is the narrow coordinator surface used by OS signal and
// (later) management-API driving adapters. Callers never supply path or YAML.
type ReloadTriggerSink interface {
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
}
