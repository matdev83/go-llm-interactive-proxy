package speccheck_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKiroSpec(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	filter := strings.TrimSpace(os.Getenv("KIRO_SPEC"))
	specs := registeredSpecs()
	if filter != "" {
		for _, spec := range specs {
			if spec.name == filter {
				spec.check(t, root)
				return
			}
		}
		t.Fatalf("unknown KIRO_SPEC=%q", filter)
	}
	for _, spec := range specs {
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			spec.check(t, root)
		})
	}
}

type kiroSpec struct {
	name  string
	check func(t *testing.T, root string)
}

func registeredSpecs() []kiroSpec {
	return []kiroSpec{{
		name:  "cursor-sdk-backend",
		check: checkCursorSDKBackend,
	}}
}

func checkCursorSDKBackend(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".kiro", "specs", "cursor-sdk-backend")
	requiredFiles := []string{
		"AGENTS.md",
		"requirements.md",
		"design.md",
		"tasks.md",
		"research.md",
		"spec.json",
		"file-plan.md",
		"packaging.md",
		"validation-checklist.md",
	}
	bodies := map[string]string{}
	for _, name := range requiredFiles {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("required artifact missing: %s: %v", name, err)
		}
		bodies[name] = string(b)
	}

	joinedNormative := strings.Join([]string{
		bodies["AGENTS.md"],
		bodies["requirements.md"],
		bodies["design.md"],
		bodies["tasks.md"],
		bodies["file-plan.md"],
		bodies["packaging.md"],
		bodies["validation-checklist.md"],
	}, "\n")
	all := joinedNormative + "\n" + bodies["research.md"] + "\n" + bodies["spec.json"]

	needles := []string{
		"connectors/cursorsdk",
		"pkg/lipsdk/backendplugin",
		"golip.backendplugin.manifest/v1",
		"per_instance",
		"local_only",
		"static",
		"digest",
		"secure local IPC",
		"lazy activation",
		"bridge-node",
		"private_companions",
		"internal/plugins/backends/cursorsdk",
		"@cursor/sdk",
		"package.json",
		"cursorcliacp",
		"process-tree",
		"first content",
		"canonical",
		"agent pool",
	}
	for _, needle := range needles {
		if !strings.Contains(all, needle) {
			t.Fatalf("cursor-sdk-backend specs missing required needle %q", needle)
		}
	}

	mustContain(t, bodies["requirements.md"], "connectors/cursorsdk")
	mustContain(t, bodies["requirements.md"], "pkg/lipsdk/backendplugin")
	mustContain(t, bodies["requirements.md"], "process_sharing: per_instance")
	mustContain(t, bodies["requirements.md"], "first client-visible content")
	mustContain(t, bodies["requirements.md"], "process-tree cleanup")
	mustContain(t, bodies["design.md"], "per_instance")
	mustContain(t, bodies["design.md"], "Secret isolation")
	mustContain(t, bodies["design.md"], "approved secure local IPC")
	mustContain(t, bodies["file-plan.md"], "Root forbidden paths")
	mustContain(t, bodies["packaging.md"], "private_companions")
	mustContain(t, bodies["AGENTS.md"], "make kiro-spec-check SPEC=cursor-sdk-backend")
	mustContain(t, bodies["research.md"], "Phase 8.3")
	mustContain(t, bodies["research.md"], "Recommended Design Direction")
	mustContain(t, bodies["research.md"], "connectors/cursorsdk")
	mustContain(t, bodies["validation-checklist.md"], "make kiro-spec-check SPEC=cursor-sdk-backend")

	normativeNames := []string{
		"AGENTS.md", "requirements.md", "design.md", "tasks.md", "file-plan.md", "packaging.md",
	}
	bannedPhrases := []string{
		"Add `internal/plugins/backends/cursorsdk/`",
		"lives in `internal/plugins/backends/cursorsdk`",
		"implement under `internal/plugins/backends/cursorsdk`",
		"Create `internal/plugins/backends/cursorsdk`",
	}
	for _, name := range normativeNames {
		body := bodies[name]
		for _, bad := range bannedPhrases {
			if strings.Contains(body, bad) {
				t.Fatalf("%s still selects root-tree delivery via %q", name, bad)
			}
		}
	}
	if !strings.Contains(bodies["research.md"], "withdrawn") &&
		!strings.Contains(bodies["research.md"], "superseded") &&
		!strings.Contains(bodies["research.md"], "Superseded") {
		t.Fatal("research.md must mark the old internal path as withdrawn/superseded")
	}
	if strings.Contains(bodies["research.md"], "Add `internal/plugins/backends/cursorsdk/` as a direct") {
		t.Fatal("research.md Recommended Design Direction still prescribes internal/plugins/backends/cursorsdk")
	}

	var meta struct {
		FeatureName            string `json:"feature_name"`
		ReadyForImplementation bool   `json:"ready_for_implementation"`
		Approvals              struct {
			Requirements struct {
				Approved bool `json:"approved"`
			} `json:"requirements"`
			Design struct {
				Approved bool `json:"approved"`
			} `json:"design"`
			Tasks struct {
				Approved bool `json:"approved"`
			} `json:"tasks"`
		} `json:"approvals"`
	}
	if err := json.Unmarshal([]byte(bodies["spec.json"]), &meta); err != nil {
		t.Fatalf("spec.json: %v", err)
	}
	if meta.FeatureName != "cursor-sdk-backend" {
		t.Fatalf("spec.json feature_name=%q", meta.FeatureName)
	}
	if meta.ReadyForImplementation {
		t.Fatal("spec.json ready_for_implementation must stay false until a later product wave")
	}
	if !meta.Approvals.Requirements.Approved || !meta.Approvals.Design.Approved || !meta.Approvals.Tasks.Approved {
		t.Fatal("spec.json Phase 8.3 revalidation must approve requirements, design, and tasks")
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "plugins", "backends", "cursorsdk")); err == nil {
		t.Fatal("forbidden path exists: internal/plugins/backends/cursorsdk")
	}
	rootMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rootMod), "connectors/cursorsdk") {
		t.Fatal("root go.mod must not require/replace connectors/cursorsdk")
	}
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		if strings.Contains(string(b), "@cursor/sdk") {
			t.Fatal("root package.json must not depend on @cursor/sdk")
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func mustContain(t *testing.T, body, needle string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("missing %q", needle)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "cmd", "lipstd")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}
