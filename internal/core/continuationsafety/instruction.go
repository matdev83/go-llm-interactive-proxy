package continuationsafety

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bounded size limits for hidden recovery instruction echo.
const (
	MaxRecoveryReasonBytes    = 512
	MaxRecoveryObjectiveBytes = 512
)

// RecoveryInput describes the bounded facts carried into the hidden recovery
// instruction. It is pure data; no I/O is performed.
type RecoveryInput struct {
	Reason             string
	RemainingObjective string
	Attempt            int
	MaxAttempts        int
	WorkComplete       bool
	NeedsUser          bool
}

// BoundRecoveryText returns s truncated to at most max bytes without splitting
// a UTF-8 rune. It mirrors the stopguardverify boundText approach but ensures
// complete rune validity.
func BoundRecoveryText(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	// Remove trailing continuation bytes, then validate.
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	// If we cut inside a multi-byte start, the remaining start byte alone is
	// invalid; drop it until valid.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
		for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
			cut = cut[:len(cut)-1]
		}
	}
	return cut
}

// BuildRecoveryInstruction renders the normative <automated-recovery> template
// with bounded reason/objective echo and attempt framing. The output is
// internal control content: it starts with the automation tag and never uses
// a user-role preamble. Runtime guarantees it is absent from A-side user
// transcript.
//
// The template contains the three conditional branches unconditionally so that
// all conservative directives are always present; tests assert containment
// rather than exclusivity.
func BuildRecoveryInstruction(in RecoveryInput) string {
	reason := BoundRecoveryText(strings.TrimSpace(in.Reason), MaxRecoveryReasonBytes)
	objective := BoundRecoveryText(strings.TrimSpace(in.RemainingObjective), MaxRecoveryObjectiveBytes)
	attempt := in.Attempt
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = attempt
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
	}
	if attempt <= 0 {
		attempt = 1
	}
	var b strings.Builder
	// Conservative growth: template overhead ~900 + bounded payloads.
	b.Grow(1024 + len(reason) + len(objective))
	b.WriteString("<automated-recovery>\n")
	b.WriteString("This is an internal recovery instruction, not a new user request, approval, or expansion of scope. The user has not sent a new message.\n")
	b.WriteString("\n")
	b.WriteString("Re-read the existing user request and the work already completed.\n")
	b.WriteString("If there is concrete unfinished work from that request that you can perform without new user input, resume exactly that work from the last safe point.\n")
	b.WriteString("\n")
	b.WriteString("If the requested work is already complete, DO NOT invent, repeat, broaden, optimize, or discover additional work. End normally.\n")
	b.WriteString("\n")
	b.WriteString("If the next step requires user input, permission, approval, or a choice, do not assume it; end normally so the user can respond.\n")
	b.WriteString("\n")
	fmt.Fprintf(&b, "Recovery reason: %s\n", reason)
	fmt.Fprintf(&b, "Remaining objective: %s\n", objective)
	fmt.Fprintf(&b, "Attempt %d/%d\n", attempt, maxAttempts)
	b.WriteString("</automated-recovery>")
	return b.String()
}
