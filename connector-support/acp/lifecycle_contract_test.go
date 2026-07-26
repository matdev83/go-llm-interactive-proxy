package acp

import "github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"

var _ lipapi.ManagedEventStream = (*promptStream)(nil)
