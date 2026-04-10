package server

import (
	"regexp"
	"strings"
	"testing"
)

// simulateBlugeUnescape strips one layer of backslash escaping for Bluge
// reserved characters, simulating what Bluge's query lexer does before
// handing the pattern to Go's regexp engine.
func simulateBlugeUnescape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && strings.ContainsRune(blugeReservedChars, rune(s[i+1])) {
			// Strip the backslash; keep the next char.
			b.WriteByte(s[i+1])
			i++
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func TestRegexEscapeForBluge(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string // exact expected output from regexEscapeForBluge
	}{
		{
			name:     "plain alphanumeric",
			input:    "foobar123",
			expected: "foobar123",
		},
		{
			name:  "hyphen and underscore",
			input: "foo-bar_baz",
			// '-' is Bluge reserved but not regex metachar → single backslash
			// '_' is neither → no escaping
			expected: `foo\-bar_baz`,
		},
		{
			name:  "parentheses",
			input: "foo-bar-(baz",
			// '-' → \-  (Bluge reserved, not regex metachar)
			// '(' → \\( (both Bluge reserved AND regex metachar: double-backslash)
			expected: `foo\-bar\-\\(baz`,
		},
		{
			name:  "brackets",
			input: "foo[bar]",
			// '[' and ']' are both Bluge reserved AND regex metachar → double-backslash
			expected: `foo\\[bar\\]`,
		},
		{
			name:  "plus and star",
			input: "a+b*c",
			// '+' and '*' are both Bluge reserved AND regex metachar → double-backslash
			expected: `a\\+b\\*c`,
		},
		{
			name:  "colon and space",
			input: "deathman :3",
			// ' ' and ':' are Bluge reserved but not regex metachar → single backslash
			expected: `deathman\ \:3`,
		},
		{
			name:  "pipe (Bluge reserved and regex metachar)",
			input: "a|b",
			// '|' is both Bluge reserved AND regex metachar → double-backslash
			expected: `a\\|b`,
		},
		{
			name:     "underscore passthrough",
			input:    "foo_bar",
			expected: "foo_bar",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := regexEscapeForBluge(tc.input)
			if got != tc.expected {
				t.Errorf("regexEscapeForBluge(%q)\n  got:  %q\n  want: %q", tc.input, got, tc.expected)
			}
		})
	}
}

// TestRegexEscapeForBluge_AfterBlugeUnescape verifies that after simulating
// Bluge's lexer (stripping one backslash from reserved chars), the resulting
// pattern is a valid Go regexp that matches the original input literally.
func TestRegexEscapeForBluge_AfterBlugeUnescape(t *testing.T) {
	inputs := []string{
		"foo-bar-(baz",
		"foo_bar",
		"deathman :3",
		"a+b*c",
		"foo[bar]",
		`+-=&|><!(){}[]^"~*?:\/ `,
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			escaped := regexEscapeForBluge(input)
			// Simulate Bluge stripping one backslash layer from reserved chars.
			afterBluge := simulateBlugeUnescape(escaped)

			// Verify it compiles and matches the original input literally.
			re, err := regexp.Compile(afterBluge)
			if err != nil {
				t.Fatalf("after Bluge unescape, pattern %q fails regexp.Compile: %v", afterBluge, err)
			}
			if !re.MatchString(input) {
				t.Errorf("after Bluge unescape, pattern %q does not match input %q", afterBluge, input)
			}
		})
	}
}

// TestRegexEscapeForBluge_QuoteStringValueBroken demonstrates the bug:
// Query.QuoteStringValue does not produce valid regex patterns for Bluge
// when the input contains Bluge reserved characters that are also regex
// metacharacters (e.g. (, ), [, +, *).
func TestRegexEscapeForBluge_QuoteStringValueBroken(t *testing.T) {
	// QuoteStringValue only single-escapes. After Bluge strips that
	// single backslash, regex metacharacters are left bare → parse error.
	problematic := []string{
		"foo-bar-(baz",
		"a+b*c",
		"foo[bar]",
	}
	for _, input := range problematic {
		t.Run(input, func(t *testing.T) {
			quoted := Query.QuoteStringValue(input)
			afterBluge := simulateBlugeUnescape(quoted)
			_, err := regexp.Compile(afterBluge)
			if err == nil {
				t.Skipf("QuoteStringValue(%q) happens to produce valid regex after Bluge unescape; not a failing case", input)
			}
			// Confirm regexEscapeForBluge fixes it.
			escaped := regexEscapeForBluge(input)
			afterBluge2 := simulateBlugeUnescape(escaped)
			_, err2 := regexp.Compile(afterBluge2)
			if err2 != nil {
				t.Errorf("regexEscapeForBluge(%q) also fails after Bluge unescape: %v", input, err2)
			}
		})
	}
}

func TestQuery_JoinAsRegex(t *testing.T) {
	testCases := []struct {
		name     string
		elems    []string
		expected string
	}{
		{
			name:     "empty elements",
			elems:    []string{},
			expected: "",
		},
		{
			name:     "single element",
			elems:    []string{"test"},
			expected: "/(test)/",
		},
		{
			name:     "multiple elements",
			elems:    []string{"test1", "test2", "test3"},
			expected: "/(test1|test2|test3)/",
		},
		{
			name:  "elements with special characters",
			elems: []string{"test-1", "test[2", "test+3", "test*4", "test(5"},
			// Bluge's query lexer strips single backslashes from its reserved chars
			// (like +, [, *, ()), so those need double-backslash escaping to survive.
			// '-' is a Bluge reserved char (not a regex metachar) so gets single-backslash.
			expected: "/(test\\-1|test\\\\[2|test\\\\+3|test\\\\*4|test\\\\(5)/",
		},
		{
			name:  "elements with colon (Bluge field separator)",
			elems: []string{"deathman :3"},
			// ':' and space are Bluge reserved chars but not regex metacharacters,
			// so they get single-backslash escaping to prevent Bluge query parse errors.
			expected: "/(deathman\\ \\:3)/",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := Query.CreateMatchPattern(tc.elems)
			if result != tc.expected {
				t.Errorf("Expected: %q, got: %q", tc.expected, result)
			}
		})
	}
}
