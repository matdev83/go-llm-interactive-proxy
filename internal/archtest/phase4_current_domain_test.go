package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhase4CurrentBillingDomainForbidsRetiredNames is the Phase-4 source
// ratchet. Historical migration sources and audit-only decode DTOs are not
// current domain/writer code and are intentionally outside this scan.
func TestPhase4CurrentBillingDomainForbidsRetiredNames(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range []string{"internal/core/billing", "internal/infra/billingstore", "internal/infra/billingcompose", "internal/infra/billingadmission"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			base := filepath.Base(path)
			if strings.HasPrefix(base, "202608") || base == "historical_authorization.go" {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"ReservedNano", "reserved_nano", "JournalBookLegacyAuthorization"} {
				if strings.Contains(string(body), forbidden) {
					t.Errorf("%s contains retired current-domain symbol %q", filepath.ToSlash(path), forbidden)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
