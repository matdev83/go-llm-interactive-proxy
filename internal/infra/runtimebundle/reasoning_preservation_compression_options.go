package runtimebundle

import (
	"github.com/matdev83/go-llm-interactive-proxy/internal/standardplugins/featurehost"
)

// ReasoningCompressionOptions carries trusted host-provided egress policy and
// MatcherResolver bindings for reasoning preservation/compression (task 6.2).
type ReasoningCompressionOptions = featurehost.ReasoningCompressionOptions
