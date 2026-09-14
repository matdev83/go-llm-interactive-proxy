package runtime

import (
	"errors"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/b2bua"
	"github.com/matdev83/go-llm-interactive-proxy/internal/core/billing"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipapi"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/metering"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/session"
	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/submission"
)

func TestStampBillingCallIDReusesOnlyTrustedContinuationIdentity(t *testing.T) {
	t.Parallel()

	existing := billing.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	prep := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:          submission.KindTransportRetry,
			Source:        submission.SourceAuthenticatedContinuation,
			SubmissionID:  "submission-retry",
			BillingCallID: existing.String(),
		},
	}}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatalf("stampBillingCallID() error = %v", err)
	}
	if prep.billingCallID != existing {
		t.Fatalf("billing call ID = %q, want trusted existing %q", prep.billingCallID, existing)
	}

	followUp := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindFollowUp,
			Source:       submission.SourceAuthenticatedCurrentTurn,
			SubmissionID: "submission-follow-up",
		},
	}}
	if err := stampBillingCallID(followUp); err != nil {
		t.Fatalf("follow-up stampBillingCallID() error = %v", err)
	}
	if followUp.billingCallID == "" || followUp.billingCallID == existing {
		t.Fatalf("follow-up billing call ID = %q, want a fresh ID", followUp.billingCallID)
	}

	toolContinuation := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindToolContinuation,
			Source:       submission.SourceAuthenticatedContinuation,
			SubmissionID: "submission-retry",
		},
	}}
	if err := stampBillingCallID(toolContinuation); err != nil {
		t.Fatalf("tool continuation stampBillingCallID() error = %v", err)
	}
	if toolContinuation.billingCallID == "" || toolContinuation.billingCallState.submissionIdentity() != "submission-retry" {
		t.Fatalf("tool continuation state = %+v, want fresh call with stable submission identity", toolContinuation.billingCallState)
	}

	firstSubmission := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindNewSubmission,
			Source:       submission.SourceAuthenticatedCurrentTurn,
			SubmissionID: "submission-1",
		},
	}}
	if err := stampBillingCallID(firstSubmission); err != nil {
		t.Fatalf("first submission stampBillingCallID() error = %v", err)
	}
	firstCallID := firstSubmission.billingCallID
	followUpSubmission := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindFollowUp,
			Source:       submission.SourceAuthenticatedCurrentTurn,
			SubmissionID: "submission-2",
		},
	}}
	if err := stampBillingCallID(followUpSubmission); err != nil {
		t.Fatalf("resumed follow-up stampBillingCallID() error = %v", err)
	}
	if followUpSubmission.billingCallID == firstCallID || firstSubmission.billingCallState.submissionIdentity() != "submission-1" || followUpSubmission.billingCallState.submissionIdentity() != "submission-2" {
		t.Fatalf("follow-up state reopened prior submission: first=%q/%q follow-up=%q/%q", firstCallID, firstSubmission.billingCallState.submissionIdentity(), followUpSubmission.billingCallID, followUpSubmission.billingCallState.submissionIdentity())
	}
}

func TestStampBillingCallIDRejectsSubmissionIdentityChangeOnSameCall(t *testing.T) {
	t.Parallel()

	callID := billing.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	state := newBillingCallState(callID)
	state.submissionID = "submission-1"
	prep := &preparedRequest{
		billingCallID:    callID,
		billingCallState: state,
		submission: submission.BindingResult{
			Status: submission.StatusTrusted,
			Authority: submission.Authority{
				Kind:          submission.KindToolContinuation,
				Source:        submission.SourceAuthenticatedContinuation,
				SubmissionID:  "submission-2",
				BillingCallID: callID.String(),
			},
		},
	}
	if err := stampBillingCallID(prep); !errors.Is(err, submission.ErrScopeMismatch) {
		t.Fatalf("stampBillingCallID() error = %v, want ErrScopeMismatch", err)
	}
}

func TestCompleteSubmissionAuthorityKeepsFreshFollowUpCallIdentitySeparate(t *testing.T) {
	t.Parallel()

	callID := billing.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	prep := &preparedRequest{
		call:          &lipapi.Call{ID: "call-follow-up"},
		billingCallID: callID,
		recvTurnFacts: recvTurnFacts{billingStoreID: "store-1"},
		submission: submission.BindingResult{
			Status: submission.StatusTrusted,
			Authority: submission.Authority{
				Kind:         submission.KindFollowUp,
				Source:       submission.SourceAuthenticatedCurrentTurn,
				SubmissionID: "submission-follow-up",
			},
		},
	}
	ibt := &identityBoundTurn{
		aLeg:       b2bua.ALegRecord{ALegID: "a-leg-1"},
		preSession: session.SessionView{ALegID: "a-leg-1"},
	}
	if err := completeSubmissionAuthority(prep, ibt); err != nil {
		t.Fatalf("completeSubmissionAuthority() error = %v", err)
	}
	if !prep.submission.Trusted() || prep.submission.Authority.SubmissionID != "submission-follow-up" {
		t.Fatalf("submission result = %+v, want trusted follow-up", prep.submission)
	}
	if prep.submission.Authority.BillingCallID != "" {
		t.Fatalf("fresh follow-up authority reused BillingCallID %q", prep.submission.Authority.BillingCallID)
	}
}

func TestBillingLegRecordCarriesTrustedSubmissionIdentity(t *testing.T) {
	t.Parallel()

	callID := billing.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	record := billingLegRecord(billingLegDraft{
		callID: callID, submissionID: "submission-leg", aLegID: "a-leg", storeID: "store",
		bLegID: "b-leg", seq: 1,
	})
	if record.SubmissionID != "submission-leg" {
		t.Fatalf("record submission ID = %q, want %q", record.SubmissionID, "submission-leg")
	}
}

func TestSubmissionIdentityStampCannotBeOverriddenByObservationPayload(t *testing.T) {
	t.Parallel()

	incoming := []metering.Observation{{
		Subject:     metering.SubjectRef{SubmissionID: "submission-spoofed"},
		Correlation: metering.CorrelationV2{SubmissionID: "submission-spoofed"},
	}}
	got := stampSubmissionIdentity(incoming, "submission-authenticated")
	if got[0].Subject.SubmissionID != "submission-authenticated" || got[0].Correlation.SubmissionID != "submission-authenticated" {
		t.Fatalf("stamped observation identity = %+v, want authenticated identity", got[0])
	}
	if incoming[0].Subject.SubmissionID != "submission-spoofed" || incoming[0].Correlation.SubmissionID != "submission-spoofed" {
		t.Fatal("identity stamping mutated caller-owned observation")
	}
	cleared := stampSubmissionIdentity(incoming, "")
	if cleared[0].Subject.SubmissionID != "" || cleared[0].Correlation.SubmissionID != "" {
		t.Fatalf("unsupported observation identity = %+v, want no submission claim", cleared[0])
	}
}
