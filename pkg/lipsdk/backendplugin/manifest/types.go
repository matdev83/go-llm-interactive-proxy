package manifest

import (
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
)

// SchemaV1 is the only closed manifest schema identifier accepted in Phase 2.
const SchemaV1 = "golip.backendplugin.manifest/v1"

// Manifest is the public installation metadata document for one plugin artifact.
type Manifest struct {
	Schema           string
	PluginID         string
	Version          string
	BuildID          string
	Executable       string
	SHA256           string
	ProtocolMajor    uint32
	ProtocolMinMinor uint32
	ProtocolMaxMinor uint32
	Platforms        []Platform
	Exports          []Export
	Extensions       []Extension
}

// Platform is one supported OS/architecture pair.
type Platform struct {
	OS   string
	Arch string
}

// Export describes one factory kind exported by the plugin.
type Export struct {
	Kind           string
	DisplayName    string
	Description    string
	CredentialMode backendplugin.CredentialMode
	AccessScope    backendplugin.AccessScope
	ProcessSharing backendplugin.ProcessSharing
	ExecutionClass lipsdk.BackendExecutionClass
	Experimental   bool
	Deprecated     bool
}

// Extension is a versioned, allowlisted metadata block. v1 ships with an empty
// allowlist; non-empty unknown extensions are rejected by the strict parser.
type Extension struct {
	Name    string
	Version uint32
}
