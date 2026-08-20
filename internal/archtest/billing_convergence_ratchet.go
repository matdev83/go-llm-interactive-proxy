package archtest

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
)

// BillingFinalConvergenceDenominatorMeasurement is the recomputed denominator
// breakdown for deterministic reproducibility checks.
type BillingFinalConvergenceDenominatorMeasurement struct {
	RootLines             int
	FileLines             int
	DeclarationLines      int
	DenominatorLOC        int
	Roots                 []BillingFinalConvergenceRootMeasurement
	Files                 []BillingFinalConvergenceFileMeasurement
	SymbolDeclarations    []BillingFinalConvergenceDeclaration
	ExecutorConfigMissing []string
}

type BillingFinalConvergenceRootMeasurement struct {
	ID            string
	Path          string
	BaselineLines int
	CurrentLines  int
}

type BillingFinalConvergenceFileMeasurement struct {
	ID            string
	Path          string
	BaselineLines int
	CurrentLines  int
}

type billingFinalConvergenceSpan struct {
	file       string
	start, end int
}

// MeasureBillingFinalConvergenceDenominator recomputes the physical-go-lines-v1
// denominator from the artifact inventory against the pinned Git commit SHA.
func MeasureBillingFinalConvergenceDenominator(root string, doc BillingFinalConvergenceBaselineFile) (BillingFinalConvergenceDenominatorMeasurement, error) {
	fs, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		return BillingFinalConvergenceDenominatorMeasurement{}, fmt.Errorf("load git commit FS at %s: %w", doc.BaselineSHA, err)
	}
	return MeasureBillingFinalConvergenceDenominatorFS(fs, doc)
}

// MeasureBillingFinalConvergenceCurrentDenominator measures the denominator against the working tree.
func MeasureBillingFinalConvergenceCurrentDenominator(root string, doc BillingFinalConvergenceBaselineFile) (BillingFinalConvergenceDenominatorMeasurement, error) {
	fs := &workingTreeFS{root: root}
	return MeasureBillingFinalConvergenceDenominatorFS(fs, doc)
}

// MeasureBillingFinalConvergenceDenominatorFS recomputes the physical-go-lines-v1
// denominator from the artifact inventory against the given FS.
func MeasureBillingFinalConvergenceDenominatorFS(fs archtestFS, doc BillingFinalConvergenceBaselineFile) (BillingFinalConvergenceDenominatorMeasurement, error) {
	m := BillingFinalConvergenceDenominatorMeasurement{}
	for _, r := range doc.IncludedRoots {
		n, err := CountBillingFinalConvergenceRootLinesFS(fs, r, doc.ExcludedGlobs)
		if err != nil {
			return m, fmt.Errorf("root %s: %w", r.ID, err)
		}
		m.Roots = append(m.Roots, BillingFinalConvergenceRootMeasurement{ID: r.ID, Path: r.Path, BaselineLines: r.BaselineLines, CurrentLines: n})
		m.RootLines += n
	}
	for _, f := range doc.IncludedFiles {
		n, err := CountBillingFinalConvergenceFileLinesFS(fs, f.Path)
		if err != nil {
			return m, fmt.Errorf("file %s: %w", f.ID, err)
		}
		m.Files = append(m.Files, BillingFinalConvergenceFileMeasurement{ID: f.ID, Path: f.Path, BaselineLines: f.BaselineLines, CurrentLines: n})
		m.FileLines += n
	}
	symbolDecls, err := ComputeBillingFinalConvergenceSymbolInventoryFS(fs, doc)
	if err != nil {
		return m, err
	}
	m.SymbolDeclarations = symbolDecls
	var spans []billingFinalConvergenceSpan
	for _, d := range symbolDecls {
		spans = append(spans, billingFinalConvergenceSpan{file: d.File, start: d.StartLine, end: d.EndLine})
	}
	for _, d := range doc.IncludedDeclarations {
		if d.Cause != "executor-config" {
			continue
		}
		info, ok, err := billingFinalConvergenceFindExecutorConfigDeclFS(fs, d)
		if err != nil {
			return m, err
		}
		if !ok {
			m.ExecutorConfigMissing = append(m.ExecutorConfigMissing, d.File+":"+d.Name)
			continue
		}
		spans = append(spans, billingFinalConvergenceSpan{file: d.File, start: info.startLine, end: info.endLine})
	}
	m.DeclarationLines = billingFinalConvergenceUnionSpanLines(spans)
	m.DenominatorLOC = m.RootLines + m.FileLines + m.DeclarationLines
	return m, nil
}

func billingFinalConvergenceFindExecutorConfigDeclFS(fs archtestFS, d BillingFinalConvergenceDeclaration) (billingFinalConvergenceDeclInfo, bool, error) {
	src, err := fs.ReadFile(d.File)
	if err != nil {
		if os.IsNotExist(err) {
			return billingFinalConvergenceDeclInfo{}, false, nil
		}
		return billingFinalConvergenceDeclInfo{}, false, err
	}
	fset, f, err := ParseGoSource(d.File, src)
	if err != nil {
		return billingFinalConvergenceDeclInfo{}, false, fmt.Errorf("%s: %w", d.File, err)
	}
	for _, di := range billingFinalConvergenceFileDecls(fs, fset, f, d.File) {
		for _, n := range di.names {
			if n == d.Name && di.kind == d.Kind {
				return di, true, nil
			}
		}
	}
	return billingFinalConvergenceDeclInfo{}, false, nil
}

func billingFinalConvergenceUnionSpanLines(spans []billingFinalConvergenceSpan) int {
	byFile := make(map[string]map[int]struct{})
	for _, s := range spans {
		set, ok := byFile[s.file]
		if !ok {
			set = make(map[int]struct{})
			byFile[s.file] = set
		}
		for i := s.start; i <= s.end; i++ {
			set[i] = struct{}{}
		}
	}
	total := 0
	for _, set := range byFile {
		total += len(set)
	}
	return total
}

// BillingFinalConvergenceDeletionTargetPresent reports whether a recorded
// deletion target still exists in the working tree.
func BillingFinalConvergenceDeletionTargetPresent(root string, target BillingFinalConvergenceDeletionTarget) (bool, error) {
	fs := &workingTreeFS{root: root}
	return BillingFinalConvergenceDeletionTargetPresentFS(fs, target)
}

func BillingFinalConvergenceDeletionTargetPresentFS(fs archtestFS, target BillingFinalConvergenceDeletionTarget) (bool, error) {
	switch target.Kind {
	case "type", "const", "func", "method":
		return billingFinalConvergenceDeclTargetPresentFS(fs, target)
	case "field", "ident", "schema":
		return billingFinalConvergenceMarkerTargetPresentFS(fs, target)
	default:
		return false, fmt.Errorf("%s: unknown deletion target kind %q", target.ID, target.Kind)
	}
}

func billingFinalConvergenceDeclTargetPresentFS(fs archtestFS, target BillingFinalConvergenceDeletionTarget) (bool, error) {
	if target.Package == "" || target.Name == "" {
		return false, fmt.Errorf("%s: package and name required", target.ID)
	}
	kind := SymbolType
	switch target.Kind {
	case "const":
		kind = SymbolConst
	case "func":
		kind = SymbolFunc
	case "method":
		kind = SymbolMethod
	}
	rule := ForbiddenDeclRule{Package: target.Package, Kind: kind, Name: target.Name}
	entries, err := fs.ReadDir(target.Package)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if isBillingFinalConvergenceMigrationName(entry.Name()) {
			continue
		}
		relPath := filepath.ToSlash(filepath.Join(target.Package, entry.Name()))
		src, rerr := fs.ReadFile(relPath)
		if rerr != nil {
			return false, rerr
		}
		_, f, perr := ParseGoSource(relPath, src)
		if perr != nil {
			return false, perr
		}
		if DeclExists(f, rule) {
			return true, nil
		}
	}
	return false, nil
}

func billingFinalConvergenceMarkerTargetPresentFS(fs archtestFS, target BillingFinalConvergenceDeletionTarget) (bool, error) {
	if target.Marker == "" {
		return false, fmt.Errorf("%s: marker required for kind %q", target.ID, target.Kind)
	}
	allow := make(map[string]struct{}, len(target.HistoricalReaders))
	for _, evidence := range target.HistoricalReaders {
		rel := evidence
		if separator := strings.LastIndexByte(evidence, ':'); separator >= 0 {
			rel = evidence[:separator]
		}
		allow[filepath.ToSlash(rel)] = struct{}{}
	}
	scanFiles := target.Files
	if target.Kind == "schema" {
		expanded, err := billingFinalConvergenceExpandPackageFilesFS(fs, scanFiles)
		if err != nil {
			return false, err
		}
		scanFiles = expanded
	}
	for _, rel := range scanFiles {
		rel = filepath.ToSlash(rel)
		if _, ok := allow[rel]; ok {
			continue
		}
		src, err := fs.ReadFile(rel)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if target.Kind == "schema" && isBillingFinalConvergenceMigrationName(filepath.Base(rel)) {
			continue
		}
		if target.Kind == "field" {
			_, fileAST, err := ParseGoSource(rel, src)
			if err != nil {
				return false, err
			}
			found := false
			ast.Inspect(fileAST, func(node ast.Node) bool {
				field, ok := node.(*ast.Field)
				if !ok {
					return true
				}
				for _, name := range field.Names {
					if name.Name == target.Name {
						found = true
						return false
					}
				}
				return true
			})
			if found {
				return true, nil
			}
			continue
		}
		if strings.Contains(string(src), target.Marker) {
			return true, nil
		}
	}
	return false, nil
}

func billingFinalConvergenceExpandPackageFilesFS(fs archtestFS, files []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{})
	for _, rel := range files {
		rel = filepath.ToSlash(rel)
		dir := filepath.ToSlash(filepath.Dir(rel))
		entries, err := fs.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			if isBillingFinalConvergenceMigrationName(entry.Name()) {
				continue
			}
			p := filepath.ToSlash(filepath.Join(dir, entry.Name()))
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out, nil
}

// isBillingFinalConvergenceMigrationName reports whether a file is a timestamped
// Bun migration source (14-digit prefix), treated as historical-only DDL.
func isBillingFinalConvergenceMigrationName(name string) bool {
	if len(name) < 15 || name[14] != '_' {
		return false
	}
	for i := range 14 {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

// EvaluateBillingFinalConvergenceDeletionRatchet checks the planned deletion
// inventory at the baseline SHA commit.
func EvaluateBillingFinalConvergenceDeletionRatchet(root string, doc BillingFinalConvergenceBaselineFile) ([]RuleFinding, error) {
	fs, err := loadGitCommitFS(root, doc.BaselineSHA)
	if err != nil {
		return nil, fmt.Errorf("load git commit FS at %s: %w", doc.BaselineSHA, err)
	}
	return EvaluateBillingFinalConvergenceDeletionRatchetFS(fs, doc, false)
}

// EvaluateBillingFinalConvergenceCurrentDeletionRatchet checks the planned or active deletion
// inventory on the working tree.
func EvaluateBillingFinalConvergenceCurrentDeletionRatchet(root string, doc BillingFinalConvergenceBaselineFile) ([]RuleFinding, error) {
	fs := &workingTreeFS{root: root}
	return EvaluateBillingFinalConvergenceDeletionRatchetFS(fs, doc, true)
}

// EvaluateBillingFinalConvergenceDeletionRatchetFS checks the deletion targets.
// If isCurrentTree is true, planned ratchets do not enforce presence in the current working tree.
// If isCurrentTree is false (baseline validation), planned ratchets enforce presence.
func EvaluateBillingFinalConvergenceDeletionRatchetFS(fs archtestFS, doc BillingFinalConvergenceBaselineFile, isCurrentTree bool) ([]RuleFinding, error) {
	var out []RuleFinding
	deletionRatchetActive := false
	for _, r := range doc.PlannedRatchets {
		if r.ID == "structural_deletion" {
			deletionRatchetActive = r.Status == BillingFinalConvergenceRatchetActive
		}
	}

	for _, target := range doc.DeletionTargets {
		found, err := BillingFinalConvergenceDeletionTargetPresentFS(fs, target)
		if err != nil {
			return nil, err
		}

		if isCurrentTree {
			if deletionRatchetActive || target.Status == BillingFinalConvergenceRatchetActive {
				if target.Present {
					out = append(out, RuleFinding{
						Rule:   "billing_final_convergence_deletion_activated",
						Path:   target.ID,
						Detail: "activated ratchet must record present=false in baseline metadata",
					})
				}
				if found {
					out = append(out, RuleFinding{
						Rule:   "billing_final_convergence_deletion_activated",
						Path:   target.ID,
						Detail: "deletion target must be absent in current source after activation: " + target.Name,
					})
				}
			}
		} else {
			if target.Status == BillingFinalConvergenceRatchetActive {
				// The pinned Phase-0 tree is intentionally the immutable brownfield
				// evidence. Activation retires the target from the current tree; it
				// must not rewrite or pretend to alter that historical snapshot.
				if target.Present {
					out = append(out, RuleFinding{
						Rule:   "billing_final_convergence_deletion_activated",
						Path:   target.ID,
						Detail: "activated ratchet must record present=false",
					})
				}
			} else {
				if !target.Present {
					out = append(out, RuleFinding{
						Rule:   "billing_final_convergence_deletion_planned",
						Path:   target.ID,
						Detail: "planned ratchet requires present=true until the deletion ratchet is activated in 7.2",
					})
				}
				if !found {
					out = append(out, RuleFinding{
						Rule:   "billing_final_convergence_deletion_planned",
						Path:   target.ID,
						Detail: "planned deletion target missing from baseline SHA source: " + target.Name,
					})
				}
			}
		}
	}
	return out, nil
}

// EvaluateBillingFinalConvergenceLOCRatchet checks the denominator lock. While
// the LOC ratchet is not activated (Phase 0), the recomputed denominator must
// equal the recorded denominator_loc. Activation (Phase 7.2) requires
// final LOC <= floor(denominator_loc * 0.90).
func EvaluateBillingFinalConvergenceLOCRatchet(doc BillingFinalConvergenceBaselineFile, measured BillingFinalConvergenceDenominatorMeasurement) []RuleFinding {
	if !billingFinalConvergenceLOCRatchetActive(doc) {
		if measured.DenominatorLOC != doc.DenominatorLOC {
			return []RuleFinding{{
				Rule: "billing_final_convergence_loc_lock",
				Detail: fmt.Sprintf("recomputed denominator %d != locked baseline %d (Phase 7.2 will require a >=10%% reduction vs this baseline)",
					measured.DenominatorLOC, doc.DenominatorLOC),
			}}
		}
		return nil
	}
	ceiling := doc.DenominatorLOC * 90 / 100
	if measured.DenominatorLOC > ceiling {
		return []RuleFinding{{
			Rule: "billing_final_convergence_loc_reduction",
			Detail: fmt.Sprintf("final LOC %d exceeds 10%% reduction ceiling %d (baseline %d)",
				measured.DenominatorLOC, ceiling, doc.DenominatorLOC),
		}}
	}
	return nil
}

func billingFinalConvergenceLOCRatchetActive(doc BillingFinalConvergenceBaselineFile) bool {
	for _, r := range doc.PlannedRatchets {
		if r.ID == "loc_reduction" {
			return r.Status == BillingFinalConvergenceRatchetActive
		}
	}
	return false
}

// ValidateBillingFinalConvergencePlannedRatchets checks the checked-in
// activation lock: both final ratchets exist, are activated at task 7.2, and
// retain their immutable activation flags.
func ValidateBillingFinalConvergencePlannedRatchets(doc BillingFinalConvergenceBaselineFile) []RuleFinding {
	want := []struct {
		id   string
		task string
		flag string
	}{
		{"structural_deletion", "7.2", BillingFinalConvergenceActivationDeletionRatchet},
		{"loc_reduction", "7.2", BillingFinalConvergenceActivationLOCRatchet},
	}
	byID := make(map[string]BillingFinalConvergencePlannedRatchet, len(doc.PlannedRatchets))
	for _, r := range doc.PlannedRatchets {
		byID[r.ID] = r
	}
	var out []RuleFinding
	for _, w := range want {
		r, ok := byID[w.id]
		if !ok {
			out = append(out, RuleFinding{Rule: "billing_final_convergence_ratchet_inventory", Path: w.id, Detail: "missing planned ratchet"})
			continue
		}
		if r.Status != BillingFinalConvergenceRatchetActive {
			out = append(out, RuleFinding{Rule: "billing_final_convergence_ratchet_inventory", Path: w.id, Detail: fmt.Sprintf("status=%q, want %q (Phase 7.2 activates final gates)", r.Status, BillingFinalConvergenceRatchetActive)})
		}
		if r.ActivationTask != w.task {
			out = append(out, RuleFinding{Rule: "billing_final_convergence_ratchet_inventory", Path: w.id, Detail: fmt.Sprintf("activation_task=%q, want %q", r.ActivationTask, w.task)})
		}
		if r.ActivationFlag != w.flag {
			out = append(out, RuleFinding{Rule: "billing_final_convergence_ratchet_inventory", Path: w.id, Detail: fmt.Sprintf("activation_flag=%q, want %q", r.ActivationFlag, w.flag)})
		}
		if strings.TrimSpace(r.EndState) == "" {
			out = append(out, RuleFinding{Rule: "billing_final_convergence_ratchet_inventory", Path: w.id, Detail: "end_state must document the activated end-state forbid"})
		}
	}
	return out
}
