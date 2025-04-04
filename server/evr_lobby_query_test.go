package server

import (
	"strings"
	"testing"
)

func TestQuery_Join(t *testing.T) {
	testCases := []struct {
		name     string
		elems    []string
		expected string
	}{
		{
			name:  "empty elements",
			elems: []string{},

			expected: "",
		},
		{
			name:  "single element",
			elems: []string{"test"},

			expected: "\btest\b",
		},
		{
			name:  "multiple elements",
			elems: []string{"test1", "test2", "test3"},

			expected: "\btest1\b,\btest2\b,\btest3\b",
		},
		{
			name:  "elements with special characters",
			elems: []string{"test-1", "test[2", "test+3"},

			expected: "\btest\\-1\b,\btest\\[2\b,\btest\\+3\b",
		},
		{
			name:  "different separator",
			elems: []string{"test1", "test2"},

			expected: "\btest1\b|\btest2\b",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := Query.MatchItem(tc.elems)
			if result != tc.expected {
				t.Errorf("Expected: %q, got: %q", tc.expected, result)
			}
		})
	}
}

func TestQuery_MatchDelimitedItem(t *testing.T) {
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
			expected: "/(^|,)(test)(,|$)/",
		},
		{
			name:     "multiple elements",
			elems:    []string{"test1", "test2", "test3"},
			expected: "/(^|,)(test1|test2|test3)(,|$)/",
		},
		{
			name:     "elements with special characters",
			elems:    []string{"test-1", "test[2", "test+3"},
			expected: "/(^|,)(test\\-1|test\\[2|test\\+3)[,\\$](,|$)/",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := Query.MatchDelimitedItem(tc.elems, ",")
			if result != tc.expected {
				t.Errorf("Expected: %q, got: %q", tc.expected, result)
			}
		})
	}
}

func FuzzQuery_Join(f *testing.F) {
	f.Add("test1,test2,test3", true)
	f.Fuzz(func(t *testing.T, elems string, exactMatchOnly bool) {
		result := Query.MatchItem(strings.Split(elems, ","))
		if len(elems) > 0 {
			if !strings.Contains(result, "\b") {
				t.Errorf("Expected backtick to be present")
			}
		}
	})
}
