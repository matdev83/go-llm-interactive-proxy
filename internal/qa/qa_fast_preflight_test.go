//go:build precommit

package qa

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/archtest"
)

const maxStaleReferenceLineBytes = 4 * 1024 * 1024

type kiroLifecycleMetadata struct {
	Phase                  string `json:"phase"`
	ReadyForImplementation bool   `json:"ready_for_implementation"`
	Completed              bool   `json:"completed"`
}

func TestQAFastPreflight_KiroLifecycle(t *testing.T) {
	t.Parallel()

	root := repositoryFile(t)
	errs := validateKiroLifecycle(filepath.Join(root, ".kiro", "specs"))
	errs = append(errs, validateStaleKiroReferences(root)...)
	if len(errs) != 0 {
		t.Fatalf("invalid Kiro lifecycle state:\n%s", strings.Join(errs, "\n"))
	}
}

func TestQAFastPreflight_KiroLifecycleRejectsStaleActiveReference(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeKiroFixture(t, filepath.Join(root, ".kiro", "specs"), "archive/finished", `{"phase":"completed","completed":true}`, "- [x] done")
	path := filepath.Join(root, "internal", "check.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "package internal\n// " + strings.Repeat("x", 128*1024) + " .kiro/specs/finished/spec.json"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := validateStaleKiroReferences(root)
	if joined := strings.Join(errs, "\n"); !strings.Contains(joined, "archived spec finished through stale active path") {
		t.Fatalf("errors %q do not report the stale active-spec reference", joined)
	}
}

func TestQAFastPreflight_ArchitectureBudgets(t *testing.T) {
	t.Parallel()
	root := repositoryFile(t)

	for _, budget := range archtest.CriticalFileBudgets {
		lines, err := archtest.CountFileLines(filepath.Join(root, filepath.FromSlash(budget.Path)))
		if err != nil {
			t.Errorf("%s: %v", budget.Path, err)
			continue
		}
		if lines > budget.Max {
			t.Errorf("%s: measured %d exceeds budget ceiling %d", budget.Path, lines, budget.Max)
		}
	}
	for _, budget := range archtest.LineBudgets {
		lines, err := archtest.CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(budget.Dir)))
		if err != nil {
			t.Errorf("%s: %v", budget.Dir, err)
			continue
		}
		if lines > budget.Max {
			t.Errorf("%s: measured %d exceeds budget ceiling %d", budget.Dir, lines, budget.Max)
		}
	}
}

func TestQAFastPreflight_KiroLifecycleRejectsInvalidArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeKiroFixture(t, root, "archive/finished", `{"phase":"completed","completed":true,"ready_for_implementation":true}`, "- [ ] unfinished")

	errs := validateKiroLifecycle(root)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"ready_for_implementation must be false", "unchecked task"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors %q do not contain %q", joined, want)
		}
	}
}

func validateKiroLifecycle(specsRoot string) []string {
	var errs []string
	active := make(map[string]struct{})

	entries, err := os.ReadDir(specsRoot)
	if err != nil {
		return []string{fmt.Sprintf("read active specs: %v", err)}
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "archive" {
			continue
		}
		active[entry.Name()] = struct{}{}
		metadata, loadErr := loadKiroLifecycle(filepath.Join(specsRoot, entry.Name(), "spec.json"))
		if loadErr != nil {
			errs = append(errs, fmt.Sprintf("active/%s: %v", entry.Name(), loadErr))
			continue
		}
		if metadata.Completed || metadata.Phase == "completed" || metadata.Phase == "superseded" {
			errs = append(errs, fmt.Sprintf("active/%s: completed or superseded specs must be archived", entry.Name()))
		}
	}

	archiveRoot := filepath.Join(specsRoot, "archive")
	archived, err := os.ReadDir(archiveRoot)
	if err != nil {
		errs = append(errs, fmt.Sprintf("read archived specs: %v", err))
		sort.Strings(errs)
		return errs
	}
	for _, entry := range archived {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, duplicated := active[name]; duplicated {
			errs = append(errs, fmt.Sprintf("archive/%s: duplicate active spec exists", name))
		}
		dir := filepath.Join(archiveRoot, name)
		metadata, loadErr := loadKiroLifecycle(filepath.Join(dir, "spec.json"))
		if loadErr != nil {
			errs = append(errs, fmt.Sprintf("archive/%s: %v", name, loadErr))
			continue
		}
		if metadata.Phase != "completed" && metadata.Phase != "superseded" {
			errs = append(errs, fmt.Sprintf("archive/%s: phase %q must be completed or superseded", name, metadata.Phase))
		}
		if !metadata.Completed {
			errs = append(errs, fmt.Sprintf("archive/%s: completed must be true", name))
		}
		if metadata.ReadyForImplementation {
			errs = append(errs, fmt.Sprintf("archive/%s: ready_for_implementation must be false", name))
		}
		if metadata.Phase == "completed" {
			errs = append(errs, uncheckedKiroTasks(dir, name)...)
		}
	}

	sort.Strings(errs)
	return errs
}

func loadKiroLifecycle(path string) (kiroLifecycleMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return kiroLifecycleMetadata{}, err
	}
	var metadata kiroLifecycleMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return kiroLifecycleMetadata{}, err
	}
	return metadata, nil
}

var activeKiroReference = regexp.MustCompile(`\.kiro/specs/([a-z0-9][a-z0-9-]*)/`)

func validateStaleKiroReferences(root string) []string {
	active := directoryNames(filepath.Join(root, ".kiro", "specs"), "archive")
	archived := directoryNames(filepath.Join(root, ".kiro", "specs", "archive"))
	allowedExtensions := map[string]bool{".go": true, ".sh": true, ".ps1": true, ".yml": true, ".yaml": true}
	var errs []string

	for _, tree := range []string{"cmd", "internal", "pkg", "scripts", "tools", ".github"} {
		base := filepath.Join(root, tree)
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				errs = append(errs, fmt.Sprintf("scan %s: %v", path, walkErr))
				return nil
			}
			if entry.IsDir() || !allowedExtensions[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				errs = append(errs, fmt.Sprintf("scan %s: %v", path, err))
				return nil
			}
			scanner := bufio.NewScanner(file)
			scanner.Buffer(nil, maxStaleReferenceLineBytes)
			for line := 1; scanner.Scan(); line++ {
				for _, match := range activeKiroReference.FindAllStringSubmatch(scanner.Text(), -1) {
					name := match[1]
					_, isActive := active[name]
					_, isArchived := archived[name]
					if !isActive && isArchived {
						rel, _ := filepath.Rel(root, path)
						errs = append(errs, fmt.Sprintf("%s:%d references archived spec %s through stale active path", filepath.ToSlash(rel), line, name))
					}
				}
			}
			if err := scanner.Err(); err != nil {
				errs = append(errs, fmt.Sprintf("scan %s: %v", path, err))
			}
			_ = file.Close()
			return nil
		})
	}
	sort.Strings(errs)
	return errs
}

func directoryNames(path string, excluded ...string) map[string]struct{} {
	names := make(map[string]struct{})
	exclusions := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		exclusions[name] = struct{}{}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, skip := exclusions[entry.Name()]; !skip {
				names[entry.Name()] = struct{}{}
			}
		}
	}
	return names
}

func uncheckedKiroTasks(dir, name string) []string {
	file, err := os.Open(filepath.Join(dir, "tasks.md"))
	if err != nil {
		return []string{fmt.Sprintf("archive/%s: read tasks.md: %v", name, err)}
	}
	defer file.Close()

	var errs []string
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		if strings.HasPrefix(strings.TrimSpace(scanner.Text()), "- [ ]") {
			errs = append(errs, fmt.Sprintf("archive/%s: unchecked task at tasks.md:%d", name, line))
		}
	}
	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("archive/%s: scan tasks.md: %v", name, err))
	}
	return errs
}

func writeKiroFixture(t *testing.T, root, name, metadata, tasks string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks.md"), []byte(tasks), 0o600); err != nil {
		t.Fatal(err)
	}
}
