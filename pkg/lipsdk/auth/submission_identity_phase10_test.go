package auth

import (
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/submission"
)

func TestDecisionCarriesTrustedSubmissionAuthority(t *testing.T) {
	t.Parallel()

	authority := submission.Authority{
		Kind:         submission.KindNewSubmission,
		Source:       submission.SourceAuthenticatedCurrentTurn,
		SubmissionID: "submission-auth",
	}
	decision := Decision{SubmissionAuthority: &authority}
	if decision.SubmissionAuthority == nil {
		t.Fatal("decision lost trusted submission authority")
	}
	if decision.SubmissionAuthority.SubmissionID != authority.SubmissionID {
		t.Fatalf("submission ID = %q, want %q", decision.SubmissionAuthority.SubmissionID, authority.SubmissionID)
	}
}
