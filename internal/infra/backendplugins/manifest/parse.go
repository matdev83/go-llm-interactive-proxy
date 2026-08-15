package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin"
	sdkmanifest "github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/backendplugin/manifest"
)

// ParseStrict decodes a closed v1 manifest document. Every unknown JSON field
// is rejected. No filesystem access or process execution occurs.
func ParseStrict(r io.Reader) (sdkmanifest.Manifest, error) {
	limited := io.LimitReader(r, int64(sdkmanifest.MaxManifestBytes)+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return sdkmanifest.Manifest{}, err
	}
	if len(raw) > sdkmanifest.MaxManifestBytes {
		return sdkmanifest.Manifest{}, fmt.Errorf("%w: file size", sdkmanifest.ErrBoundsExceeded)
	}
	if err := rejectForbiddenKeys(raw); err != nil {
		return sdkmanifest.Manifest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire wireManifest
	if err := dec.Decode(&wire); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return sdkmanifest.Manifest{}, fmt.Errorf("%w: %v", sdkmanifest.ErrUnknownField, err)
		}
		return sdkmanifest.Manifest{}, fmt.Errorf("%w: %v", sdkmanifest.ErrInvalidManifest, err)
	}
	if dec.More() {
		return sdkmanifest.Manifest{}, fmt.Errorf("%w: trailing data", sdkmanifest.ErrInvalidManifest)
	}
	m := wire.toManifest()
	if err := m.Validate(); err != nil {
		return sdkmanifest.Manifest{}, err
	}
	if m.RequiresWindowsEXE() && !strings.HasSuffix(strings.ToLower(path.Base(m.Executable)), ".exe") {
		return sdkmanifest.Manifest{}, fmt.Errorf("%w: windows requires .exe", sdkmanifest.ErrInvalidExecutable)
	}
	return m, nil
}

// ParseStrictBytes is ParseStrict for an in-memory buffer.
func ParseStrictBytes(b []byte) (sdkmanifest.Manifest, error) {
	return ParseStrict(bytes.NewReader(b))
}

var forbiddenKeys = []string{
	"secrets", "secret", "args", "argv", "env", "environment", "hooks", "install",
	"url", "urls", "download", "downloads", "shell", "script", "scripts", "interpreter",
	"command", "commands", "models", "model_catalog", "catalog", "metadata", "config",
	"entrypoint", "cwd", "working_directory",
}

func rejectForbiddenKeys(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("%w: %v", sdkmanifest.ErrInvalidManifest, err)
	}
	for k := range top {
		lk := strings.ToLower(k)
		if slices.Contains(forbiddenKeys, lk) {
			return fmt.Errorf("%w: %s", sdkmanifest.ErrForbiddenField, k)
		}
	}
	return nil
}

type wireManifest struct {
	Schema           string          `json:"schema"`
	PluginID         string          `json:"plugin_id"`
	Version          string          `json:"version"`
	BuildID          string          `json:"build_id"`
	Executable       string          `json:"executable"`
	SHA256           string          `json:"sha256"`
	ProtocolMajor    uint32          `json:"protocol_major"`
	ProtocolMinMinor uint32          `json:"protocol_min_minor"`
	ProtocolMaxMinor uint32          `json:"protocol_max_minor"`
	Platforms        []wirePlatform  `json:"platforms"`
	Exports          []wireExport    `json:"exports"`
	Extensions       []wireExtension `json:"extensions"`
}

type wirePlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type wireExport struct {
	Kind           string `json:"kind"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	CredentialMode string `json:"credential_mode"`
	AccessScope    string `json:"access_scope"`
	ProcessSharing string `json:"process_sharing"`
	ExecutionClass string `json:"execution_class,omitempty"`
	Experimental   bool   `json:"experimental"`
	Deprecated     bool   `json:"deprecated"`
}

type wireExtension struct {
	Name    string `json:"name"`
	Version uint32 `json:"version"`
}

func (w wireManifest) toManifest() sdkmanifest.Manifest {
	m := sdkmanifest.Manifest{
		Schema:           w.Schema,
		PluginID:         strings.TrimSpace(w.PluginID),
		Version:          strings.TrimSpace(w.Version),
		BuildID:          strings.TrimSpace(w.BuildID),
		Executable:       strings.TrimSpace(w.Executable),
		SHA256:           strings.ToLower(strings.TrimSpace(w.SHA256)),
		ProtocolMajor:    w.ProtocolMajor,
		ProtocolMinMinor: w.ProtocolMinMinor,
		ProtocolMaxMinor: w.ProtocolMaxMinor,
	}
	for _, p := range w.Platforms {
		m.Platforms = append(m.Platforms, sdkmanifest.Platform{
			OS: strings.ToLower(strings.TrimSpace(p.OS)), Arch: strings.ToLower(strings.TrimSpace(p.Arch)),
		})
	}
	for _, e := range w.Exports {
		m.Exports = append(m.Exports, sdkmanifest.Export{
			Kind:           strings.TrimSpace(e.Kind),
			DisplayName:    strings.TrimSpace(e.DisplayName),
			Description:    strings.TrimSpace(e.Description),
			CredentialMode: backendplugin.CredentialMode(strings.TrimSpace(e.CredentialMode)),
			AccessScope:    backendplugin.AccessScope(strings.TrimSpace(e.AccessScope)),
			ProcessSharing: backendplugin.ProcessSharing(strings.TrimSpace(e.ProcessSharing)),
			ExecutionClass: lipsdk.BackendExecutionClass(strings.TrimSpace(e.ExecutionClass)),
			Experimental:   e.Experimental,
			Deprecated:     e.Deprecated,
		})
	}
	for _, ext := range w.Extensions {
		m.Extensions = append(m.Extensions, sdkmanifest.Extension{
			Name: strings.TrimSpace(ext.Name), Version: ext.Version,
		})
	}
	return m
}
