package server

import (
	"testing"
)

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
