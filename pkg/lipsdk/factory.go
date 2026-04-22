package lipsdk

import (
	"net/http"

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

// FrontendMount registers HTTP routes for one frontend plugin instance.
type FrontendMount func(mux *http.ServeMux, pluginCfg yaml.Node, exec ExecutorView, defaultRoute string, maxRequestBodyBytes int64) error
