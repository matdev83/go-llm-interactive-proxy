package lipsdk

import "gopkg.in/yaml.v3"

// PluginKind identifies a plugin family exposed through the composition root.
type PluginKind string

const (
	PluginKindFrontend PluginKind = "frontend"
	PluginKindBackend  PluginKind = "backend"
	PluginKindFeature  PluginKind = "feature"
)

// ConfigPayload keeps plugin-private configuration opaque to the core.
type ConfigPayload struct {
	Node yaml.Node
}

// Registration describes a plugin available to the composition root.
type Registration struct {
	ID      string
	Kind    PluginKind
	Config  ConfigPayload
	Enabled bool
}
