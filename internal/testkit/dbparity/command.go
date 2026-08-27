package dbparity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// RunnerMode defines the execution mode of the dbparity runner.
type RunnerMode string

const (
	// ModeList outputs the catalog-derived component and package inventory without executing tests.
	ModeList RunnerMode = "list"
	// ModeSQLite executes the canonical SQLite parity wrappers.
	ModeSQLite RunnerMode = "sqlite"
	// ModePostgresDirect executes the canonical PostgreSQL-direct parity wrappers in fail-closed mode.
	ModePostgresDirect RunnerMode = "postgres-direct"
	// ModeAll executes SQLite parity wrappers followed by PostgreSQL-direct parity wrappers.
	ModeAll RunnerMode = "all"
)

// ValidRunnerModes returns all recognized runner modes.
func ValidRunnerModes() []RunnerMode {
	return []RunnerMode{
		ModeList,
		ModeSQLite,
		ModePostgresDirect,
		ModeAll,
	}
}

// IsValid reports whether the runner mode is recognized.
func (m RunnerMode) IsValid() bool {
	return slices.Contains(ValidRunnerModes(), m)
}

// ParseRunnerMode parses a string into a RunnerMode or returns an actionable error.
func ParseRunnerMode(s string) (RunnerMode, error) {
	mode := RunnerMode(strings.TrimSpace(s))
	if mode.IsValid() {
		return mode, nil
	}
	validNames := make([]string, len(ValidRunnerModes()))
	for i, vm := range ValidRunnerModes() {
		validNames[i] = string(vm)
	}
	return "", fmt.Errorf("unknown runner mode %q: valid modes are %s", s, strings.Join(validNames, ", "))
}

// CommandPlan describes a single executable test command planned by the runner.
type CommandPlan struct {
	ComponentID string   `json:"component_id"`
	Package     string   `json:"package"`
	Backend     string   `json:"backend"`
	Args        []string `json:"args"`
	Env         []string `json:"env,omitempty"`
}

// Cmd creates an *exec.Cmd configured for this plan with normalized child environment variables.
func (p CommandPlan) Cmd(ctx context.Context, baseEnv []string) *exec.Cmd {
	if len(p.Args) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, p.Args[0], p.Args[1:]...)
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	cmd.Env = normalizeEnv(baseEnv, p.Env)
	return cmd
}

// splitEnv splits an environment variable entry into key and value,
// respecting Windows pseudo environment variables that begin with an '=' (e.g. "=C:=C:\repo").
func splitEnv(entry string) (key, val string, ok bool) {
	if strings.HasPrefix(entry, "=") {
		if idx := strings.Index(entry[1:], "="); idx >= 0 {
			return entry[:idx+1], entry[idx+2:], true
		}
		return entry, "", false
	}
	return strings.Cut(entry, "=")
}

// normalizeEnv merges baseEnv with overrides, replacing existing keys (case-insensitively)
// in place, deduplicating keys with latest-wins semantics, and avoiding duplicate variable definitions.
func normalizeEnv(baseEnv, overrides []string) []string {
	if len(baseEnv) == 0 && len(overrides) == 0 {
		return nil
	}

	baseMap := make(map[string]string, len(baseEnv))
	for _, b := range baseEnv {
		k, _, ok := splitEnv(b)
		if !ok {
			k = b
		}
		canonicalKey := strings.ToUpper(strings.TrimSpace(k))
		baseMap[canonicalKey] = b
	}

	overrideMap := make(map[string]string, len(overrides))
	overrideKeys := make([]string, 0, len(overrides))
	for _, o := range overrides {
		k, _, ok := splitEnv(o)
		if !ok {
			k = o
		}
		canonicalKey := strings.ToUpper(strings.TrimSpace(k))
		if _, exists := overrideMap[canonicalKey]; !exists {
			overrideKeys = append(overrideKeys, canonicalKey)
		}
		overrideMap[canonicalKey] = o
	}

	res := make([]string, 0, len(baseMap)+len(overrideKeys))
	emitted := make(map[string]bool, len(baseMap)+len(overrideMap))

	for _, b := range baseEnv {
		k, _, ok := splitEnv(b)
		if !ok {
			k = b
		}
		canonicalKey := strings.ToUpper(strings.TrimSpace(k))
		if emitted[canonicalKey] {
			continue
		}
		if replacement, ok := overrideMap[canonicalKey]; ok {
			res = append(res, replacement)
		} else {
			res = append(res, baseMap[canonicalKey])
		}
		emitted[canonicalKey] = true
	}

	for _, k := range overrideKeys {
		if !emitted[k] {
			res = append(res, overrideMap[k])
			emitted[k] = true
		}
	}

	return res
}

// PlanOptions contains options for planning and executing the dbparity runner.
type PlanOptions struct {
	Catalog     Catalog                   // Parity catalog to use (defaults to DefaultCatalog() if empty)
	GoTestFlags []string                  // Additional flags for `go test` (e.g. -timeout=10m, -parallel=8)
	ComponentID string                    // Optional filter by component ID
	BaseEnv     []string                  // Base environment for DSN/env inspection (defaults to os.Environ() if nil)
	GoBinary    string                    // Optional go binary path (defaults to "go")
	CmdRunner   func(cmd *exec.Cmd) error // Optional test hook to override cmd.Run() execution (defaults to cmd.Run if nil)
}

var (
	dsnURLRegex = regexp.MustCompile(`(?i)(postgres(?:ql)?://)([^:@/\s]*):([^@/\s]+)@`)
	dsnKVRegex  = regexp.MustCompile(`(?i)\b(password=)[^\s"']+(\b|$)`)
)

// RedactDSN scrubs sensitive credentials (passwords in URLs and key-value strings) from output.
func RedactDSN(s string) string {
	s = dsnURLRegex.ReplaceAllString(s, "${1}${2}:***@")
	s = dsnKVRegex.ReplaceAllString(s, "${1}***$2")
	return s
}

// Environment variable names for PostgreSQL DSNs and mandatory parity enforcement.
const (
	EnvTestPostgresDSN      = "LIP_TEST_POSTGRES_DSN"
	EnvTestPostgresAdminDSN = "LIP_TEST_POSTGRES_ADMIN_DSN"
	EnvManagedPostgresDSN   = "LIP_MANAGED_POSTGRES_DSN"
	EnvRequirePostgres      = "LIP_REQUIRE_POSTGRES"
)

// PreflightPostgresDirect checks whether a direct PostgreSQL DSN is configured in baseEnv.
// If baseEnv is nil, os.Environ() is used. Returns an actionable error naming the accepted
// environment variables if no direct DSN exists.
func PreflightPostgresDirect(baseEnv []string) error {
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	hasRuntimeDSN := envLookup(baseEnv, EnvTestPostgresDSN) != "" || envLookup(baseEnv, EnvManagedPostgresDSN) != ""
	adminDSN := envLookup(baseEnv, EnvTestPostgresAdminDSN)
	if !hasRuntimeDSN && adminDSN == "" {
		return fmt.Errorf("postgres-direct parity requires PostgreSQL DSN: set %s, %s, or legacy %s",
			EnvTestPostgresDSN, EnvTestPostgresAdminDSN, EnvManagedPostgresDSN)
	}
	return nil
}

// Plan constructs the sequence of CommandPlans for the requested runner mode.
func Plan(mode RunnerMode, opts PlanOptions) ([]CommandPlan, error) {
	validatedMode, err := ParseRunnerMode(string(mode))
	if err != nil {
		return nil, err
	}

	if validatedMode == ModeList {
		return nil, nil
	}

	cat := opts.Catalog
	if len(cat.Components) == 0 {
		cat = DefaultCatalog()
	}

	goBin := opts.GoBinary
	if strings.TrimSpace(goBin) == "" {
		goBin = "go"
	}

	var components []Component
	if strings.TrimSpace(opts.ComponentID) != "" {
		comp, ok := cat.ComponentByID(strings.TrimSpace(opts.ComponentID))
		if !ok {
			return nil, fmt.Errorf("unknown component %q: valid components are %s", opts.ComponentID, strings.Join(cat.ComponentIDs(), ", "))
		}
		components = []Component{comp}
	} else {
		components = cat.Components
	}

	switch validatedMode {
	case ModeSQLite:
		return planSQLite(components, opts, goBin), nil
	case ModePostgresDirect:
		return planPostgresDirect(components, opts, goBin)
	case ModeAll:
		pgPlans, err := planPostgresDirect(components, opts, goBin)
		if err != nil {
			return nil, err
		}
		sqlitePlans := planSQLite(components, opts, goBin)
		combined := make([]CommandPlan, 0, len(sqlitePlans)+len(pgPlans))
		combined = append(combined, sqlitePlans...)
		combined = append(combined, pgPlans...)
		return combined, nil
	default:
		return nil, fmt.Errorf("unhandled runner mode %q", validatedMode)
	}
}

func planSQLite(components []Component, opts PlanOptions, goBin string) []CommandPlan {
	seenPkgs := make(map[string]bool)
	var plans []CommandPlan

	for _, comp := range components {
		for _, pkg := range comp.TestPackages {
			if seenPkgs[pkg] {
				continue
			}
			seenPkgs[pkg] = true

			formattedPkg := formatPackagePath(pkg)
			args := []string{goBin, "test"}
			if len(opts.GoTestFlags) > 0 {
				args = append(args, opts.GoTestFlags...)
			}
			args = append(args, "-run", "^TestDBParity_SQLite$", "-count=1", formattedPkg)

			plans = append(plans, CommandPlan{
				ComponentID: comp.ID,
				Package:     pkg,
				Backend:     "sqlite",
				Args:        args,
			})
		}
	}

	return plans
}

func planPostgresDirect(components []Component, opts PlanOptions, goBin string) ([]CommandPlan, error) {
	if err := PreflightPostgresDirect(opts.BaseEnv); err != nil {
		return nil, err
	}

	seenPkgs := make(map[string]bool)
	var plans []CommandPlan

	baseEnv := opts.BaseEnv
	if baseEnv == nil {
		baseEnv = os.Environ()
	}

	var pgEnv []string
	pgEnv = append(pgEnv, EnvRequirePostgres+"=1")

	hasRuntimeDSN := envLookup(baseEnv, EnvTestPostgresDSN) != "" || envLookup(baseEnv, EnvManagedPostgresDSN) != ""
	adminDSN := envLookup(baseEnv, EnvTestPostgresAdminDSN)
	if !hasRuntimeDSN && adminDSN != "" {
		pgEnv = append(pgEnv, EnvTestPostgresDSN+"="+adminDSN)
	}

	for _, comp := range components {
		for _, pkg := range comp.TestPackages {
			if seenPkgs[pkg] {
				continue
			}
			seenPkgs[pkg] = true

			formattedPkg := formatPackagePath(pkg)
			args := []string{goBin, "test"}
			if len(opts.GoTestFlags) > 0 {
				args = append(args, opts.GoTestFlags...)
			}
			args = append(args, "-tags=integration", "-run", "^TestDBParity_PostgresDirect$", "-count=1", formattedPkg)

			plans = append(plans, CommandPlan{
				ComponentID: comp.ID,
				Package:     pkg,
				Backend:     "postgres-direct",
				Args:        args,
				Env:         pgEnv,
			})
		}
	}

	return plans, nil
}

func formatPackagePath(pkg string) string {
	clean := filepath.ToSlash(strings.TrimSpace(pkg))
	clean = strings.TrimPrefix(clean, "./")
	return "./" + clean
}

func envLookup(env []string, key string) string {
	targetKey := strings.ToUpper(strings.TrimSpace(key))
	for i := len(env) - 1; i >= 0; i-- {
		k, v, ok := splitEnv(env[i])
		if !ok {
			continue
		}
		if strings.ToUpper(strings.TrimSpace(k)) == targetKey {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ComponentListEntry is a serializable representation of a component's test scope.
type ComponentListEntry struct {
	ID           string   `json:"id"`
	TestPackages []string `json:"test_packages"`
}

// FormatList formats the catalog components and test packages as human-readable text.
func FormatList(cat Catalog) string {
	if len(cat.Components) == 0 {
		cat = DefaultCatalog()
	}
	var sb strings.Builder
	for i, comp := range cat.Components {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("%s:\n", comp.ID))
		sb.WriteString("  TestPackages:\n")
		for _, tp := range comp.TestPackages {
			sb.WriteString(fmt.Sprintf("    - %s\n", tp))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// FormatListJSON formats the catalog components and test packages as formatted JSON.
func FormatListJSON(cat Catalog) (string, error) {
	if len(cat.Components) == 0 {
		cat = DefaultCatalog()
	}
	entries := make([]ComponentListEntry, len(cat.Components))
	for i, comp := range cat.Components {
		entries[i] = ComponentListEntry{
			ID:           comp.ID,
			TestPackages: comp.TestPackages,
		}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ExitCoder is implemented by error types that provide a process exit status code (e.g. *exec.ExitError).
type ExitCoder interface {
	ExitCode() int
}

// RunStepError represents a failure encountered when executing a test step in the runner.
// It wraps the underlying cause (such as *exec.ExitError or context.Canceled) to allow callers
// to inspect the exit code or error type while preserving contextual information (component, package, backend).
type RunStepError struct {
	Component string `json:"component"`
	Package   string `json:"package"`
	Backend   string `json:"backend"`
	Err       error  `json:"-"`
}

// Error formats a human-readable and redacted error description.
func (e *RunStepError) Error() string {
	if e == nil {
		return ""
	}
	errMsg := ""
	if e.Err != nil {
		errMsg = e.Err.Error()
	}
	return fmt.Sprintf("dbparity: test failed for component %q package %q (backend: %s): %s",
		e.Component, e.Package, e.Backend, RedactDSN(errMsg))
}

// Unwrap returns the underlying error cause (e.g. *exec.ExitError, context.Canceled).
func (e *RunStepError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ExitCode returns the exit code of the underlying error.
func (e *RunStepError) ExitCode() int {
	if e == nil {
		return 0
	}
	return MapExitStatus(e.Err)
}

// MapExitStatus determines the process exit status code for a given error.
// - Returns 0 if err is nil.
// - Returns 130 if err is or wraps context.Canceled (standard SIGINT exit code 128+2).
// - Returns the process exit code if err is or wraps an ExitCoder (e.g. *exec.ExitError).
// - Returns 1 for any other non-nil error.
func MapExitStatus(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var coder ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}

// Run executes the dbparity runner for the specified mode, routing output to stdout and stderr.
func Run(ctx context.Context, mode RunnerMode, opts PlanOptions, stdout, stderr io.Writer) error {
	validatedMode, err := ParseRunnerMode(string(mode))
	if err != nil {
		return err
	}

	if validatedMode == ModeList {
		cat := opts.Catalog
		if len(cat.Components) == 0 {
			cat = DefaultCatalog()
		}
		_, err := fmt.Fprintln(stdout, FormatList(cat))
		return err
	}

	plans, err := Plan(validatedMode, opts)
	if err != nil {
		return fmt.Errorf("%s", RedactDSN(err.Error()))
	}

	runner := opts.CmdRunner
	if runner == nil {
		runner = func(cmd *exec.Cmd) error {
			return cmd.Run()
		}
	}

	for _, plan := range plans {
		cmd := plan.Cmd(ctx, opts.BaseEnv)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if runErr := runner(cmd); runErr != nil {
			wrappedErr := runErr
			if ctx.Err() != nil {
				wrappedErr = ctx.Err()
			}
			return &RunStepError{
				Component: plan.ComponentID,
				Package:   plan.Package,
				Backend:   plan.Backend,
				Err:       wrappedErr,
			}
		}
	}

	return nil
}

