package archtest

import (
	"strings"
	"testing"
)

// Task 4.3 production gates certify the data-plane serve boundary after
// RunWithRuntime / App-owned serve deletion (req 3.2-3.3, 4.6-4.7, 8.6-8.7).

func TestTask43_SoleProductionDataPlaneServeAPI(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask43SoleServeAPI, scanTask43SoleServeAPISource)
	ids := collectTask43SoleServeIdentities(got)
	want := []string{"func:" + task43SoleAllowedServeAPI}
	if len(ids) != 1 || ids[0] != want[0] {
		t.Fatalf("Task 4.3: production data-plane serve surface must be exactly %v, got %v\n%s",
			want, ids, formatFindings(got))
	}
}

func TestTask43_NoDeletedServeLifecycleSymbols(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask43DeletedServe, scanTask43DeletedServeSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.3: deleted serve/lifecycle symbols must stay gone (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask43_NoAppOwnedHTTPServeLifecycle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask43AppOwnedServe, scanTask43AppOwnedServeSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.3: runtime.App must not own HTTP serve lifecycle (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask43_SupportedStdhttpTestsAvoidDeletedServeNames(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanTestConvergenceGate(t, root, gateTask43StaleTestNames, scanTask43StaleSupportedTestNamesSource)
	if len(got) > 0 {
		t.Fatalf("Task 4.3 RED: supported stdhttp tests must not advertise deleted RunWithRuntime/NewStandardHandler seams (%d findings):\n%s",
			len(got), formatFindings(got))
	}
}

func TestTask43_ServerGoRetainsOnlyListenAndServeSeam(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got := scanProductionConvergenceGate(t, root, gateTask43DeletedServe, func(filename, src string) ([]convergenceFinding, error) {
		rel := slashPath(filename)
		if rel != "internal/stdhttp/server.go" {
			return nil, nil
		}
		if strings.Contains(src, "RunWithRuntime") || strings.Contains(src, "releaseBuiltResources") || strings.Contains(src, "runClosers") {
			return []convergenceFinding{{
				Gate: gateTask43DeletedServe, Path: rel, Identity: "file:server.go",
				Classification: classDeclaration,
				Detail:         "server.go must not reintroduce deleted RunWithRuntime/closer-release symbols",
			}}, nil
		}
		if !strings.Contains(src, "listenAndServe") {
			return []convergenceFinding{{
				Gate: gateTask43DeletedServe, Path: rel, Identity: "var:listenAndServe",
				Classification: classDeclaration,
				Detail:         "server.go must retain the overridable listenAndServe seam used by RunWithGenerationHost",
			}}, nil
		}
		return nil, nil
	})
	if len(got) > 0 {
		t.Fatalf("Task 4.3: server.go serve seam contract broken:\n%s", formatFindings(got))
	}
}
