package qa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQAFastPreflight_SupersededCIJobsRemainCancelable(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow ciWorkflow
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			t.Fatal(err)
		}
		for name, job := range workflow.Jobs {
			// Failure aggregation needs always(), but it must not protect a
			// superseded heavy job from workflow-level cancellation. Step-level
			// log/artifact cleanup keeps its separate cancellation semantics.
			if strings.Contains(job.If, "always()") && !strings.HasPrefix(job.If, "always() && !cancelled()") {
				t.Errorf("%s/%s shields superseded execution; use always() && !cancelled()", filepath.Base(path), name)
			}
		}
	}
}

func TestQAFastPreflight_AllGoWorkflowsUseSharedCachePolicy(t *testing.T) {
	t.Parallel()
	var policy map[string]struct {
		Workflow string `json:"workflow"`
		Job      string `json:"job"`
		BuildMiB int    `json:"build_mib"`
	}
	if err := json.Unmarshal([]byte(readRepositoryFile(t, ".github", "actions", "go-cache", "policy.json")), &policy); err != nil {
		t.Fatal(err)
	}
	producers := map[string]int{}
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow ciWorkflow
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			t.Fatal(err)
		}
		for jobName, job := range workflow.Jobs {
			var setupGo, restoreCache, codeQLCache bool
			for _, step := range job.Steps {
				if strings.HasPrefix(step.Uses, "actions/setup-go@") {
					setupGo = true
					if step.With["cache"] != false {
						t.Errorf("%s/%s bypasses shared cache ownership", filepath.Base(path), jobName)
					}
				}
				if strings.HasPrefix(step.Uses, "github/codeql-action/init@") && step.With["dependency-caching"] == true {
					codeQLCache = true
				}
				if step.Uses == "./.github/actions/go-cache" {
					restoreCache = restoreCache || step.With["phase"] == nil || step.With["phase"] == "restore"
					lane, _ := step.With["lane"].(string)
					owner, ok := policy[lane]
					if !ok {
						t.Errorf("unknown cache lane %q in %s", lane, path)
					}
					if step.With["phase"] == "save" {
						producers[lane]++
						if owner.Workflow != workflow.Name || owner.Job != jobName || !strings.Contains(step.If, "success()") {
							t.Errorf("%s/%s cannot publish complete %s baseline", workflow.Name, jobName, lane)
						}
					}
				}
			}
			// These scope-only jobs compile small stdlib probes. CodeQL owns
			// its dependency cache through its supported extraction action.
			identity := filepath.Base(path) + "/" + jobName
			lightweight := identity == "ci.yml/preflight" || identity == "qa.yml/changes"
			codeQL := identity == "codeql.yml/analyze" && codeQLCache
			if setupGo && !restoreCache && !lightweight && !codeQL {
				t.Errorf("%s must restore a registered Go cache lane", identity)
			}
		}
	}
	for lane, owner := range policy {
		if producers[lane] != 1 || owner.BuildMiB < 128 || owner.BuildMiB > 1536 {
			t.Errorf("%s needs one bounded, complete main producer", lane)
		}
	}
}

func TestQAFastPreflight_RemoteCIWorkloadOwnership(t *testing.T) {
	t.Parallel()
	read := func(name string) ciWorkflow {
		t.Helper()
		var workflow ciWorkflow
		if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", name)), &workflow); err != nil {
			t.Fatal(err)
		}
		return workflow
	}
	ci := read("ci.yml")
	if _, ok := ci.Jobs["preflight"]; !ok || slices.Contains(parseCINeeds(ci.Jobs["preflight"].Needs), "db-parity") {
		t.Error("repository preflight must run independently of database parity")
	}
	if !slices.Contains(parseCINeeds(ci.Jobs["repo-hygiene"].Needs), "preflight") {
		t.Error("required Repo hygiene must propagate preflight failures")
	}
	image := ci.Jobs["db-parity"].Services["postgres"].Image
	if !strings.HasPrefix(image, "${{ needs.changes.outputs.test == 'true' && '") || !strings.HasSuffix(image, "' || '' }}") {
		t.Error("unrelated PRs must not start a PostgreSQL service")
	}
	backend := read("backend-plugin-cross-platform.yml")
	if !slices.Contains(parseCINeeds(backend.Jobs["cross-platform-qa"].Needs), "claimed-compilation") || !slices.Contains(parseCINeeds(backend.Jobs["cross-platform-qa"].Needs), "native-evidence") {
		t.Error("existing native matrix checks must require claimed compilation")
	}
	var compileOwned, nativeOwned, compileRequired bool
	for _, step := range backend.Jobs["claimed-compilation"].Steps {
		compileOwned = compileOwned || strings.Contains(step.Run, "-compile-only")
	}
	for _, step := range backend.Jobs["native-evidence"].Steps {
		nativeOwned = nativeOwned || (strings.Contains(step.Run, "make backend-plugin-cross-platform-qa") && step.Env["CROSS_PLATFORM_SKIP_COMPILE"] == "1")
	}
	for _, step := range backend.Jobs["cross-platform-qa"].Steps {
		compileRequired = compileRequired || (step.Env["COMPILE_RESULT"] == "${{ needs.claimed-compilation.result }}" && strings.Contains(step.Run, `test "$COMPILE_RESULT" = success`) && strings.Contains(step.Run, `test "$NATIVE_RESULT" = success`))
	}
	if slices.Contains(parseCINeeds(backend.Jobs["native-evidence"].Needs), "claimed-compilation") {
		t.Error("native execution must not wait for cross-compilation")
	}
	if !compileOwned || !nativeOwned || !compileRequired {
		t.Error("compile and native owners must retain distinct, fail-closed evidence")
	}
	bridge := read("cursor-sdk-platform.yml").Jobs["bridge-node-tests"]
	if bridge.Name != "bridge-node-tests" || bridge.If != "always() && !cancelled()" || !slices.Contains(parseCINeeds(bridge.Needs), "changes") {
		t.Error("required bridge check must keep its independent status and scope dependency")
	}
}
