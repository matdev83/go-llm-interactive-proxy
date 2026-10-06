package archtest

// Task 10.3 certification tests over the shared neutrality sweep declared in
// sessionclassification_consumer_neutrality_test.go.
//
// Three separate claims are certified, because one assertion could not tell them
// apart:
//
//  1. Requirement 1.6/1.8 structural: outside the sanctioned classification
//     plumbing there is NO production reference to session classification at
//     all, so no consumer exists and no future consumer can gate by accident.
//
//  2. Requirement 1.8 bundled-feature neutrality, with a coverage census: every
//     bundled feature package in internal/plugins/features is scanned and clean,
//     and a new feature directory is covered automatically because the census
//     enumerates the tree instead of a frozen list.
//
//  3. Requirement 1.7 advisory-only: every package that owns tool authorization,
//     permission, identity, billing entitlement or routing authority was scanned
//     and holds no reference, so classification cannot by itself grant any of
//     them.
//
// The sweep instrument itself is pinned too: the detector must reject a
// synthetic consumer that reads the classification, a synthetic one that reads
// only a same-named unrelated field, and a synthetic one that reaches the field
// through a pointer and an alias. Without those three controls, a detector that
// reported zero references everywhere would look identical to a clean tree.

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestSessionClassificationHasNoConsumerOutsideSanctionedPlumbing is the
// closed-allowlist form of requirement 1.8: every production file in the repo
// that can observe session classification must be one of the enumerated
// plumbing roles. A new reference anywhere - a bundled feature, an authorization
// package, generic core - fails here instead of silently becoming the baseline.
func TestSessionClassificationHasNoConsumerOutsideSanctionedPlumbing(t *testing.T) {
	sweep := sharedSessionClassificationSweep(t)
	if len(sweep.References) == 0 {
		t.Fatal("no production file references session classification at all; the sanctioned plumbing must still " +
			"project the snapshot, so the sweep itself is broken")
	}

	claimed := make([]string, 0, len(sweep.References))
	for _, ref := range sweep.References {
		roles := sessionClassificationRoleOf(ref.File)
		if len(roles) == 0 {
			t.Errorf("production file %q references session classification (%s) but belongs to no sanctioned role; "+
				"existing features must stay behavior-neutral until their own implementation opts in "+
				"(requirements 1.8, 1.6, 1.7)", ref.File, strings.Join(ref.Reasons, ", "))
			continue
		}
		claimed = append(claimed, ref.File)
		t.Logf("  %-78s %-32s %s", ref.File, strings.Join(roles, "+"), strings.Join(ref.Reasons, ", "))
	}

	// Every declared role must still be reachable. A role whose files were all
	// deleted or renamed would otherwise keep authorizing a name nothing
	// implements any more, which is how an allowlist rots.
	for _, role := range sessionClassificationRoles {
		live := 0
		for _, file := range claimed {
			if slices.Contains(sessionClassificationRoleOf(file), role.Role) {
				live++
			}
		}
		if live == 0 {
			t.Errorf("sanctioned classification role %q (%s) matches no live reference; update the allowlist "+
				"deliberately instead of leaving a dead exemption", role.Role, role.Reason)
		}
	}
	t.Logf("%d production files can observe session classification (%d matched a sanctioned role, "+
		"%d rejected above); %d roles are live",
		len(sweep.References), len(claimed), len(sweep.References)-len(claimed), len(sessionClassificationRoles))
}

// bundledCodingFeatureDirs are the bundled feature directories whose behavior
// requirement 1.8 freezes. They are enumerated from disk rather than from a
// frozen list precisely so that a feature added after this test was written is
// covered by the sweep without anyone remembering to extend the table.
var bundledCodingFeatureDirs = []string{
	"agentloopguard",
	"codexclientcompat",
	"compactioncontinuity",
	"interleavedthinking",
	"keepwarm",
	"partsnoop",
	"prerequestpolicy",
	"reasoningpreservation",
	"refautoappend",
	"refparts",
	"refsubmit",
	"reftool",
	"reftoolpolicy",
	"reftraffictranscript",
	"refverifier",
	"refworkspaceguard",
	"secretguard",
	"submitnoop",
	"toolcallrepair",
	"toolreactornoop",
}

// TestBundledCodingFeaturesHaveNoSessionClassificationDependencyOrGate is the
// structural half of requirement 1.8. Every bundled feature package outside the
// classifier itself must have zero references to session classification: not the
// snapshot, not IsCodingAgent, not the SDK classifier contract, not the feature
// or featurehost classifier packages.
//
// The coverage census is part of the claim. If the sweep silently scanned no
// feature files, "zero references" would be indistinguishable from "not
// checked", so each named bundled feature must contribute at least one scanned
// production file and the whole feature tree must be non-trivially covered.
func TestBundledCodingFeaturesHaveNoSessionClassificationDependencyOrGate(t *testing.T) {
	sweep := sharedSessionClassificationSweep(t)

	discovered := make([]string, 0, len(bundledCodingFeatureDirs))
	entries, err := os.ReadDir(filepath.Join(repoRoot(t), "internal", "plugins", "features"))
	if err != nil {
		t.Fatalf("read the bundled feature tree: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "testdata" {
			continue
		}
		if entry.Name() == "sessionclassification" {
			continue
		}
		discovered = append(discovered, entry.Name())
	}
	sort.Strings(discovered)
	for _, want := range bundledCodingFeatureDirs {
		if !slices.Contains(discovered, want) {
			t.Errorf("bundled feature %q no longer exists under internal/plugins/features; requirement 1.8 "+
				"freezes this feature set", want)
		}
	}
	if len(discovered) < len(bundledCodingFeatureDirs) {
		t.Errorf("the bundled feature tree carries %d features, want at least the %d named ones",
			len(discovered), len(bundledCodingFeatureDirs))
	}

	// Coverage census: every bundled feature package must have been type-checked.
	// This is what makes the zero below a measurement.
	totalScanned := 0
	for _, name := range discovered {
		dir := "internal/plugins/features/" + name
		scanned := sweep.Scanned[dir]
		if scanned == 0 {
			t.Errorf("bundled feature %q contributed no scanned production file; the neutrality sweep did not "+
				"cover it, so its zero-reference result would be vacuous", name)
			continue
		}
		totalScanned += scanned
	}
	if totalScanned < 100 {
		t.Errorf("the neutrality sweep scanned only %d production files across %d bundled features; a census that "+
			"small cannot support a repo-wide neutrality claim", totalScanned, len(discovered))
	}

	for _, name := range discovered {
		dir := "internal/plugins/features/" + name
		files := sweep.filesIn(dir)
		for _, file := range files {
			ref := sessionClassificationReasonFor(sweep, file)
			t.Errorf("bundled coding-oriented feature %q references session classification in %s (%s); "+
				"requirement 1.8 keeps existing features behavior-neutral until their own implementation "+
				"explicitly opts into classification gating", name, file, strings.Join(ref, ", "))
		}
	}

	// The classifier implementation itself is the one sanctioned feature, and it
	// must still be there: a guard that passes because the classifier vanished
	// would certify nothing.
	classifierFiles := sweep.Scanned["internal/plugins/features/sessionclassification"]
	if classifierFiles == 0 {
		t.Error("the classifier feature package contributed no scanned production file; the sweep is blind to it")
	}
	if len(sweep.filesIn("internal/plugins/features/sessionclassification")) == 0 {
		t.Error("the classifier feature package references nothing in session classification; its own dependency " +
			"has disappeared and this sweep would no longer prove a projection exists")
	}
	t.Logf("%d bundled features scanned (%d production files), zero classification references; classifier "+
		"implementation itself is the only sanctioned feature reference", len(discovered), totalScanned)
}

func sessionClassificationReasonFor(sweep sessionClassificationSweep, file string) []string {
	for _, ref := range sweep.References {
		if ref.File == file {
			return ref.Reasons
		}
	}
	return nil
}

// TestSessionClassificationIsAdvisoryOnlyForEveryAuthorityPath is the structural
// form of requirement 1.7. Classification must not by itself authorize a tool,
// grant a permission, establish identity, alter billing entitlement, or override
// routing authority. The strongest available statement of that is not a comment:
// it is that the packages owning those authorities were all scanned and none of
// them can read the classification at all.
func TestSessionClassificationIsAdvisoryOnlyForEveryAuthorityPath(t *testing.T) {
	sweep := sharedSessionClassificationSweep(t)
	for _, dir := range sessionClassificationAuthorityDirs {
		absolute := filepath.Join(repoRoot(t), filepath.FromSlash(dir))
		if _, err := os.Stat(absolute); err != nil {
			t.Errorf("authority package %q does not exist: %v", dir, err)
			continue
		}
		scanned := sweepScannedUnder(sweep, dir)
		if scanned == 0 {
			t.Errorf("authority package %q contributed no scanned production file; its neutrality was never "+
				"checked, so requirement 1.7 would be unproven for it", dir)
			continue
		}
		for _, file := range sweepFilesUnder(sweep, dir) {
			ref := sessionClassificationReasonFor(sweep, file)
			t.Errorf("authority path %q references session classification in %s (%s); classification is advisory "+
				"derived metadata and must not authorize a tool, grant a permission, establish identity, alter "+
				"billing entitlement, or override routing authority (requirement 1.7)", dir, file,
				strings.Join(ref, ", "))
		}
	}
	t.Logf("%d authority-owning packages scanned, none references session classification",
		len(sessionClassificationAuthorityDirs))
}

// sweepScannedUnder counts production files the sweep type-checked at or below
// the given repo-relative directory.
func sweepScannedUnder(sweep sessionClassificationSweep, dir string) int {
	total := 0
	for scannedDir, count := range sweep.Scanned {
		if scannedDir == dir || strings.HasPrefix(scannedDir, dir+"/") {
			total += count
		}
	}
	return total
}

func sweepFilesUnder(sweep sessionClassificationSweep, dir string) []string {
	var out []string
	for _, ref := range sweep.References {
		pkgDir := PackageDirFromRel(ref.File)
		if pkgDir == dir || strings.HasPrefix(pkgDir, dir+"/") {
			out = append(out, ref.File)
		}
	}
	return out
}
