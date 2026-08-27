package dbparity_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/matdev83/go-llm-interactive-proxy/internal/testkit/dbparity"
)

func TestParseFlagWords_Success(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty input string returns nil",
			input:    "",
			expected: nil,
		},
		{
			name:     "whitespace only returns nil",
			input:    "   \t\r\n  ",
			expected: nil,
		},
		{
			name:     "standard flags without quotes",
			input:    "-timeout=10m -parallel=8 -count=1",
			expected: []string{"-timeout=10m", "-parallel=8", "-count=1"},
		},
		{
			name:     "double quoted flag value with spaces",
			input:    `-run "Test Name" -count=1`,
			expected: []string{"-run", "Test Name", "-count=1"},
		},
		{
			name:     "single quoted flag value with spaces",
			input:    `-run 'Test Name'`,
			expected: []string{"-run", "Test Name"},
		},
		{
			name:     "adjacent single and unquoted segments",
			input:    `-literal=a'b'`,
			expected: []string{"-literal=ab"},
		},
		{
			name:     "adjacent double and unquoted segments",
			input:    `-run="Test Name"`,
			expected: []string{"-run=Test Name"},
		},
		{
			name:     "multiple adjacent quoted segments",
			input:    `'a'"b"'c'`,
			expected: []string{"abc"},
		},
		{
			name:     "prefix and suffix surrounding quotes",
			input:    `prefix"mid"suffix`,
			expected: []string{"prefixmidsuffix"},
		},
		{
			name:     "empty double quoted word",
			input:    `""`,
			expected: []string{""},
		},
		{
			name:     "empty single quoted word",
			input:    `''`,
			expected: []string{""},
		},
		{
			name:     "multiple empty quoted words separated by space",
			input:    `"" ''`,
			expected: []string{"", ""},
		},
		{
			name:     "unquoted word containing empty quote",
			input:    `a""b`,
			expected: []string{"ab"},
		},
		{
			name:     "flag with empty quoted value",
			input:    `-flag=""`,
			expected: []string{"-flag="},
		},
		{
			name:     "UNC network path preserves double leading backslashes",
			input:    `\\server\share\subfolder`,
			expected: []string{`\\server\share\subfolder`},
		},
		{
			name:     "Windows drive root preserves trailing backslash without error",
			input:    `C:\`,
			expected: []string{`C:\`},
		},
		{
			name:     "Windows path outside quotes",
			input:    `-dir=C:\Users\Mateusz\source\repos`,
			expected: []string{`-dir=C:\Users\Mateusz\source\repos`},
		},
		{
			name:     "quoted path ending separator using single quotes",
			input:    `-dir='C:\path\'`,
			expected: []string{`-dir=C:\path\`},
		},
		{
			name:     "quoted path ending separator using double quotes",
			input:    `-dir="C:\path\"`,
			expected: []string{`-dir=C:\path\`},
		},
		{
			name:     "regex with backslashes inside single quotes",
			input:    `-run='^Test\d+\s+$'`,
			expected: []string{`-run=^Test\d+\s+$`},
		},
		{
			name:     "regex with backslashes inside double quotes",
			input:    `-run="^Test\d+\s+$"`,
			expected: []string{`-run=^Test\d+\s+$`},
		},
		{
			name:     "regex flags separated by space",
			input:    `-run '\s+' -count=1`,
			expected: []string{"-run", `\s+`, "-count=1"},
		},
		{
			name:     "single quote inside double quotes (switch quote types)",
			input:    `-msg="it's fine"`,
			expected: []string{`-msg=it's fine`},
		},
		{
			name:     "double quote inside single quotes (switch quote types)",
			input:    `-msg='he said "hello"'`,
			expected: []string{`-msg=he said "hello"`},
		},
		{
			name:     "literal backslashes outside quotes",
			input:    `foo\\bar foo\bar -flag=\`,
			expected: []string{`foo\\bar`, `foo\bar`, `-flag=\`},
		},
		{
			name:     "command substitution syntax is treated as literal",
			input:    `$(echo evil) ` + "`date`",
			expected: []string{"$(echo", "evil)", "`date`"},
		},
		{
			name:     "environment variable syntax is treated as literal",
			input:    `$FOO ${BAR} "$BAZ"`,
			expected: []string{"$FOO", "${BAR}", "$BAZ"},
		},
		{
			name:     "extra whitespace between words is collapsed",
			input:    "   -a   -b    -c   ",
			expected: []string{"-a", "-b", "-c"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbparity.ParseFlagWords(tc.input)
			if err != nil {
				t.Fatalf("ParseFlagWords(%q) returned unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.expected) {
				t.Errorf("ParseFlagWords(%q)\ngot:  %#v\nwant: %#v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestParseFlagWords_Errors(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		errorContains string
	}{
		{
			name:          "unclosed single quote",
			input:         `-run 'Test Name`,
			errorContains: "unclosed single quote",
		},
		{
			name:          "unclosed double quote",
			input:         `-run "Test Name`,
			errorContains: "unclosed double quote",
		},
		{
			name:          "unclosed single quote inside word",
			input:         `-flag=prefix'suffix`,
			errorContains: "unclosed single quote",
		},
		{
			name:          "unclosed double quote inside word",
			input:         `-flag=prefix"suffix`,
			errorContains: "unclosed double quote",
		},
		{
			name:          "unclosed quote with trailing path separator",
			input:         `-dir="C:\path\`,
			errorContains: "unclosed double quote",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dbparity.ParseFlagWords(tc.input)
			if err == nil {
				t.Fatalf("ParseFlagWords(%q) expected error, got: %#v", tc.input, got)
			}
			if !strings.Contains(err.Error(), tc.errorContains) {
				t.Errorf("ParseFlagWords(%q) error %q does not contain %q", tc.input, err.Error(), tc.errorContains)
			}
		})
	}
}
