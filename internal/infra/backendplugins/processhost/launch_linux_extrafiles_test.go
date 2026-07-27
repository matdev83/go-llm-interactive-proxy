//go:build linux

package processhost_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	inframanifest "github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/manifest"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/processhost"
	"github.com/matdev83/go-llm-interactive-proxy/internal/infra/backendplugins/trust"
	bpkit "github.com/matdev83/go-llm-interactive-proxy/internal/testkit/backendplugin"
)

// TestLaunch_ExtraFilesDoesNotDisplaceExecutableFD proves channel ExtraFiles[0]
// (child FD 3) does not steal the /proc/self/fd exec target from the verified
// artifact descriptor (historical permission-denied on /proc/self/fd/3).
func TestLaunch_ExtraFilesDoesNotDisplaceExecutableFD(t *testing.T) {
	t.Parallel()
	root := bpkit.StageLocalStub(t)
	raw, err := os.ReadFile(filepath.Join(root, "plugin.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := inframanifest.ParseStrictBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	res := trust.Verify(root, m, trust.VerifyOptions{})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("verify: %+v", res)
	}
	t.Cleanup(func() { _ = res.Artifact.Close() })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

	proc, err := processhost.NewPlatformLauncher().Launch(context.Background(), processhost.LaunchSpec{
		Artifact:   res.Artifact,
		Generation: 1,
		Env:        []string{"PATH=/usr/bin:/bin"},
		ExtraFiles: []*os.File{w},
	})
	if err != nil {
		t.Fatalf("Launch with ExtraFiles: %v", err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	if proc.PID() <= 0 {
		t.Fatal("expected live pid")
	}
}
