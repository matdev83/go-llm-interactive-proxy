package protocolstate

import (
	"testing"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

func TestStateRoundTripAndStableFingerprint(t *testing.T) {
	in := terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{Cause:terminaldecision.CandidateCauseNormal,OutputCommitted:true},
		Evidence: terminaldecision.Evidence{Objective:"x",CandidateText:"y",ExplicitCompletionExpected:true},
		Deadline: time.Now().Add(time.Minute),
	}
	fp := Fingerprint(in)
	token, err := Encode(State{Reprompts:1,LastFingerprint:fp,ConsecutiveNoProgress:1})
	if err != nil { t.Fatal(err) }
	got, err := Decode(token)
	if err != nil { t.Fatal(err) }
	if got.Reprompts != 1 || got.LastFingerprint != fp || got.ConsecutiveNoProgress != 1 { t.Fatalf("got=%+v", got) }
	in.Request.TraceID = "volatile"
	if Fingerprint(in) != fp { t.Fatal("volatile identity changed fingerprint") }
}
