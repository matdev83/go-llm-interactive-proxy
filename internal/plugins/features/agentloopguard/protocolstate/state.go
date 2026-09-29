// Package protocolstate owns the preferred ALG explicit-completion recovery token.
package protocolstate

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/matdev83/go-llm-interactive-proxy/pkg/lipsdk/terminaldecision"
)

const Prefix = "alg-proto-v1."

var ErrInvalid = errors.New("agent-loop-guard protocol: invalid state token")

// State is bounded request-local protocol progress. It contains no prompt text or IDs.
type State struct {
	Reprompts             int
	LastFingerprint       string
	ConsecutiveNoProgress int
	Terminal              bool
}

func Encode(s State) (string, error) {
	if s.Reprompts < 0 || s.Reprompts > 3 || s.ConsecutiveNoProgress < 0 || s.ConsecutiveNoProgress > 64 {
		return "", ErrInvalid
	}
	if s.LastFingerprint != "" && (!strings.HasPrefix(s.LastFingerprint, "sha256:") || len(s.LastFingerprint) != 71) {
		return "", ErrInvalid
	}
	terminal := "0"
	if s.Terminal {
		terminal = "1"
	}
	fp := base64.RawURLEncoding.EncodeToString([]byte(s.LastFingerprint))
	token := Prefix + strconv.Itoa(s.Reprompts) + "." + strconv.Itoa(s.ConsecutiveNoProgress) + "." + terminal + "." + fp
	if len(token) > terminaldecision.MaxIdentifierBytes {
		return "", ErrInvalid
	}
	return token, nil
}

func Decode(token string) (State, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, Prefix) || len(token) > terminaldecision.MaxIdentifierBytes {
		return State{}, ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(token, Prefix), ".")
	if len(parts) != 4 {
		return State{}, ErrInvalid
	}
	reprompts, err := strconv.Atoi(parts[0])
	if err != nil || reprompts < 0 || reprompts > 3 {
		return State{}, ErrInvalid
	}
	noProgress, err := strconv.Atoi(parts[1])
	if err != nil || noProgress < 0 || noProgress > 64 {
		return State{}, ErrInvalid
	}
	if parts[2] != "0" && parts[2] != "1" {
		return State{}, ErrInvalid
	}
	fpBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return State{}, ErrInvalid
	}
	fp := string(fpBytes)
	if fp != "" && (!strings.HasPrefix(fp, "sha256:") || len(fp) != 71) {
		return State{}, ErrInvalid
	}
	return State{Reprompts: reprompts, LastFingerprint: fp, ConsecutiveNoProgress: noProgress, Terminal: parts[2] == "1"}, nil
}

// Fingerprint hashes only stable canonical progress evidence. Volatile IDs,
// attempt counters, revisions, and timestamps are intentionally excluded.
func Fingerprint(in terminaldecision.Input) string {
	h := sha256.New()
	write := func(name, value string) {
		_, _ = fmt.Fprintf(h, "%s:%d:%s\n", name, len(value), value)
	}
	write("cause", string(in.Candidate.Cause))
	write("committed", strconv.FormatBool(in.Candidate.OutputCommitted))
	write("objective", normalize(in.Evidence.Objective))
	write("recent", normalize(in.Evidence.RecentText))
	write("candidate", normalize(in.Evidence.CandidateText))
	write("explicit", strconv.FormatBool(in.Evidence.ExplicitCompletion))
	write("expected", strconv.FormatBool(in.Evidence.ExplicitCompletionExpected))
	count := min(int(in.Evidence.ActionCount), len(in.Evidence.Actions))
	write("action_count", strconv.Itoa(count))
	for i := range count {
		action := in.Evidence.Actions[i]
		write("action_kind", string(action.Kind))
		write("action_status", string(action.Status))
		write("action_name", normalize(action.Name))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func normalize(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}
