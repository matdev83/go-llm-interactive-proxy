package codexreasoning

import "regexp"

var (
	emptyHTMLComment        = regexp.MustCompile(`<!--\s*-->`)
	trailingHTMLCommentOpen = regexp.MustCompile(`<!--\s*$`)
	leadingHTMLCommentClose = regexp.MustCompile(`^\s*-->\s*`)
)

// StripEmptyHTMLCommentMarkers removes private empty HTML comment markers from
// Codex reasoning summaries before they become client-visible reasoning.
func StripEmptyHTMLCommentMarkers(text string) string {
	text = emptyHTMLComment.ReplaceAllString(text, "")
	text = trailingHTMLCommentOpen.ReplaceAllString(text, "")
	text = leadingHTMLCommentClose.ReplaceAllString(text, "")
	return text
}
