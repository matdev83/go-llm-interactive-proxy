package archtest

import (
	"strings"
	"testing"
)

// Shared readers for the task 11.2 doc contract. The file read is a tree walk on
// purpose (docs/ belongs to no Go package), so a string contract is the only
// machine check available for an operator guide.
//
// Every helper here is adjacency-first. A name/value pairing is always required
// on ONE line, because two independent substring checks would let a falsified
// value pass while the correct one appears somewhere else in the document. That
// is exactly the stale-documentation failure task 11.1's review caught, and it
// is why these helpers return the matching line instead of a bare bool.

// sessionClassificationGuideSection returns one guide section, bounded by the
// next heading of the same level.
func sessionClassificationGuideSection(t *testing.T, heading string) string {
	t.Helper()
	section, ok := sessionClassificationSectionIn(readSessionClassificationDoc(t), heading)
	if !ok {
		t.Fatalf("%s has no %q section", sessionClassificationDocRel, heading)
	}
	return section
}

// sessionClassificationSectionIn returns one section of any guide text, bounded
// by the next heading of the same level.
func sessionClassificationSectionIn(text, heading string) (string, bool) {
	start := strings.Index(text, heading)
	if start < 0 {
		return "", false
	}
	end := len(text)
	if next := strings.Index(text[start+len(heading):], "\n## "); next >= 0 {
		end = start + len(heading) + next
	}
	return text[start:end], true
}

// sessionClassificationGuideLine returns the single line of section that states
// every needle together, and fails when no such line exists.
func sessionClassificationGuideLine(t *testing.T, section, heading, what string, needles ...string) string {
	t.Helper()
	for line := range strings.SplitSeq(section, "\n") {
		complete := true
		for _, needle := range needles {
			if !strings.Contains(line, needle) {
				complete = false
				break
			}
		}
		if complete {
			return line
		}
	}
	t.Fatalf("%s %q has no %s stating %s on one line; a reader must not be able to find a stale or "+
		"invented value next to the real one",
		sessionClassificationDocRel, heading, what, strings.Join(needles, " and "))
	return ""
}

// sessionClassificationGuideBulletBlock returns the heading's line plus every
// markdown bullet that follows it, each with its wrapped continuation lines, so a
// "never do X" list can be required as one closed block rather than as scattered
// words. Continuation lines matter: a prohibition whose second half wrapped onto
// the next line must not read as absent.
func sessionClassificationGuideBulletBlock(t *testing.T, section, heading, anchor string) string {
	t.Helper()
	var block strings.Builder
	block.WriteString(sessionClassificationGuideLine(t, section, heading, "anchor line "+anchor, anchor))
	inBullet := false
	for line := range strings.SplitSeq(section, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- "):
			block.WriteString("\n" + line)
			inBullet = true
		case inBullet && trimmed != "" && line != trimmed:
			block.WriteString("\n" + line)
		default:
			inBullet = false
		}
	}
	return block.String()
}

// sessionClassificationGuideFence is one fenced code block of a guide section.
type sessionClassificationGuideFence struct {
	// Language is the fence's declared info string. A bare fence leaves a reader
	// unable to tell a config snippet from a log line.
	Language string
	// Body is the fenced text.
	Body string
	// Line is the 1-based line the fence opens on, for failure messages.
	Line int
}

// sessionClassificationGuideFences returns every fenced code block of section.
func sessionClassificationGuideFences(section string) []sessionClassificationGuideFence {
	var fences []sessionClassificationGuideFence
	var current []string
	var open *sessionClassificationGuideFence
	number := 0
	for line := range strings.SplitSeq(section, "\n") {
		number++
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") {
			if open != nil {
				current = append(current, line)
			}
			continue
		}
		if open != nil {
			open.Body = strings.Join(current, "\n")
			fences = append(fences, *open)
			current = nil
			open = nil
			continue
		}
		open = &sessionClassificationGuideFence{
			Language: strings.TrimSpace(strings.TrimPrefix(trimmed, "```")),
			Line:     number,
		}
	}
	return fences
}

// sessionClassificationGuideFenceLanguage returns the body of the ordinal-th
// (1-based) fenced block of section, failing when the fence does not declare the
// wanted language.
func sessionClassificationGuideFenceLanguage(t *testing.T, section, heading, language string, ordinal int) string {
	t.Helper()
	fences := sessionClassificationGuideFences(section)
	if ordinal < 1 || ordinal > len(fences) {
		t.Fatalf("%s %q has %d fenced code blocks, cannot read ordinal %d",
			sessionClassificationDocRel, heading, len(fences), ordinal)
	}
	fence := fences[ordinal-1]
	if fence.Language != language {
		t.Fatalf("%s %q code block %d (line %d) declares %q, want %q",
			sessionClassificationDocRel, heading, ordinal, fence.Line, fence.Language, language)
	}
	return fence.Body
}

// sessionClassificationGuideSlice returns the whole guide. Section helpers are
// used wherever a check is about one section; a whole-guide check is for
// prohibitions that must hold everywhere, such as publishing a raw identity.
func sessionClassificationGuideSlice(t *testing.T) string {
	t.Helper()
	return readSessionClassificationDoc(t)
}
