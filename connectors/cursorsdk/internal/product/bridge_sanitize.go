package product

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	unixAbsPathRe = regexp.MustCompile(`(?:^|[\s"'=])(/[^/\s"'<>|*?](?:[^"'<>|\s]*))`)
	winAbsPathRe  = regexp.MustCompile(`(?i)(?:^|[\s"'=])([a-z]:\\(?:[^"'<>|\s\\]+\\)*[^"'<>|\s\\]*)`)
	uncPathRe     = regexp.MustCompile(`(?i)(?:^|[\s"'=])(\\\\[^"'<>|\s\\]+\\[^"'<>|\s]+)`)
	skKeyishRe    = regexp.MustCompile(`sk-[A-Za-z0-9_-]+`)
	crsrKeyishRe  = regexp.MustCompile(`crsr_[A-Za-z0-9_-]+`)
)

func sanitizeBridgeDiag(raw []byte, apiKey string) string {
	if len(raw) > MaxStderrRetainBytes {
		raw = raw[len(raw)-MaxStderrRetainBytes:]
	}
	var out []byte
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		switch {
		case r == '\t', r == '\n', r == '\r':
			out = utf8.AppendRune(out, r)
		case r >= 32 && r != 127:
			out = utf8.AppendRune(out, r)
		}
		i += size
	}
	s := strings.TrimSpace(string(out))
	if apiKey != "" {
		s = strings.ReplaceAll(s, apiKey, "[REDACTED]")
	}
	s = redactKeyishSecrets(s)
	s = redactAbsolutePaths(s)
	s = redactPromptLike(s)
	if len(s) > MaxStderrRetainBytes {
		s = s[len(s)-MaxStderrRetainBytes:]
	}
	return s
}

func redactAbsolutePaths(s string) string {
	s = unixAbsPathRe.ReplaceAllStringFunc(s, func(m string) string {
		return replacePathKeepPrefix(m, "[PATH]")
	})
	s = winAbsPathRe.ReplaceAllStringFunc(s, func(m string) string {
		return replacePathKeepPrefix(m, "[PATH]")
	})
	s = uncPathRe.ReplaceAllStringFunc(s, func(m string) string {
		return replacePathKeepPrefix(m, "[PATH]")
	})
	if filepath.IsAbs(s) {
		return "[PATH]"
	}
	return s
}

func replacePathKeepPrefix(m, repl string) string {
	if m == "" {
		return repl
	}
	switch m[0] {
	case ' ', '\t', '\n', '\r', '"', '\'', '=':
		return string(m[0]) + repl
	default:
		return repl
	}
}

func redactPromptLike(s string) string {
	const mark = "prompt:"
	lower := strings.ToLower(s)
	if i := strings.Index(lower, mark); i >= 0 {
		return s[:i] + "prompt:[REDACTED]"
	}
	return s
}

func redactToolLike(s string) string {
	lower := strings.ToLower(s)
	marks := []string{"tool_call", "args:", "arguments:", `"content":`, "content:"}
	cut := -1
	for _, mark := range marks {
		if i := strings.Index(lower, mark); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut < 0 {
		return s
	}
	return s[:cut] + "[REDACTED]"
}

func sanitizeWarningMessage(msg, apiKey string) string {
	s := sanitizeBridgeDiag([]byte(msg), apiKey)
	s = redactToolLike(s)
	if len(s) > MaxStderrRetainBytes {
		s = s[:MaxStderrRetainBytes]
	}
	return s
}

func redactKeyishSecrets(s string) string {
	s = skKeyishRe.ReplaceAllString(s, "[REDACTED]")
	s = crsrKeyishRe.ReplaceAllString(s, "[REDACTED]")
	return s
}
