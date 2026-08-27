package dbparity

import (
	"fmt"
	"strings"
	"unicode"
)

// ParseFlagWords parses a flag string (such as GO_TEST_FLAGS or a CLI -flags argument)
// into individual argument words using a deliberately minimal, predictable word format.
//
// Format rules:
//   - Whitespace (spaces, tabs, newlines) outside quotes separates words.
//   - Single ('...') and double ("...") quotes group characters into words and are stripped.
//   - Adjacent quoted and unquoted segments concatenate into a single word
//     (e.g., -run="^$" -> -run=^$, -literal=a'b' -> -literal=ab, prefix"mid"suffix -> prefixmidsuffix).
//   - Backslashes ('\') are ALWAYS literal characters (inside and outside quotes), preserving
//     UNC paths (\\server\share), drive roots (C:\), trailing separators (C:\path\), and
//     regular expression escapes (\d+, \s+) without escaping or stripping.
//   - Quote characters cannot be escaped inside their own quote type; users switch quote types
//     to embed the other quote delimiter (e.g., 'he said "hello"' or "it's fine").
//   - Unmatched (unclosed) single or double quotes are rejected with an error.
func ParseFlagWords(s string) ([]string, error) {
	var words []string
	var buf strings.Builder
	inWord := false
	inSingle := false
	inDouble := false

	for _, r := range s {
		if inSingle {
			if r == '\'' {
				inSingle = false
			} else {
				buf.WriteRune(r)
			}
			continue
		}

		if inDouble {
			if r == '"' {
				inDouble = false
			} else {
				buf.WriteRune(r)
			}
			continue
		}

		if unicode.IsSpace(r) {
			if inWord {
				words = append(words, buf.String())
				buf.Reset()
				inWord = false
			}
			continue
		}

		inWord = true

		switch r {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		default:
			buf.WriteRune(r)
		}
	}

	if inSingle {
		return nil, fmt.Errorf("unclosed single quote in %q", s)
	}
	if inDouble {
		return nil, fmt.Errorf("unclosed double quote in %q", s)
	}
	if inWord {
		words = append(words, buf.String())
	}

	return words, nil
}
