package server

import (
	"testing"
	"time"
)

func TestMroundRTT(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		modulus  time.Duration
		expected time.Duration
	}{
		{
			name:     "Test Case 1",
			duration: 12 * time.Millisecond,
			modulus:  5 * time.Millisecond,
			expected: 10 * time.Millisecond,
		},
		{
			name:     "Test Case 2",
			duration: 27 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
		},
		{
			name:     "Test Case 3",
			duration: 25 * time.Millisecond,
			modulus:  15 * time.Millisecond,
			expected: 30 * time.Millisecond,
		},
		// Add more test cases as needed
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mroundRTT(tt.duration, tt.modulus)
			if result != tt.expected {
				t.Errorf("Expected %v, but got %v", tt.expected, result)
			}
		})
	}
}

func TestRTTweightedPopulationComparison(t *testing.T) {
	tests := []struct {
		name     string
		i        time.Duration
		j        time.Duration
		o        int
		p        int
		expected bool
	}{
		{
			name:     "Test Case 1",
			i:        100 * time.Millisecond,
			j:        80 * time.Millisecond,
			o:        10,
			p:        5,
			expected: true,
		},
		{
			name:     "Test Case 2",
			i:        80 * time.Millisecond,
			j:        100 * time.Millisecond,
			o:        5,
			p:        10,
			expected: false,
		},
		{
			name:     "Test Case 3",
			i:        90 * time.Millisecond,
			j:        90 * time.Millisecond,
			o:        5,
			p:        5,
			expected: false,
		},
		// Add more test cases as needed
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RTTweightedPopulationCmp(tt.i, tt.j, tt.o, tt.p)
			if result != tt.expected {
				t.Errorf("Expected %v, but got %v", tt.expected, result)
			}
		})
	}
}
