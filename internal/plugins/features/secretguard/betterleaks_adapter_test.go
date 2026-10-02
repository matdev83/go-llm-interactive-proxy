package secretguard

import (
	"context"
	"strings"
	"testing"

	"github.com/betterleaks/betterleaks/v2/report"
	blscan "github.com/betterleaks/betterleaks/v2/scan"
	"github.com/betterleaks/betterleaks/v2/sources"
)

const adapterGitHubToken = "ghp_aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"

func TestNewBetterLeaksScanner_AllowMarkersDoNotSuppressFindings(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{
		Enabled:           true,
		MinimumConfidence: blscan.ConfidenceMedium,
		MaxDecodeDepth:    1,
		Workers:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detector == nil {
		t.Fatal("enabled policy must construct a scanner")
	}

	for _, marker := range []string{"betterleaks:allow", "gitleaks:allow"} {
		t.Run(marker, func(t *testing.T) {
			findings := scanBetterLeaksTestFragment(t, detector, "GITHUB_TOKEN="+adapterGitHubToken+" // "+marker)
			if len(findings) == 0 {
				t.Fatalf("marker %q suppressed a detectable credential", marker)
			}
		})
	}
}

func scanBetterLeaksTestFragment(t *testing.T, detector *betterLeaksScanner, content string) []report.Finding {
	t.Helper()
	var findings []report.Finding
	_, err := detector.scanner.Scan(context.Background(), &sources.Reader{Content: strings.NewReader(content)}, func(finding report.Finding) error {
		findings = append(findings, finding)
		return nil
	})
	if err != nil {
		t.Fatalf("scan fragment: %v", err)
	}
	return findings
}

func TestNewBetterLeaksScanner_RejectsInvalidConstructionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy BetterLeaksPolicy
	}{
		{
			name: "zero workers",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    1,
				Workers:           0,
			},
		},
		{
			name: "negative workers",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    1,
				Workers:           -1,
			},
		},
		{
			name: "invalid confidence",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: "unknown",
				MaxDecodeDepth:    1,
				Workers:           1,
			},
		},
		{
			name: "negative decode depth",
			policy: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: blscan.ConfidenceMedium,
				MaxDecodeDepth:    -1,
				Workers:           1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if detector, err := newBetterLeaksScanner(test.policy); err == nil || detector != nil {
				t.Fatalf("newBetterLeaksScanner(%+v) = detector=%v, err=%v; want construction error", test.policy, detector, err)
			}
		})
	}
}

func TestNewBetterLeaksScanner_DisabledPolicyDoesNotConstructHandle(t *testing.T) {
	detector, err := newBetterLeaksScanner(BetterLeaksPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if detector != nil {
		t.Fatal("disabled policy must not construct a scanner handle")
	}
}
