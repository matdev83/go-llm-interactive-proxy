package backendplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// StageOpenCode builds connectors/opencode into a discovery root with a closed
// manifest exporting both opencode-go and opencode-zen (per_instance).
func StageOpenCode(tb testing.TB) (pluginRoot string) {
	tb.Helper()
	cb, binName := getCachedConnectorBinary(tb, "connectors/opencode", "./cmd/lip-backend-opencode", "lip-backend-opencode")
	root := tb.TempDir()
	rel, digest := stageCachedBinary(tb, cb, root, binName)
	body := fmt.Sprintf(`{
  "schema":"golip.backendplugin.manifest/v1",
  "plugin_id":"io.golip.backend.opencode",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[
    {"kind":"opencode-go","credential_mode":"static","access_scope":"any","process_sharing":"per_instance"},
    {"kind":"opencode-zen","credential_mode":"static","access_scope":"any","process_sharing":"per_instance"}
  ]
}`, rel, digest, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
	return root
}
