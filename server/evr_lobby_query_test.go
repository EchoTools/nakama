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
			name:     "elements with special characters",
			elems:    []string{"test-1", "test[2", "test+3", "test*4", "test(5"},
			expected: "/(test\\-1|test\\[2|test\\+3|test\\*4|test\\(5)/",
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
