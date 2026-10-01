package testscope

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitFixture(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(root, "absent-gitconfig"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/scope\n\ngo 1.26.0\n")
	writeFixture(t, root, "cmd/app/main.go", "package main\nfunc main() {}\n")
	writeFixture(t, root, "base/base.go", "package base\nconst Value = 1\n")
	writeFixture(t, root, "base/base_test.go", "package base\nimport \"testing\"\nfunc TestBase(t *testing.T) {}\n")
	writeFixture(t, root, "consumer/consumer.go", "package consumer\nimport \"example.com/scope/base\"\nconst Value = base.Value\n")
	writeFixture(t, root, "embed/embed.go", "package embed\nimport _ \"embed\"\n//go:embed data.txt\nvar Data string\n")
	writeFixture(t, root, "embed/data.txt", "fixture\n")
	writeFixture(t, root, "connectors/one/go.mod", "module example.com/one\n\ngo 1.26.0\n")
	writeFixture(t, root, "connectors/one/one.go", "package one\n")
	gitFixture(t, root, "init", "-q", "-b", "main")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	gitFixture(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitFixture(t, root, "checkout", "-qb", "feature")
	return root
}

func TestPlanBranchAndAllWorkingChanges(t *testing.T) {
	root := fixtureRepository(t)
	writeFixture(t, root, "cmd/app/main.go", "package main\nfunc main() { println(1) }\n")
	gitFixture(t, root, "add", ".")
	gitFixture(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "branch change")
	writeFixture(t, root, "base/base_test.go", "package base\nimport \"testing\"\nfunc TestBase(t *testing.T) { t.Log(1) }\n")
	gitFixture(t, root, "add", "base/base_test.go")
	writeFixture(t, root, "embed/data.txt", "changed\n")
	writeFixture(t, root, "new/new.go", "package new\n")
	plan, err := Build(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changed) != 4 || plan.Fallback != "" || len(plan.Modules) != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	want := []string{"./base", "./cmd/app", "./embed", "./new"}
	if !reflect.DeepEqual(plan.Modules[0].Packages, want) {
		t.Fatalf("packages=%v want %v", plan.Modules[0].Packages, want)
	}
}

func TestPlanInputsAndFallbacks(t *testing.T) {
	root := fixtureRepository(t)
	for _, tc := range []struct {
		name, path, body string
		packages         []string
		module           string
		full             bool
	}{
		{"cli", "cmd/app/main.go", "package main\nfunc main() { println(2) }\n", []string{"./cmd/app"}, ".", false},
		{"production", "base/base.go", "package base\nconst Value = 2\n", []string{"./base", "./consumer"}, ".", false},
		{"fixture", "base/testdata/input.json", "{}\n", []string{"./base"}, ".", false},
		{"docs", "docs/guide.md", "# Guide\n", nil, "", false},
		{"connector", "connectors/one/one_test.go", "package one\n", []string{"."}, "connectors/one", false},
		{"unknown", "assets/unknown.txt", "input\n", nil, "", true},
		{"shared SDK", "pkg/lipapi/shared.go", "package lipapi\n", nil, "", true},
		{"shared testkit", "internal/testkit/helper.go", "package testkit\n", nil, "", true},
		{"policy", "Makefile", "all:\n", nil, "", true},
		{"shared support", "connector-support/acp/helper.go", "package acp\n", nil, "", true},
		{"dependency", "connectors/one/go.mod", "module example.com/one\n\ngo 1.26.1\n", nil, "", true},
		{"broken graph", "base/broken.go", "package base\nimport _ \"missing.invalid/package\"\n", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(root, filepath.FromSlash(tc.path))
			original, readErr := os.ReadFile(file)
			t.Cleanup(func() {
				if readErr == nil {
					writeFixture(t, root, tc.path, string(original))
				} else if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			})
			writeFixture(t, root, tc.path, tc.body)
			plan, err := Build(context.Background(), root, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.full {
				if plan.Fallback == "" || len(plan.Modules) != 2 || plan.Modules[0].Packages[0] != "./..." {
					t.Fatalf("expected full root and connector fallback: %+v", plan)
				}
				return
			}
			if plan.Fallback != "" {
				t.Fatalf("unexpected fallback: %+v", plan)
			}
			if tc.packages == nil {
				if len(plan.Modules) != 0 {
					t.Fatalf("documentation selected tests: %+v", plan)
				}
				return
			}
			if len(plan.Modules) != 1 || plan.Modules[0].Directory != tc.module || !reflect.DeepEqual(plan.Modules[0].Packages, tc.packages) {
				t.Fatalf("plan=%+v want module=%s packages=%v", plan, tc.module, tc.packages)
			}
		})
	}
}

func TestMissingBaseAndExplicitFull(t *testing.T) {
	root := fixtureRepository(t)
	plan, err := Build(context.Background(), root, Options{Full: true})
	if err != nil || plan.Fallback == "" || len(plan.Modules) != 2 {
		t.Fatalf("full plan=%+v err=%v", plan, err)
	}
	gitFixture(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	plan, err = Build(context.Background(), root, Options{})
	if err != nil || !strings.Contains(plan.Fallback, "base") || len(plan.Modules) != 2 {
		t.Fatalf("missing base plan=%+v err=%v", plan, err)
	}
	if _, err := Build(context.Background(), root, Options{Base: "missing-explicit-base"}); err == nil {
		t.Fatal("accepted invalid explicit base")
	}
}

func TestCleanAndDeletedPackage(t *testing.T) {
	root := fixtureRepository(t)
	plan, err := Build(context.Background(), root, Options{})
	if err != nil || len(plan.Modules) != 0 || len(plan.Changed) != 0 {
		t.Fatalf("clean plan=%+v err=%v", plan, err)
	}
	if err := os.Remove(filepath.Join(root, "cmd/app/main.go")); err != nil {
		t.Fatal(err)
	}
	plan, err = Build(context.Background(), root, Options{})
	if err != nil || plan.Fallback == "" || len(plan.Modules) != 2 {
		t.Fatalf("deleted package plan=%+v err=%v", plan, err)
	}
}

func TestRenameAndValidExplicitBase(t *testing.T) {
	root := fixtureRepository(t)
	gitFixture(t, root, "mv", "cmd/app/main.go", "cmd/app/entry.go")
	plan, err := Build(context.Background(), root, Options{Base: "origin/main"})
	if err != nil || plan.Fallback != "" || len(plan.Changed) != 2 || len(plan.Modules) != 1 || !reflect.DeepEqual(plan.Modules[0].Packages, []string{"./cmd/app"}) {
		t.Fatalf("renamed package plan=%+v err=%v", plan, err)
	}
}
