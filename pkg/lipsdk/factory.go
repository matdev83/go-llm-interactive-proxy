package lipsdk

import (
	"net/http"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/traffic"
	"gopkg.in/yaml.v3"
)

// BackendBuild is the opaque return type of [BackendFactory]. It aliases any on purpose:
// lipsdk must not import internal/core/runtime (AGENTS.md: core-owned types stay out of stable
// SDK surfaces). Official wiring in internal/pluginreg type-asserts to runtime.Backend at the
// composition root; custom distributions may assert their own concrete backend wrapper instead.
// The alias documents that boundary while keeping registration signatures ergonomic for YAML-only factories.
type BackendBuild = any

// BackendFactory builds a backend adapter from opaque per-plugin YAML.
type BackendFactory func(n yaml.Node) (BackendBuild, error)

// FrontendMountOptions carries runtime wiring for [FrontendMount] beyond the [http.ServeMux].
// Use composite literals with named fields at call sites.
type FrontendMountOptions struct {
	// PluginCfg is the opaque plugin-local YAML subtree for this frontend instance.
	PluginCfg yaml.Node
	// Exec is the runtime execution surface the mounted handler uses to submit canonical calls.
	// Real frontend mounts require a non-nil Exec.
	Exec ExecutorView
	// DefaultRoute is the selector used when the frontend protocol omits a route/header override.
	DefaultRoute string
	// MaxRequestBodyBytes caps inbound HTTP request size. Zero means the frontend should use its
	// own default limit.
	MaxRequestBodyBytes int64
	// TrafficPorts optionally emits client→proxy raw bytes after body read (design §10).
	TrafficPorts traffic.PortBundle
}

// FrontendMount registers HTTP routes for one frontend plugin instance.
type FrontendMount func(mux *http.ServeMux, opts FrontendMountOptions) error
