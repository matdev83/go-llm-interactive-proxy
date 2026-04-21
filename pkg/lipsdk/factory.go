package lipsdk

import (
	"net/http"

	"gopkg.in/yaml.v3"
)

// BackendBuild is the opaque product of a bundled backend factory; the standard distribution
// asserts *runtime.Backend shapes at composition time (see internal/pluginreg.BuildBackend).
type BackendBuild = any

// BackendFactory builds a backend adapter from opaque per-plugin YAML.
type BackendFactory func(n yaml.Node) (BackendBuild, error)

// FrontendMount registers HTTP routes for one frontend plugin instance.
type FrontendMount func(mux *http.ServeMux, pluginCfg yaml.Node, exec ExecutorView, defaultRoute string, maxRequestBodyBytes int64) error
