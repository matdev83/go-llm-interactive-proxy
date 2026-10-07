// Command lintscope selects local lint packages and their reverse consumers.
// Comprehensive lint remains owned by the unscoped lint target and CI.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/gitscope"
)

type moduleScope struct {
	Directory string   `json:"directory"`
	Packages  []string `json:"packages"`
}

type lintPlan struct {
	Full    bool          `json:"full"`
	Modules []moduleScope `json:"modules"`
}

type listedPackage struct {
	ImportPath                         string
	Dir                                string
	Imports, TestImports, XTestImports []string
	Module                             *struct{ Path, Dir string }
	Error                              *struct{ Err string }
}

func main() {
	mode := flag.String("mode", "changed", "changed or staged local work")
	root := flag.String("root", ".", "repository root")
	format := flag.String("format", "json", "json or lines for shell adapters")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	plan, err := buildLintPlan(ctx, *root, *mode)
	if err == nil {
		switch *format {
		case "json":
			err = json.NewEncoder(os.Stdout).Encode(plan)
		case "lines":
			if plan.Full {
				fmt.Println("FULL")
			} else {
				for _, module := range plan.Modules {
					fmt.Printf("%s\t%s\n", module.Directory, strings.Join(module.Packages, " "))
				}
			}
		default:
			err = fmt.Errorf("invalid output format %q", *format)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint scope:", err)
		os.Exit(1)
	}
}

func commandOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(gitscope.Environ(), "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", name, args, err, stderr.String())
	}
	return out, nil
}

func changedPaths(ctx context.Context, root, mode string) ([]string, error) {
	commands := [][]string{{"diff", "--cached", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRD"}}
	if mode == "changed" {
		commands = append(commands, []string{"diff", "--no-renames", "--name-only", "-z", "--diff-filter=ACMRD"}, []string{"ls-files", "--others", "--exclude-standard", "-z"})
	} else if mode != "staged" {
		return nil, fmt.Errorf("invalid scope mode %q", mode)
	}
	var paths []string
	for _, args := range commands {
		out, err := commandOutput(ctx, root, "git", args...)
		if err != nil {
			return nil, err
		}
		for name := range strings.SplitSeq(string(out), "\x00") {
			if name != "" {
				paths = append(paths, filepath.ToSlash(name))
			}
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func requiresFullLint(name string) bool {
	if isSkillPath(name) {
		return false
	}
	base := path.Base(name)
	if base == "go.mod" || base == "go.sum" || base == "go.work" || base == "go.work.sum" || base == "Makefile" || strings.HasPrefix(base, ".golangci") {
		return true
	}
	for _, prefix := range []string{"pkg/", "connector-support/", "internal/testkit/", "internal/core/config/", "tools/lintscope/", "scripts/lint-all-modules", "scripts/quality-checks"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func isSkillPath(name string) bool {
	for _, prefix := range []string{".agents/", ".codex/", ".cursor/", ".kiro/", ".opencode/", ".pi/"} {
		if strings.HasPrefix(name, prefix+"skills/") {
			return true
		}
	}
	return false
}

func buildLintPlan(ctx context.Context, root, mode string) (lintPlan, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return lintPlan{}, err
	}
	paths, err := changedPaths(ctx, root, mode)
	if err != nil {
		return lintPlan{}, err
	}
	if len(paths) == 0 && mode == "changed" {
		// Preserve the standalone clean-checkout root gate without expanding it
		// to unrelated independent modules.
		return lintPlan{Modules: []moduleScope{{Directory: ".", Packages: []string{"./..."}}}}, nil
	}
	byModule := map[string][]string{}
	for _, name := range paths {
		if requiresFullLint(name) {
			return lintPlan{Full: true}, nil
		}
		if !strings.HasSuffix(name, ".go") || isSkillPath(name) {
			continue
		}
		dir := path.Dir(name)
		module := dir
		for module != "." {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(module), "go.mod")); err == nil {
				break
			}
			module = path.Dir(module)
		}
		byModule[module] = append(byModule[module], dir)
	}
	plan := lintPlan{Modules: []moduleScope{}}
	var modules []string
	for module := range byModule {
		modules = append(modules, module)
	}
	slices.Sort(modules)
	for _, module := range modules {
		moduleRoot := filepath.Join(root, filepath.FromSlash(module))
		out, err := commandOutput(ctx, moduleRoot, "go", "list", "-mod=readonly", "-json", "./...")
		if err != nil {
			return lintPlan{}, err
		}
		decoder := json.NewDecoder(bytes.NewReader(out))
		var graph []listedPackage
		modulePath := ""
		for {
			var pkg listedPackage
			err := decoder.Decode(&pkg)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return lintPlan{}, err
			}
			if pkg.Error != nil {
				return lintPlan{}, fmt.Errorf("package %s: %s", pkg.ImportPath, pkg.Error.Err)
			}
			if pkg.Module != nil && filepath.Clean(pkg.Module.Dir) == filepath.Clean(moduleRoot) {
				modulePath = pkg.Module.Path
				graph = append(graph, pkg)
			}
		}
		if modulePath == "" {
			return lintPlan{Full: true}, nil
		}
		var seeds []string
		for _, dir := range byModule[module] {
			rel, err := filepath.Rel(moduleRoot, filepath.Join(root, filepath.FromSlash(dir)))
			if err != nil {
				return lintPlan{}, err
			}
			seed := modulePath
			if rel != "." {
				seed += "/" + filepath.ToSlash(rel)
			}
			seeds = append(seeds, seed)
		}
		selected := affectedPackages(graph, seeds)
		// Unresolved/deleted package selection must never silently omit evidence.
		if len(selected) == 0 {
			return lintPlan{Full: true}, nil
		}
		var patterns []string
		for _, name := range selected {
			rel := strings.TrimPrefix(name, modulePath)
			if rel == "" {
				patterns = append(patterns, ".")
			} else {
				patterns = append(patterns, "."+rel)
			}
		}
		plan.Modules = append(plan.Modules, moduleScope{Directory: module, Packages: patterns})
	}
	return plan, nil
}

func affectedPackages(graph []listedPackage, seeds []string) []string {
	selected := map[string]bool{}
	for _, seed := range seeds {
		selected[seed] = true
	}
	for changed := true; changed; {
		changed = false
		for _, pkg := range graph {
			if selected[pkg.ImportPath] {
				continue
			}
			imports := append(slices.Clone(pkg.Imports), pkg.TestImports...)
			imports = append(imports, pkg.XTestImports...)
			for _, dependency := range imports {
				if selected[dependency] {
					selected[pkg.ImportPath] = true
					changed = true
					break
				}
			}
		}
	}
	var result []string
	for _, pkg := range graph {
		if selected[pkg.ImportPath] {
			result = append(result, pkg.ImportPath)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}
