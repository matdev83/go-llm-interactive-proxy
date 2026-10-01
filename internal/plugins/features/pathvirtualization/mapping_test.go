package pathvirtualization_test

import (
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/pathvirtualization"
)

// reservedNamespaceV1 is the fixed, non-operator-configurable V1 alias namespace
// marker from design.md 159. The test spells it literally rather than reusing an
// unexported package constant so a rename cannot silently follow the code.
const reservedNamespaceV1 = ".__lip_v1__"

// workspaceTagChars is the exact length of the V1 workspace identity tag:
// 96 digest bits encode as exactly 20 unpadded base32 characters (design.md 175).
const workspaceTagChars = 20

// goldenVector pins one full derivation: the supported real root, its flavor, the
// 20-character workspace tag, and the exact V1 virtual-root spelling (design.md
// 177-182). The expected tag and alias were computed independently of the
// implementation from design.md 161-182:
//
//	digest = SHA-256("lip:path-virtualization:v1\x00" + flavorID + "\x00" + canonicalRootIdentity)
//	workspaceTag = lower-case base32-no-padding(digest[0:12])
//
// Hardcoded constants are what make these vectors a process-recreation proof: a
// freshly built proxy that read no prior state must reproduce the same bytes.
type goldenVector struct {
	name         string
	root         string
	wantFlavor   pathvirtualization.PathFlavor
	wantTag      string
	wantVirtual  string
	wantRealRoot string
	wantActive   bool
}

func goldenVectors() []goldenVector {
	return []goldenVector{
		{
			name:         "posix_long_workspace",
			root:         `/home/dev/projects/go-llm-interactive-proxy`,
			wantFlavor:   pathvirtualization.FlavorPOSIX,
			wantTag:      "ylfucd77chy74zh3qwma",
			wantVirtual:  `/.__lip_v1__/w_ylfucd77chy74zh3qwma/`,
			wantRealRoot: `/home/dev/projects/go-llm-interactive-proxy`,
			wantActive:   true,
		},
		{
			name:         "windows_drive_long_workspace",
			root:         `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "ilcrzjze5qdqueaesaxa",
			wantVirtual:  `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa\`,
			wantRealRoot: `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			wantActive:   true,
		},
		{
			name:         "windows_unc_long_workspace",
			root:         `\\build01\team\source\repos\go-llm-interactive-proxy`,
			wantFlavor:   pathvirtualization.FlavorWindowsUNC,
			wantTag:      "ffs2lbqb5oquzqw4nz5a",
			wantVirtual:  `\\.__lip_v1__\w_ffs2lbqb5oquzqw4nz5a\`,
			wantRealRoot: `\\build01\team\source\repos\go-llm-interactive-proxy`,
			wantActive:   true,
		},
		{
			name:         "windows_extended_drive_long_workspace",
			root:         `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedDrive,
			wantTag:      "xlocyx2rtgrjkuj4cr3q",
			wantVirtual:  `\\?\C:\.__lip_v1__\w_xlocyx2rtgrjkuj4cr3q\`,
			wantRealRoot: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			wantActive:   true,
		},
		{
			name:         "windows_extended_unc_long_workspace",
			root:         `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedUNC,
			wantTag:      "nmlkx2hd77peyqhynboa",
			wantVirtual:  `\\?\UNC\.__lip_v1__\w_nmlkx2hd77peyqhynboa\`,
			wantRealRoot: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			wantActive:   true,
		},
		// Roots too short for their alias: the gate at design.md 184 keeps the
		// derived tag observable but leaves virtualization inactive.
		{
			name:         "posix_short_root_is_inactive",
			root:         `/a/b`,
			wantFlavor:   pathvirtualization.FlavorPOSIX,
			wantTag:      "v4vm5apmwzenoijignaq",
			wantRealRoot: `/a/b`,
		},
		{
			name:         "drive_short_root_is_inactive",
			root:         `C:\x`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "izye4warfhfi2nafoxfq",
			wantRealRoot: `C:\x`,
		},
		{
			name:         "posix_anchor_is_inactive",
			root:         `/`,
			wantFlavor:   pathvirtualization.FlavorPOSIX,
			wantTag:      "7fvfk2o7mw2tdqoxkaqa",
			wantRealRoot: `/`,
		},
		{
			name:         "drive_volume_root_is_inactive",
			root:         `C:\`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "i3uqsdoakxppcvab5p4a",
			wantRealRoot: `C:\`,
		},
		{
			name:         "unc_volume_root_is_inactive",
			root:         `\\build01\share`,
			wantFlavor:   pathvirtualization.FlavorWindowsUNC,
			wantTag:      "p6vps2ysggpwc6pzyzza",
			wantRealRoot: `\\build01\share`,
		},
		{
			name:         "extended_drive_volume_root_is_inactive",
			root:         `\\?\C:\`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedDrive,
			wantTag:      "blss3niyb32ip7jftvza",
			wantRealRoot: `\\?\C:\`,
		},
		{
			name:         "extended_unc_volume_root_is_inactive",
			root:         `\\?\UNC\build01\share`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedUNC,
			wantTag:      "dsf734zgsqnko7s3b3ca",
			wantRealRoot: `\\?\UNC\build01\share`,
		},
		// A UNC volume already ends with its share name, so the separator that
		// follows a bare share is a non-volume trailing separator and is removed.
		// The two spellings of one workspace must share the frozen V1 tag, so these
		// tags repeat the bare-share values above; they were computed from
		// design.md 161-182, not read from the implementation.
		{
			name:         "unc_volume_root_with_trailing_separator_is_inactive",
			root:         `\\build01\share\`,
			wantFlavor:   pathvirtualization.FlavorWindowsUNC,
			wantTag:      "p6vps2ysggpwc6pzyzza",
			wantRealRoot: `\\build01\share\`,
		},
		{
			name:         "extended_unc_volume_root_with_trailing_separator_is_inactive",
			root:         `\\?\UNC\build01\share\`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedUNC,
			wantTag:      "dsf734zgsqnko7s3b3ca",
			wantRealRoot: `\\?\UNC\build01\share\`,
		},
		{
			// A drive volume is spelled `C:`, which names no root on its own, so
			// its boundary separator belongs to the volume and survives the trim.
			name:         "drive_volume_root_with_repeated_separator_is_inactive",
			root:         `C:\\`,
			wantFlavor:   pathvirtualization.FlavorWindowsDrive,
			wantTag:      "i3uqsdoakxppcvab5p4a",
			wantRealRoot: `C:\\`,
		},
		{
			name:         "extended_drive_volume_root_with_repeated_separator_is_inactive",
			root:         `\\?\C:\\`,
			wantFlavor:   pathvirtualization.FlavorWindowsExtendedDrive,
			wantTag:      "blss3niyb32ip7jftvza",
			wantRealRoot: `\\?\C:\\`,
		},
	}
}

// TestDeriveMappingGoldenVectors pins the exact V1 contract: one deterministic
// virtual root per project root, without any prior state.
func TestDeriveMappingGoldenVectors(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason, pathvirtualization.SkipReasonNone)
			}
			if got.Flavor != tc.wantFlavor {
				t.Errorf("flavor = %v, want %v", got.Flavor, tc.wantFlavor)
			}
			if got.RealRoot != tc.wantRealRoot {
				t.Errorf("real root = %q, want %q", got.RealRoot, tc.wantRealRoot)
			}
			if got.WorkspaceTag != tc.wantTag {
				t.Errorf("workspace tag = %q, want %q", got.WorkspaceTag, tc.wantTag)
			}
			if got.VirtualRoot != tc.wantVirtual {
				t.Errorf("virtual root = %q, want %q", got.VirtualRoot, tc.wantVirtual)
			}
			if (got.VirtualRoot != "") != tc.wantActive {
				t.Errorf("active = %v (virtual root %q), want %v", got.VirtualRoot != "", got.VirtualRoot, tc.wantActive)
			}
		})
	}
}

// TestDeriveMappingIsStatelessAndDeterministic proves requirement 6.2: the same
// supported root derives the same tag and virtual root on every derivation, with
// no stored dictionary, so retries, races, reloads, and restarts converge.
func TestDeriveMappingIsStatelessAndDeterministic(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			first, firstReason := pathvirtualization.DeriveMapping(tc.root)
			for range 8 {
				again, againReason := pathvirtualization.DeriveMapping(tc.root)
				if again != first || againReason != firstReason {
					t.Fatalf("derivation is not stable for %q: %+v/%q then %+v/%q",
						tc.root, first, firstReason, again, againReason)
				}
			}
			if first.WorkspaceTag != tc.wantTag || first.VirtualRoot != tc.wantVirtual {
				t.Fatalf("derivation drifted from the golden vector for %q: tag=%q virtual=%q",
					tc.root, first.WorkspaceTag, first.VirtualRoot)
			}
		})
	}
}

// TestWorkspaceTagIsExactlyTwentyLowerCaseBase32Characters pins the tag encoding
// from design.md 175: exactly 96 digest bits rendered as 20 unpadded base32
// characters with no padding and no upper-case leakage.
func TestWorkspaceTagIsExactlyTwentyLowerCaseBase32Characters(t *testing.T) {
	t.Parallel()

	const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	roots := []string{
		`/home/dev/projects/go-llm-interactive-proxy`,
		`C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`\\build01\team\source\repos\go-llm-interactive-proxy`,
		`\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
		`/a/b`,
		`C:\`,
		`\\?\UNC\build01\share`,
		`//weird/root/with/odd/bytes/` + reservedNamespaceV1,
	}
	for _, root := range roots {
		got, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if len(got.WorkspaceTag) != workspaceTagChars {
			t.Errorf("DeriveMapping(%q) tag = %q has %d characters, want %d", root, got.WorkspaceTag, len(got.WorkspaceTag), workspaceTagChars)
		}
		for i := range len(got.WorkspaceTag) {
			if !strings.ContainsRune(base32Alphabet, rune(got.WorkspaceTag[i])) {
				t.Errorf("DeriveMapping(%q) tag = %q holds non lower-case-base32 byte %q at %d",
					root, got.WorkspaceTag, got.WorkspaceTag[i], i)
			}
		}
	}
}

// canonicalIdentityCase pins which spellings of one workspace root must collapse
// onto a single tag and which must not (design.md 161-166). The first block is
// the Windows separator/case normalization plus non-volume trailing separator
// removal; the second block is POSIX case sensitivity; the third block is the
// retained flavor identity, including normal versus extended Windows forms.
func TestCanonicalRootIdentity(t *testing.T) {
	t.Parallel()

	sameTag := []struct {
		name   string
		a, b   string
		flavor pathvirtualization.PathFlavor
	}{
		{
			name:   "drive_separator_style_is_normalized",
			a:      `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:      `C:/Users/dev/source/repos/go-llm-interactive-proxy`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "drive_ascii_case_is_folded",
			a:      `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:      `c:\USERS\DEV\source\repos\GO-LLM-Interactive-Proxy`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "drive_trailing_separator_is_removed",
			a:      `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:      `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "drive_volume_root_trailing_separator_is_removed",
			a:      `C:\`,
			b:      `C:\\`,
			flavor: pathvirtualization.FlavorWindowsDrive,
		},
		{
			name:   "unc_trailing_separator_is_removed",
			a:      `\\build01\team\source\repos\go-llm-interactive-proxy`,
			b:      `\\build01\team\source\repos\go-llm-interactive-proxy\`,
			flavor: pathvirtualization.FlavorWindowsUNC,
		},
		{
			// A UNC volume already ends with its share name, so the separator that
			// follows a bare `\\server\share` is a non-volume trailing separator and
			// is removed just like the one that follows a subdirectory.
			name:   "unc_volume_root_trailing_separator_is_removed",
			a:      `\\build01\share`,
			b:      `\\build01\share\`,
			flavor: pathvirtualization.FlavorWindowsUNC,
		},
		{
			name:   "unc_forward_separator_volume_root_is_normalized",
			a:      `\\build01\share`,
			b:      `\\build01/share/`,
			flavor: pathvirtualization.FlavorWindowsUNC,
		},
		{
			name:   "extended_drive_separator_style_is_normalized",
			a:      `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:      `\\?\C:/Users/dev/source/repos/go-llm-interactive-proxy`,
			flavor: pathvirtualization.FlavorWindowsExtendedDrive,
		},
		{
			name:   "extended_drive_trailing_separator_is_removed",
			a:      `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:      `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			flavor: pathvirtualization.FlavorWindowsExtendedDrive,
		},
		{
			name:   "extended_drive_volume_root_trailing_separator_is_removed",
			a:      `\\?\C:\`,
			b:      `\\?\C:\\`,
			flavor: pathvirtualization.FlavorWindowsExtendedDrive,
		},
		{
			name:   "extended_unc_marker_case_is_folded",
			a:      `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			b:      `\\?\unc\build01\team\source\repos\go-llm-interactive-proxy`,
			flavor: pathvirtualization.FlavorWindowsExtendedUNC,
		},
		{
			// The extended UNC marker is followed by the same `server\share` volume
			// spelling, so the same non-volume trailing separator rule applies.
			name:   "extended_unc_volume_root_trailing_separator_is_removed",
			a:      `\\?\UNC\build01\share`,
			b:      `\\?\UNC\build01\share\`,
			flavor: pathvirtualization.FlavorWindowsExtendedUNC,
		},
		{
			name:   "posix_trailing_separator_is_removed",
			a:      `/home/dev/projects/go-llm-interactive-proxy`,
			b:      `/home/dev/projects/go-llm-interactive-proxy/`,
			flavor: pathvirtualization.FlavorPOSIX,
		},
	}
	for _, tc := range sameTag {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, aReason := pathvirtualization.DeriveMapping(tc.a)
			b, bReason := pathvirtualization.DeriveMapping(tc.b)
			if aReason != pathvirtualization.SkipReasonNone || bReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("unexpected skip reasons: %q/%q", aReason, bReason)
			}
			if a.Flavor != tc.flavor || b.Flavor != tc.flavor {
				t.Fatalf("flavors = %v/%v, want %v", a.Flavor, b.Flavor, tc.flavor)
			}
			if a.WorkspaceTag != b.WorkspaceTag {
				t.Errorf("tags differ for equivalent spellings: %q -> %q, %q -> %q",
					tc.a, a.WorkspaceTag, tc.b, b.WorkspaceTag)
			}
			if a.VirtualRoot != b.VirtualRoot {
				t.Errorf("virtual roots differ for equivalent spellings: %q vs %q", a.VirtualRoot, b.VirtualRoot)
			}
		})
	}

	differentTag := []struct {
		name string
		a, b string
	}{
		{
			name: "posix_is_case_sensitive",
			a:    `/home/dev/projects/go-llm-interactive-proxy`,
			b:    `/home/dev/Projects/go-llm-interactive-proxy`,
		},
		{
			name: "dot_segments_are_never_resolved",
			a:    `/home/dev/projects/go-llm-interactive-proxy`,
			b:    `/home/dev/projects/../projects/go-llm-interactive-proxy`,
		},
		{
			name: "drive_and_extended_drive_are_distinct_flavors",
			a:    `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:    `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		},
		{
			name: "unc_and_extended_unc_are_distinct_flavors",
			a:    `\\build01\team\source\repos\go-llm-interactive-proxy`,
			b:    `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
		},
		{
			name: "drive_letter_is_part_of_identity",
			a:    `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			b:    `D:\Users\dev\source\repos\go-llm-interactive-proxy`,
		},
		{
			name: "unc_server_is_part_of_identity",
			a:    `\\build01\team\source\repos\go-llm-interactive-proxy`,
			b:    `\\build02\team\source\repos\go-llm-interactive-proxy`,
		},
		{
			name: "different_directories_are_distinct",
			a:    `/home/dev/projects/go-llm-interactive-proxy`,
			b:    `/home/dev/projects/other-repository`,
		},
		{
			name: "non_ascii_bytes_never_fold",
			a:    `/home/dev/projects/straße-repository`,
			b:    `/home/dev/projects/STRAẞE-repository`,
		},
	}
	for _, tc := range differentTag {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, aReason := pathvirtualization.DeriveMapping(tc.a)
			b, bReason := pathvirtualization.DeriveMapping(tc.b)
			if aReason != pathvirtualization.SkipReasonNone || bReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("unexpected skip reasons: %q/%q", aReason, bReason)
			}
			if a.WorkspaceTag == b.WorkspaceTag {
				t.Errorf("tags collide for distinct roots: %q and %q both derive %q", tc.a, tc.b, a.WorkspaceTag)
			}
		})
	}
}

// TestDistinctRootsDeriveDistinctTags is the fixture-level half of requirement
// 6.2: across the supported flavors, different project roots never share a tag.
func TestDistinctRootsDeriveDistinctTags(t *testing.T) {
	t.Parallel()

	roots := []string{
		`/home/dev/projects/go-llm-interactive-proxy`,
		`/home/dev/projects/other-repository`,
		`/srv/shared/workspace-one`,
		`/srv/shared/workspace-two`,
		`C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`D:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`C:\Users\dev\source\repos\other-repository`,
		`\\build01\team\source\repos\go-llm-interactive-proxy`,
		`\\build02\team\source\repos\go-llm-interactive-proxy`,
		`\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`\\?\D:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
		`\\?\UNC\build01\other\source\repos\go-llm-interactive-proxy`,
	}
	seen := make(map[string]string, len(roots))
	for _, root := range roots {
		got, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if previous, duplicated := seen[got.WorkspaceTag]; duplicated {
			t.Errorf("roots %q and %q both derive workspace tag %q", previous, root, got.WorkspaceTag)
			continue
		}
		seen[got.WorkspaceTag] = root
	}
}

// TestDeriveMappingRequiresStrictlyShorterAlias pins design.md 184 and
// requirements 1.4/9.1: virtualization stays inactive whenever the complete
// virtual root is not strictly shorter than the real root it would replace, and
// whenever it is not strictly shorter than the prefix that is actually matched.
//
// The two lengths diverge when a supported root is spelled with a trailing
// separator run, so a root can be long enough while its matched prefix is not.
func TestDeriveMappingRequiresStrictlyShorterAlias(t *testing.T) {
	t.Parallel()

	// posixAliasLen is the exact length of a POSIX V1 alias
	// (`/.__lip_v1__/w_<20 chars>/`), used to place roots on both sides of the
	// requirement 9.1 boundary. A matched prefix of exactly this length would
	// leave every rewritten path the same size, so the gate must be strict.
	const posixAliasLen = 36
	// matchedPrefixLen is the length of the root that prefix matching actually
	// replaces: trailing separators above the spelled volume are trimmed first.
	// It equals the real root's length for any root not spelled with a trailing
	// separator, and is shorter otherwise.
	equalLengthRoot := "/" + strings.Repeat("a", posixAliasLen-1)
	oneByteShorterRoot := "/" + strings.Repeat("a", posixAliasLen)

	cases := []struct {
		name       string
		root       string
		wantActive bool
	}{
		{name: "posix_long_enough", root: `/home/dev/projects/go-llm-interactive-proxy`, wantActive: true},
		{name: "posix_too_short", root: `/a/b`},
		{name: "posix_anchor", root: `/`},
		{name: "posix_trailing_separator_run_is_inactive", root: `/ab` + strings.Repeat(`/`, 35)},
		{name: "posix_equal_length_matched_prefix_is_inactive", root: equalLengthRoot + `/`},
		{name: "posix_equal_length_root_is_inactive", root: equalLengthRoot},
		{name: "posix_one_byte_savings_is_active", root: oneByteShorterRoot, wantActive: true},
		{name: "drive_long_enough", root: `C:\Users\dev\source\repos\go-llm-interactive-proxy`, wantActive: true},
		{name: "drive_too_short", root: `C:\x`},
		{name: "drive_volume_root", root: `C:\`},
		{name: "drive_trailing_separator_run_is_inactive", root: `C:` + strings.Repeat(`\`, 100)},
		{name: "unc_long_enough", root: `\\build01\team\source\repos\go-llm-interactive-proxy`, wantActive: true},
		{name: "unc_volume_root", root: `\\build01\share`},
		{name: "unc_trailing_separator_run_is_inactive", root: `\\build01\share` + strings.Repeat(`\`, 30)},
		{name: "extended_drive_long_enough", root: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`, wantActive: true},
		{name: "extended_drive_volume_root", root: `\\?\C:\`},
		{name: "extended_drive_trailing_separator_run_is_inactive", root: `\\?\C:` + strings.Repeat(`\`, 100)},
		{name: "extended_unc_long_enough", root: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`, wantActive: true},
		{name: "extended_unc_volume_root", root: `\\?\UNC\build01\share`},
		{name: "extended_unc_trailing_separator_run_is_inactive", root: `\\?\UNC\build01\share` + strings.Repeat(`\`, 30)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q, want %q", reason, pathvirtualization.SkipReasonNone)
			}
			if got.VirtualRoot == "" {
				if tc.wantActive {
					t.Fatalf("mapping for %q is inactive; its alias must be shorter", tc.root)
				}
				return
			}
			if !tc.wantActive {
				t.Fatalf("mapping for %q derived alias %q that is not shorter than the real root %q",
					tc.root, got.VirtualRoot, got.RealRoot)
			}
			if len(got.VirtualRoot) >= len(got.RealRoot) {
				t.Fatalf("active mapping for %q has alias length %d >= real root length %d",
					tc.root, len(got.VirtualRoot), len(got.RealRoot))
			}
			if len(got.VirtualRoot) < len(reservedNamespaceV1) {
				t.Fatalf("derived alias %q is suspiciously short", got.VirtualRoot)
			}
			// Requirement 9.1: the active replacement must shorten a path of the
			// root's own spelling plus a separator, so the alias is provably
			// strictly shorter than the prefix that is matched.
			child, changed := got.VirtualizePath(tc.root + `/probe`)
			if !changed {
				t.Fatalf("active mapping for %q refused to virtualize its own child", tc.root)
			}
			if len(child) >= len(tc.root)+len(`/probe`) {
				t.Fatalf("virtualized child %q is not shorter than the real path for %q", child, tc.root)
			}
		})
	}
}

// TestMappingInactivityIsDistinguishableFromRejection pins the three-way state a
// caller can observe from DeriveMapping, so no state is ever guessed:
//
//  1. an unusable root: a bounded SkipReason and a zero Mapping;
//  2. a usable root whose alias is not beneficial: no SkipReason, the derived
//     WorkspaceTag is observable, and VirtualRoot is empty;
//  3. a usable root whose alias is beneficial: no SkipReason, WorkspaceTag, and
//     VirtualRoot.
//
// Requirements 1.4 and 1.8 are satisfied by states 1 and 2: state 2 only requires
// that the path is left unchanged. Requirement 1.8's bounded-reason duty belongs
// to state 1, and requirement 7.6's skip-reason duty for state 2 is owned by the
// rewrite-mode task, which must report an empty VirtualRoot as a bounded reason
// instead of counting it as a saving.
func TestMappingInactivityIsDistinguishableFromRejection(t *testing.T) {
	t.Parallel()

	// State 1: unusable roots, distinguishable only by their bounded reason.
	rejected := []struct {
		name       string
		root       string
		wantReason pathvirtualization.SkipReason
	}{
		{name: "empty", root: "", wantReason: pathvirtualization.SkipReasonEmptyRoot},
		{name: "relative", root: `repos\go-llm-interactive-proxy`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "drive_relative", root: `C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "device_namespace", root: `\\.\PIPE\lip`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
	}
	for _, tc := range rejected {
		t.Run("rejected_"+tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != tc.wantReason {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason, tc.wantReason)
			}
			if got != (pathvirtualization.Mapping{}) {
				t.Fatalf("rejected root %q returned %+v, want a zero Mapping", tc.root, got)
			}
			if got.VirtualRoot != "" || got.WorkspaceTag != "" {
				t.Fatalf("rejected root %q derived a tag or alias: %+v", tc.root, got)
			}
		})
	}

	// State 2: usable roots whose alias is not beneficial. No reason is reported
	// because nothing was skipped for an unusable root.
	usableButInactive := []struct {
		name string
		root string
	}{
		{name: "posix_subdirectory", root: `/a/b`},
		{name: "posix_anchor", root: `/`},
		{name: "drive_subdirectory", root: `C:\x`},
		{name: "drive_volume_root", root: `C:\`},
		{name: "unc_volume_root", root: `\\build01\share`},
		{name: "extended_drive_volume_root", root: `\\?\C:\`},
		{name: "extended_unc_volume_root", root: `\\?\UNC\build01\share`},
		// Roots long enough to beat their own length but not the prefix that is
		// actually matched, so the alias would never shorten a path.
		{name: "posix_trailing_separator_run", root: "/ab" + strings.Repeat("/", 35)},
		{name: "drive_trailing_separator_run", root: `C:` + strings.Repeat(`\`, 100)},
	}
	for _, tc := range usableButInactive {
		t.Run("usable_inactive_"+tc.name, func(t *testing.T) {
			t.Parallel()

			root := tc.root
			got, reason := pathvirtualization.DeriveMapping(root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q for a usable root", root, reason, pathvirtualization.SkipReasonNone)
			}
			if got.RealRoot != root {
				t.Fatalf("RealRoot = %q, want the original spelling %q", got.RealRoot, root)
			}
			if len(got.WorkspaceTag) != workspaceTagChars {
				t.Errorf("usable but inactive root %q derived tag %q; the identity stays observable",
					root, got.WorkspaceTag)
			}
			if got.VirtualRoot != "" {
				t.Fatalf("usable but inactive root %q derived alias %q", root, got.VirtualRoot)
			}
			// Requirement 1.4: no alias means the path is left unchanged.
			path := root + `/src/main.go`
			if virtual, changed := got.VirtualizePath(path); changed || virtual != path {
				t.Errorf("inactive mapping rewrote %q to %q (changed=%v)", path, virtual, changed)
			}
			if expanded, result := got.ExpandPath(path); result != pathvirtualization.ExpandResultNotApplicable || expanded != path {
				t.Errorf("inactive mapping expanded %q to %q (%v)", path, expanded, result)
			}
		})
	}

	// State 3: usable roots with a beneficial alias.
	for _, root := range goldenVectors() {
		if !root.wantActive {
			continue
		}
		t.Run("usable_active_"+root.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(root.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q, want %q", reason, pathvirtualization.SkipReasonNone)
			}
			if got.VirtualRoot == "" {
				t.Fatalf("usable root %q derived no alias", root.root)
			}
			if len(got.WorkspaceTag) != workspaceTagChars {
				t.Errorf("root %q derived tag %q", root.root, got.WorkspaceTag)
			}
		})
	}
}

// TestDeriveMappingRejectsUnsupportedRoots keeps requirement 1.8 honest: an
// unusable root yields a bounded reason code and a zero mapping rather than a
// guess. Reserved-namespace collision handling belongs to task 2.3.
func TestDeriveMappingRejectsUnsupportedRoots(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		root       string
		wantReason pathvirtualization.SkipReason
	}{
		{name: "empty", root: "", wantReason: pathvirtualization.SkipReasonEmptyRoot},
		{name: "relative", root: `relative\path`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "posix_relative", root: `home/dev/project`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "rooted_without_volume", root: `\Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "drive_relative", root: `C:`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "drive_relative_with_body", root: `C:Users\dev`, wantReason: pathvirtualization.SkipReasonRelativeRoot},
		{name: "unc_without_share", root: `\\build01`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "unc_empty_share", root: `\\build01\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "device_pipe", root: `\\.\PIPE\lip`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
		{name: "extended_drive_relative_body", root: `\\?\C:com1`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_empty", root: `\\?\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_unc_without_share", root: `\\?\UNC\build01\`, wantReason: pathvirtualization.SkipReasonMalformedVolumeRoot},
		{name: "extended_global_root", root: `\\?\GLOBALROOT\Device\HarddiskVolume1\src`, wantReason: pathvirtualization.SkipReasonDeviceNamespace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != tc.wantReason {
				t.Fatalf("DeriveMapping(%q) reason = %q, want %q", tc.root, reason, tc.wantReason)
			}
			if got != (pathvirtualization.Mapping{}) {
				t.Fatalf("rejected root returned %+v, want a zero Mapping", got)
			}
		})
	}
}

// TestVirtualRootKeepsTheRealRootFlavor proves the alias is usable as a path of
// the same absolute form: every derived virtual root reclassifies into the same
// flavor as the real root it replaces, which is what the reserved-alias flavor
// comparison in task 2.3 will rely on.
func TestVirtualRootKeepsTheRealRootFlavor(t *testing.T) {
	t.Parallel()

	for _, tc := range goldenVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q", reason)
			}
			if got.VirtualRoot == "" {
				// An inactive mapping derives no alias; task 2.3 owns the
				// reserved-namespace collision checks.
				if tc.wantVirtual != "" {
					t.Fatalf("mapping for %q is inactive, want alias %q", tc.root, tc.wantVirtual)
				}
				return
			}
			if !strings.Contains(got.VirtualRoot, reservedNamespaceV1) {
				t.Fatalf("virtual root %q does not use the fixed V1 reserved namespace %q",
					got.VirtualRoot, reservedNamespaceV1)
			}
			if !strings.Contains(got.VirtualRoot, got.WorkspaceTag) {
				t.Fatalf("virtual root %q does not embed the workspace tag %q", got.VirtualRoot, got.WorkspaceTag)
			}
			parsed, parsedReason := pathvirtualization.ClassifyPath(got.VirtualRoot)
			if parsedReason != pathvirtualization.SkipReasonNone {
				t.Fatalf("ClassifyPath(%q) rejected the derived alias with %q", got.VirtualRoot, parsedReason)
			}
			if parsed.Flavor != got.Flavor {
				t.Errorf("alias %q reclassifies as %v, want the real root flavor %v",
					got.VirtualRoot, parsed.Flavor, got.Flavor)
			}
			if parsed.Root+parsed.Rest != got.VirtualRoot {
				t.Errorf("alias %q is not reproduced byte-exactly from %q + %q",
					got.VirtualRoot, parsed.Root, parsed.Rest)
			}
		})
	}
}

// TestRealRootKeepsItsOriginalSpelling pins design.md 152: the mapping stores the
// root exactly as spelled so expansion can reconstruct it. Case, separator style,
// and dot segments are never rewritten.
func TestRealRootKeepsItsOriginalSpelling(t *testing.T) {
	t.Parallel()

	roots := []string{
		`C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`C:/Users/dev/source/repos/go-llm-interactive-proxy/`,
		`c:\USERS\DEV\source\repos\GO-LLM-Interactive-Proxy`,
		`\\build01\team\source\repos\go-llm-interactive-proxy\`,
		`\\?\C:/Users/dev/source/repos/go-llm-interactive-proxy`,
		`\\?\unc/build01/team/source/repos/go-llm-interactive-proxy`,
		`/home/dev/projects/../projects/go-llm-interactive-proxy`,
		`/home/dev/projects/go-llm-interactive-proxy/`,
	}
	for _, root := range roots {
		got, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		if got.RealRoot != root {
			t.Errorf("RealRoot = %q, want the original spelling %q", got.RealRoot, root)
		}
	}
}

// virtualizeCase is one row of the segment-boundary and matching-semantics table.
type virtualizeCase struct {
	name        string
	root        string
	path        string
	wantPath    string
	wantChanged bool
}

func virtualizeCases() []virtualizeCase {
	return []virtualizeCase{
		{
			name:        "posix_rewrites_root_prefix",
			root:        `/home/dev/projects/go-llm-interactive-proxy`,
			path:        `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
			wantPath:    `/.__lip_v1__/w_ylfucd77chy74zh3qwma/src/main.go`,
			wantChanged: true,
		},
		{
			name:        "posix_rewrites_the_bare_root",
			root:        `/home/dev/projects/go-llm-interactive-proxy`,
			path:        `/home/dev/projects/go-llm-interactive-proxy`,
			wantPath:    `/.__lip_v1__/w_ylfucd77chy74zh3qwma/`,
			wantChanged: true,
		},
		{
			name:        "posix_trailing_separator_root_matches_once",
			root:        `/home/dev/projects/go-llm-interactive-proxy/`,
			path:        `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
			wantPath:    `/.__lip_v1__/w_ylfucd77chy74zh3qwma/src/main.go`,
			wantChanged: true,
		},
		{
			name:     "posix_is_case_sensitive",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/home/dev/Projects/go-llm-interactive-proxy/src/main.go`,
			wantPath: `/home/dev/Projects/go-llm-interactive-proxy/src/main.go`,
		},
		{
			name:     "posix_backslash_is_a_file_name_byte",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/home/dev/projects/go-llm-interactive-proxy\other/main.go`,
			wantPath: `/home/dev/projects/go-llm-interactive-proxy\other/main.go`,
		},
		{
			name:     "posix_mid_segment_prefix_is_refused",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/home/dev/projects/go-llm-interactive-proxyX/src/main.go`,
			wantPath: `/home/dev/projects/go-llm-interactive-proxyX/src/main.go`,
		},
		{
			name:     "posix_shorter_prefix_is_refused",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/home/dev/projects/go-llm-interactive-prox`,
			wantPath: `/home/dev/projects/go-llm-interactive-prox`,
		},
		{
			name:     "posix_other_workspace_is_refused",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/home/dev/projects/other-repository/src/main.go`,
			wantPath: `/home/dev/projects/other-repository/src/main.go`,
		},
		{
			name:     "posix_inactive_mapping_never_rewrites",
			root:     `/a/b`,
			path:     `/a/b/src/main.go`,
			wantPath: `/a/b/src/main.go`,
		},
		{
			name:        "drive_rewrites_root_prefix",
			root:        `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:        `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath:    `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa\src\main.go`,
			wantChanged: true,
		},
		{
			name:        "drive_rewrites_the_bare_root",
			root:        `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:        `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			wantPath:    `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa\`,
			wantChanged: true,
		},
		{
			name:        "drive_folds_ascii_case_and_separators",
			root:        `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:        `c:/users/DEV/source/repos/go-llm-interactive-proxy/src/main.go`,
			wantPath:    `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa/src/main.go`,
			wantChanged: true,
		},
		{
			name:     "drive_mid_segment_prefix_is_refused",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `C:\Users\dev\source\repos\go-llm-interactive-proxyX\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\go-llm-interactive-proxyX\src\main.go`,
		},
		{
			name:     "drive_other_drive_is_refused",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `D:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath: `D:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:     "drive_inactive_mapping_never_rewrites",
			root:     `C:\x`,
			path:     `C:\x\src\main.go`,
			wantPath: `C:\x\src\main.go`,
		},
		{
			name:        "unc_rewrites_root_prefix",
			root:        `\\build01\team\source\repos\go-llm-interactive-proxy`,
			path:        `\\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath:    `\\.__lip_v1__\w_ffs2lbqb5oquzqw4nz5a\src\main.go`,
			wantChanged: true,
		},
		{
			name:        "unc_folds_case_and_separators",
			root:        `\\build01\team\source\repos\go-llm-interactive-proxy`,
			path:        `\\BUILD01/team/source/repos/go-llm-interactive-proxy/src/main.go`,
			wantPath:    `\\.__lip_v1__\w_ffs2lbqb5oquzqw4nz5a/src/main.go`,
			wantChanged: true,
		},
		{
			name:     "unc_other_server_is_refused",
			root:     `\\build01\team\source\repos\go-llm-interactive-proxy`,
			path:     `\\build02\team\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath: `\\build02\team\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:        "extended_drive_rewrites_root_prefix",
			root:        `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:        `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath:    `\\?\C:\.__lip_v1__\w_xlocyx2rtgrjkuj4cr3q\src\main.go`,
			wantChanged: true,
		},
		{
			name:     "extended_drive_refuses_the_plain_drive_spelling",
			root:     `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:        "extended_unc_rewrites_root_prefix",
			root:        `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			path:        `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath:    `\\?\UNC\.__lip_v1__\w_nmlkx2hd77peyqhynboa\src\main.go`,
			wantChanged: true,
		},
		{
			name:     "extended_unc_refuses_the_plain_unc_spelling",
			root:     `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			path:     `\\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantPath: `\\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
	}
}

// TestVirtualizePathMatchSemantics pins requirement 1.6/1.7 and design.md 148-151:
// POSIX matches case-sensitively on `/`, Windows matches ASCII case-insensitively
// with both separators, and every replacement ends on a segment boundary.
func TestVirtualizePathMatchSemantics(t *testing.T) {
	t.Parallel()

	for _, tc := range virtualizeCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			got, changed := mapping.VirtualizePath(tc.path)
			if got != tc.wantPath {
				t.Errorf("VirtualizePath(%q) = %q, want %q", tc.path, got, tc.wantPath)
			}
			if changed != tc.wantChanged {
				t.Errorf("VirtualizePath(%q) changed = %v, want %v", tc.path, changed, tc.wantChanged)
			}
			if !changed && got != tc.path {
				t.Errorf("VirtualizePath(%q) rewrote the path to %q while reporting no change", tc.path, got)
			}
		})
	}
}

// TestVirtualizePathIsIdempotent proves requirement 2.9 at the mapper level: a
// second outbound pass over an already virtualized path changes nothing.
func TestVirtualizePathIsIdempotent(t *testing.T) {
	t.Parallel()

	seeds := []struct {
		root string
		seed string
	}{
		{
			root: `/home/dev/projects/go-llm-interactive-proxy`,
			seed: `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
		},
		{
			root: `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			seed: `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			root: `\\build01\team\source\repos\go-llm-interactive-proxy`,
			seed: `\\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			root: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			seed: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			root: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			seed: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
	}
	for _, tc := range seeds {
		t.Run(tc.root, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q", reason)
			}
			seed := tc.seed
			first, changed := mapping.VirtualizePath(seed)
			if !changed {
				t.Fatalf("VirtualizePath(%q) reported no change for an active mapping", seed)
			}
			second, changedAgain := mapping.VirtualizePath(first)
			if changedAgain {
				t.Errorf("re-virtualizing %q returned %q; the rewrite must be idempotent", first, second)
			}
			if second != first {
				t.Errorf("re-virtualizing %q returned %q, want it unchanged", first, second)
			}
			third, changedThird := mapping.VirtualizePath(second)
			if changedThird || third != second {
				t.Errorf("third pass returned %q (changed=%v), want %q unchanged", third, changedThird, second)
			}
		})
	}
}

// expandCase is one row of the inverse prefix mapping table.
type expandCase struct {
	name     string
	root     string
	path     string
	wantPath string
	wantRes  pathvirtualization.ExpandResult
}

func expandCases() []expandCase {
	const expanded = pathvirtualization.ExpandResultExpanded
	const untouched = pathvirtualization.ExpandResultNotApplicable
	return []expandCase{
		{
			name:     "posix_alias_is_expanded",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/.__lip_v1__/w_ylfucd77chy74zh3qwma/src/main.go`,
			wantPath: `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
			wantRes:  expanded,
		},
		{
			// The alias ends with the boundary separator the client sent, so
			// expansion keeps it: the client's bytes are never dropped.
			name:     "posix_bare_alias_keeps_its_separator",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/.__lip_v1__/w_ylfucd77chy74zh3qwma/`,
			wantPath: `/home/dev/projects/go-llm-interactive-proxy/`,
			wantRes:  expanded,
		},
		{
			name:     "posix_repeated_separator_alias_is_preserved",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/.__lip_v1__/w_ylfucd77chy74zh3qwma//src/main.go`,
			wantPath: `/home/dev/projects/go-llm-interactive-proxy//src/main.go`,
			wantRes:  expanded,
		},
		{
			name:     "drive_bare_alias_keeps_its_separator",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa\`,
			wantPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			wantRes:  expanded,
		},
		{
			name:     "extended_unc_bare_alias_keeps_its_separator",
			root:     `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			path:     `\\?\UNC\.__lip_v1__\w_nmlkx2hd77peyqhynboa\`,
			wantPath: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy\`,
			wantRes:  expanded,
		},
		{
			// A real root spelled with a trailing separator is the mirror case: the
			// mapping's own spelling wins, because expansion reconstructs RealRoot.
			name:     "trailing_separator_real_root_reconstructs_its_own_spelling",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			path:     `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa`,
			wantPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			wantRes:  expanded,
		},
		{
			name:     "posix_foreign_tag_is_not_expanded",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/.__lip_v1__/w_aaaaaaaaaaaaaaaaaaaa/src/main.go`,
			wantPath: `/.__lip_v1__/w_aaaaaaaaaaaaaaaaaaaa/src/main.go`,
			wantRes:  untouched,
		},
		{
			name:     "posix_alias_mid_segment_is_not_expanded",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/.__lip_v1__/w_ylfucd77chy74zh3qwmaX/src/main.go`,
			wantPath: `/.__lip_v1__/w_ylfucd77chy74zh3qwmaX/src/main.go`,
			wantRes:  untouched,
		},
		{
			name:     "real_path_is_never_expanded",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
			wantPath: `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
			wantRes:  untouched,
		},
		{
			name:     "unrelated_path_is_never_expanded",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     `/etc/passwd`,
			wantPath: `/etc/passwd`,
			wantRes:  untouched,
		},
		{
			name:     "empty_path_is_never_expanded",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			path:     "",
			wantPath: "",
			wantRes:  untouched,
		},
		{
			name:     "inactive_posix_mapping_never_expands",
			root:     `/a/b`,
			path:     `/.__lip_v1__/w_v4vm5apmwzenoijignaq/src/main.go`,
			wantPath: `/.__lip_v1__/w_v4vm5apmwzenoijignaq/src/main.go`,
			wantRes:  untouched,
		},
		{
			name:     "drive_alias_is_expanded",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "drive_alias_is_case_and_separator_tolerant",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `c:/.__lip_v1__/w_ilcrzjze5qdqueaesaxa\src\main.go`,
			wantPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "posix_alias_under_a_drive_mapping_is_untouched",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `/.__lip_v1__/w_ylfucd77chy74zh3qwma/src/main.go`,
			wantPath: `/.__lip_v1__/w_ylfucd77chy74zh3qwma/src/main.go`,
			wantRes:  untouched,
		},
		{
			name:     "unc_alias_is_expanded",
			root:     `\\build01\team\source\repos\go-llm-interactive-proxy`,
			path:     `\\.__lip_v1__\w_ffs2lbqb5oquzqw4nz5a\src\main.go`,
			wantPath: `\\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "extended_drive_alias_is_expanded",
			root:     `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `\\?\C:\.__lip_v1__\w_xlocyx2rtgrjkuj4cr3q\src\main.go`,
			wantPath: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "extended_unc_alias_is_expanded",
			root:     `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			path:     `\\?\UNC\.__lip_v1__\w_nmlkx2hd77peyqhynboa\src\main.go`,
			wantPath: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
			wantRes:  expanded,
		},
		{
			name:     "extended_alias_under_a_plain_drive_mapping_is_untouched",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			path:     `\\?\C:\.__lip_v1__\w_xlocyx2rtgrjkuj4cr3q\src\main.go`,
			wantPath: `\\?\C:\.__lip_v1__\w_xlocyx2rtgrjkuj4cr3q\src\main.go`,
			wantRes:  untouched,
		},
	}
}

// TestExpandPathInversePrefixMapping pins design.md 152: expansion reconstructs
// the mapping's original RealRoot spelling plus the untouched suffix. Reserved
// alias validation and stale-workspace rejection belong to task 2.3, so this
// table only covers aliases whose tag already matches the mapping.
func TestExpandPathInversePrefixMapping(t *testing.T) {
	t.Parallel()

	for _, tc := range expandCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			got, result := mapping.ExpandPath(tc.path)
			if got != tc.wantPath {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.path, got, tc.wantPath)
			}
			if result != tc.wantRes {
				t.Errorf("ExpandPath(%q) result = %v, want %v", tc.path, result, tc.wantRes)
			}
			if result == pathvirtualization.ExpandResultNotApplicable && got != tc.path {
				t.Errorf("ExpandPath(%q) reported no expansion but returned %q", tc.path, got)
			}
		})
	}
}

// TestExpandPathReservedAliasSeamIsOwnedByTaskTwoThree makes the task 2.3
// handoff seam explicit and test-visible instead of an implicit landmine.
//
// Today a syntactically recognized V1 reserved alias that this mapping did not
// derive falls through to ExpandResultNotApplicable, i.e. it is returned to the
// caller unchanged. That is the correct behavior for task 2.2, which has no
// reserved-alias vocabulary of its own. Task 2.3 must REPLACE this fallthrough,
// not merely add result codes: per design.md 186-195, reserved-alias parsing runs
// BEFORE ordinary real-root prefix matching, so a malformed alias must be
// reported as a bounded reason and an alias of another workspace must be reported
// as a workspace mismatch, never handed back as an ordinary client path.
//
// These rows pin the current seam. Task 2.3 rewrites them into the bounded
// malformed/mismatch outcomes; the test name keeps that replacement visible.
func TestExpandPathReservedAliasSeamIsOwnedByTaskTwoThree(t *testing.T) {
	t.Parallel()

	const root = `/home/dev/projects/go-llm-interactive-proxy`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
	}
	// The mapping must be active, otherwise the seam would be untestable.
	if mapping.VirtualRoot == "" {
		t.Fatalf("mapping for %q derived no alias", root)
	}

	cases := []struct {
		name string
		path string
	}{
		{name: "foreign_workspace_alias", path: `/.__lip_v1__/w_aaaaaaaaaaaaaaaaaaaa/src/main.go`},
		{name: "malformed_short_tag", path: `/.__lip_v1__/w_tooshort/src/main.go`},
		{name: "malformed_missing_tag", path: `/.__lip_v1__/src/main.go`},
		{name: "alias_mid_segment", path: `/.__lip_v1__/w_ylfucd77chy74zh3qwmaX/src/main.go`},
		{name: "drive_alias_under_a_posix_mapping", path: `C:\.__lip_v1__\w_ilcrzjze5qdqueaesaxa\src\main.go`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, result := mapping.ExpandPath(tc.path)
			if got != tc.path {
				t.Errorf("ExpandPath(%q) = %q, want it returned unchanged", tc.path, got)
			}
			// Task 2.3 replaces this outcome with a bounded reason; until then a
			// non-matching alias is simply not applicable.
			if result != pathvirtualization.ExpandResultNotApplicable {
				t.Errorf("ExpandPath(%q) result = %v, want %v until task 2.3 replaces the fallthrough",
					tc.path, result, pathvirtualization.ExpandResultNotApplicable)
			}
		})
	}
}

// TestReservedNamespaceCollisionStaysOwnedByTaskTwoThree records the one known
// open collision: a project root spelled inside the reserved V1 namespace derives
// an alias nested inside that namespace. Task 2.2 deliberately does not detect it
// (requirements 1.8 and tasks.md 70 assign collision detection to task 2.3), so
// this test pins the current behavior instead of asserting a fix that does not
// belong here. What matters now is that the shape is handled self-consistently:
// the mapping virtualizes and expands its own paths, so nothing is corrupted
// before 2.3 rejects the root outright.
func TestReservedNamespaceCollisionStaysOwnedByTaskTwoThree(t *testing.T) {
	t.Parallel()

	const root = `C:\.__lip_v1__\w_abcdefghijklmnopqrstxx`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("DeriveMapping(%q) reason = %q; task 2.2 does not reject a root for namespace collision", root, reason)
	}
	if mapping.RealRoot != root {
		t.Fatalf("RealRoot = %q, want %q", mapping.RealRoot, root)
	}
	// The alias is nested in the reserved namespace, which task 2.3 must reject.
	if !strings.Contains(mapping.VirtualRoot, reservedNamespaceV1) {
		t.Fatalf("virtual root = %q, want an alias inside the reserved namespace", mapping.VirtualRoot)
	}
	if mapping.VirtualRoot == "" {
		t.Fatal("mapping for a reserved-namespace root is inactive; the shorter-than gate must not change this shape")
	}
	// The mapping must still round-trip its own paths, so the collision cannot
	// corrupt a path before task 2.3 handles it.
	real := root + `\src/main.go`
	alias, changed := mapping.VirtualizePath(real)
	if !changed {
		t.Fatalf("VirtualizePath(%q) reported no change; the shape would be untestable", real)
	}
	back, result := mapping.ExpandPath(alias)
	if result != pathvirtualization.ExpandResultExpanded {
		t.Fatalf("ExpandPath(%q) result = %v", alias, result)
	}
	if back != real {
		t.Errorf("ExpandPath(%q) = %q, want %q", alias, back, real)
	}
}

// roundTripCase walks a real path through virtualization and back.
type roundTripCase struct {
	name     string
	root     string
	realPath string
	// wantReverse overrides the path expansion must produce. It is set only for
	// the one shape that cannot be byte-for-byte reversible, because the alias
	// spelling is fixed and always ends with a separator (design.md 178-182): a
	// client path that is exactly the bare real root therefore gains that single
	// trailing separator on the way back, since `/…/w_<tag>` and
	// `/…/w_<tag>/` are the same alias after boundary trimming. Client bytes are
	// never dropped.
	wantReverse string
}

// wantRestored is the path expansion must produce for this case.
func (tc roundTripCase) wantRestored() string {
	if tc.wantReverse != "" {
		return tc.wantReverse
	}
	return tc.realPath
}

func roundTripCases() []roundTripCase {
	return []roundTripCase{
		{
			name:     "posix_plain_suffix",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
		},
		{
			name:     "posix_trailing_separator_root",
			root:     `/home/dev/projects/go-llm-interactive-proxy/`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy/src/main.go`,
		},
		{
			name:        "posix_bare_root_without_separator",
			root:        `/home/dev/projects/go-llm-interactive-proxy`,
			realPath:    `/home/dev/projects/go-llm-interactive-proxy`,
			wantReverse: `/home/dev/projects/go-llm-interactive-proxy/`,
		},
		{
			name:     "posix_bare_root_with_separator",
			root:     `/home/dev/projects/go-llm-interactive-proxy/`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy/`,
		},
		{
			name:     "posix_dot_segment_suffix_is_untouched",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy/pkg/../src/main.go`,
		},
		{
			name:     "posix_non_ascii_suffix_is_untouched",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy/cmd/straße/main.go`,
		},
		{
			name:     "drive_plain_suffix",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			realPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:     "drive_trailing_separator_root",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			realPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:        "drive_bare_root_without_separator",
			root:        `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			realPath:    `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			wantReverse: `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
		},
		{
			name:     "drive_bare_root_with_separator",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			realPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
		},
		{
			name:     "drive_mixed_separator_root",
			root:     `C:/Users/dev/source/repos/go-llm-interactive-proxy`,
			realPath: `C:/Users/dev/source/repos/go-llm-interactive-proxy/src/main.go`,
		},
		{
			name:     "drive_lower_case_root_is_reconstructed",
			root:     `c:\users\dev\source\repos\go-llm-interactive-proxy`,
			realPath: `c:\users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:     "unc_plain_suffix",
			root:     `\\build01\team\source\repos\go-llm-interactive-proxy`,
			realPath: `\\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:        "unc_bare_root",
			root:        `\\build01\team\source\repos\go-llm-interactive-proxy`,
			realPath:    `\\build01\team\source\repos\go-llm-interactive-proxy`,
			wantReverse: `\\build01\team\source\repos\go-llm-interactive-proxy\`,
		},
		{
			name:     "extended_drive_plain_suffix",
			root:     `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			realPath: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:     "extended_drive_trailing_separator_root",
			root:     `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
			realPath: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:     "extended_unc_plain_suffix",
			root:     `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			realPath: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy\src\main.go`,
		},
		{
			name:     "extended_unc_forward_separator_root",
			root:     `\\?\unc/build01/team/source/repos/go-llm-interactive-proxy`,
			realPath: `\\?\unc/build01/team/source/repos/go-llm-interactive-proxy/src/main.go`,
		},
		// A path that names the root plus a lone boundary separator carries bytes
		// the alias keeps, so expansion must hand them back (design.md 152).
		{
			name:     "posix_lone_separator_after_root",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy/`,
		},
		{
			name:     "posix_repeated_separator_after_root",
			root:     `/home/dev/projects/go-llm-interactive-proxy`,
			realPath: `/home/dev/projects/go-llm-interactive-proxy//`,
		},
		{
			name:     "drive_lone_separator_after_root",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			realPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
		},
		{
			name:     "drive_repeated_separator_after_root",
			root:     `C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			realPath: `C:\Users\dev\source\repos\go-llm-interactive-proxy\\`,
		},
		{
			name:     "unc_lone_separator_after_root",
			root:     `\\build01\team\source\repos\go-llm-interactive-proxy`,
			realPath: `\\build01\team\source\repos\go-llm-interactive-proxy\`,
		},
		{
			name:     "extended_drive_lone_separator_after_root",
			root:     `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
			realPath: `\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy\`,
		},
		{
			name:     "extended_unc_lone_separator_after_root",
			root:     `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
			realPath: `\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy\`,
		},
	}
}

// TestExpandPathReversesVirtualizePath pins design.md 429: expanding a virtualized
// path reproduces the original bytes, including the original real-root spelling.
//
// The single exception is spelled out per row in wantRestored: the alias spelling
// is fixed and always ends with a separator, so a client path that is exactly the
// bare real root comes back with that one separator instead of losing it.
func TestExpandPathReversesVirtualizePath(t *testing.T) {
	t.Parallel()

	for _, tc := range roundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			virtual, changed := mapping.VirtualizePath(tc.realPath)
			if !changed {
				t.Fatalf("VirtualizePath(%q) reported no change; the round trip would prove nothing", tc.realPath)
			}
			if virtual == tc.realPath {
				t.Fatalf("VirtualizePath(%q) returned the real path unchanged", tc.realPath)
			}
			if strings.Contains(virtual, mapping.RealRoot) {
				t.Errorf("virtualized path %q still exposes the real root", virtual)
			}
			if len(virtual) >= len(tc.realPath) {
				t.Errorf("virtualized path %q is not shorter than %q", virtual, tc.realPath)
			}
			back, result := mapping.ExpandPath(virtual)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("ExpandPath(%q) result = %v, want %v", virtual, result, pathvirtualization.ExpandResultExpanded)
			}
			if want := tc.wantRestored(); back != want {
				t.Errorf("round trip = %q, want %q", back, want)
			}
			// Requirement 1.6: the untouched suffix survives, and only the fixed
			// alias spelling may add a byte to a path that is exactly the root.
			if len(back) < len(tc.realPath) {
				t.Errorf("round trip dropped bytes: %q -> %q", tc.realPath, back)
			}
		})
	}
}

// TestExpandPathIsIdempotentOnRealPaths proves a model-emitted real path that
// never left the client namespace is handed back untouched.
func TestExpandPathIsIdempotentOnRealPaths(t *testing.T) {
	t.Parallel()

	for _, tc := range roundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("reason = %q", reason)
			}
			got, result := mapping.ExpandPath(tc.realPath)
			if got != tc.realPath || result != pathvirtualization.ExpandResultNotApplicable {
				t.Errorf("ExpandPath(%q) = %q/%v, want it untouched", tc.realPath, got, result)
			}
		})
	}
}

// TestVirtualizeAndExpandAreInverse proves the mapper is a reversible namespace
// translation (design.md 5-7): virtualizing an expanded alias returns the alias.
func TestVirtualizeAndExpandAreInverse(t *testing.T) {
	t.Parallel()

	for _, tc := range roundTripCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapping, reason := pathvirtualization.DeriveMapping(tc.root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatalf("DeriveMapping(%q) reason = %q", tc.root, reason)
			}
			alias, changed := mapping.VirtualizePath(tc.realPath)
			if !changed {
				t.Fatalf("VirtualizePath(%q) reported no change", tc.realPath)
			}
			real, result := mapping.ExpandPath(alias)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("ExpandPath(%q) result = %v", alias, result)
			}
			if want := tc.wantRestored(); real != want {
				t.Fatalf("ExpandPath(%q) = %q, want %q", alias, real, want)
			}
			// Re-virtualizing the restored path must return the same alias, so the
			// namespace translation is an involution apart from the documented
			// bare-root shape.
			again, changedAgain := mapping.VirtualizePath(real)
			if !changedAgain || again != alias {
				t.Errorf("VirtualizePath(%q) = %q (changed=%v), want %q", real, again, changedAgain, alias)
			}
		})
	}
}

// TestMappingIsAPureValueObject proves the derivation holds no mutable state: the
// returned Mapping is self-contained, later derivations of other roots cannot
// change it, and two mappings derived independently never alias each other's
// storage.
func TestMappingIsAPureValueObject(t *testing.T) {
	t.Parallel()

	const root = `/home/dev/projects/go-llm-interactive-proxy`
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("reason = %q", reason)
	}

	alias := mapping.VirtualRoot + `src/main.go`
	got, result := mapping.ExpandPath(alias)
	if result != pathvirtualization.ExpandResultExpanded {
		t.Fatalf("ExpandPath(%q) result = %v", alias, result)
	}
	if want := `/home/dev/projects/go-llm-interactive-proxy/src/main.go`; got != want {
		t.Errorf("ExpandPath(%q) = %q, want %q", alias, got, want)
	}

	first, _ := pathvirtualization.DeriveMapping(root)
	second, _ := pathvirtualization.DeriveMapping(root)
	if first != second || first != mapping {
		t.Fatalf("independent derivations differ: %+v vs %+v vs %+v", mapping, first, second)
	}
	// Interleaving other roots' derivations must not change an already derived
	// value: there is no cache, counter, or shared buffer behind it.
	others := []string{
		`/srv/shared/workspace-one`,
		`C:\Users\dev\source\repos\other-repository`,
		`\\build01\team\source\repos\go-llm-interactive-proxy`,
		`\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
	}
	for _, other := range others {
		derived, otherReason := pathvirtualization.DeriveMapping(other)
		if otherReason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", other, otherReason)
		}
		if derived == mapping {
			t.Fatalf("root %q derived the same mapping as %q", other, root)
		}
	}
	if again, _ := pathvirtualization.DeriveMapping(root); again != mapping {
		t.Fatalf("derivation drifted after other roots: %+v vs %+v", mapping, again)
	}

	// Clobbering one derived value must not be visible through another.
	third, _ := pathvirtualization.DeriveMapping(`/home/dev/projects/other-repository`)
	if first.WorkspaceTag == third.WorkspaceTag {
		t.Fatal("distinct roots share a workspace tag")
	}
	third.VirtualRoot = "/clobbered/"
	if first.VirtualRoot == third.VirtualRoot || mapping.VirtualRoot == third.VirtualRoot {
		t.Fatal("derived mappings share mutable storage")
	}
	if again, _ := pathvirtualization.DeriveMapping(root); again != mapping {
		t.Fatalf("a clobbered sibling mapping changed %q: %+v", root, again)
	}
}

// TestExpandResultLabelsAreBounded keeps the expansion outcome a fixed,
// content-free dimension suitable for metrics.
func TestExpandResultLabelsAreBounded(t *testing.T) {
	t.Parallel()

	want := []struct {
		result pathvirtualization.ExpandResult
		label  string
	}{
		{pathvirtualization.ExpandResultExpanded, "expanded"},
		{pathvirtualization.ExpandResultNotApplicable, "not_applicable"},
	}
	for _, tc := range want {
		if got := tc.result.String(); got != tc.label {
			t.Errorf("ExpandResult(%d).String() = %q, want %q", tc.result, got, tc.label)
		}
	}
	if got := pathvirtualization.ExpandResult(99).String(); got != "unknown" {
		t.Errorf("unbounded ExpandResult label = %q, want %q", got, "unknown")
	}
}

// TestWorkspaceTagIsNeverEmittedInBoundedDiagnostics guards the observability
// rule from requirement 7.7 and design.md 400: the 96-bit tag is backend-visible
// identity, never a reason code, label, or error string.
func TestWorkspaceTagIsNeverEmittedInBoundedDiagnostics(t *testing.T) {
	t.Parallel()

	tagged := []string{
		`/home/dev/projects/go-llm-interactive-proxy`,
		`C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`\\build01\team\source\repos\go-llm-interactive-proxy`,
		`\\?\C:\Users\dev\source\repos\go-llm-interactive-proxy`,
		`\\?\UNC\build01\team\source\repos\go-llm-interactive-proxy`,
	}
	for _, root := range tagged {
		mapping, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			t.Fatalf("DeriveMapping(%q) reason = %q", root, reason)
		}
		// A workspace tag injected into a rejected root must not reappear in the
		// bounded reason code that classifies it.
		_, deviceReason := pathvirtualization.DeriveMapping(`\\.\PIPE\` + mapping.WorkspaceTag)
		if strings.Contains(string(deviceReason), mapping.WorkspaceTag) {
			t.Errorf("reason %q leaks the workspace tag", deviceReason)
		}
		if label := pathvirtualization.ExpandResultNotApplicable.String(); strings.Contains(label, mapping.WorkspaceTag) {
			t.Errorf("expansion label %q leaks the workspace tag", label)
		}
		if label := mapping.Flavor.String(); strings.Contains(label, mapping.WorkspaceTag) {
			t.Errorf("flavor label %q leaks the workspace tag", label)
		}
	}
}
