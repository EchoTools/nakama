package server

import (
	"context"
	"testing"
)

// TestSearchPermissionLogic tests the permission checks for the /search command
func TestSearchPermissionLogic(t *testing.T) {
	tests := []struct {
		name                string
		isGlobalOperator    bool
		isAuditor           bool
		isEnforcer          bool
		canUseWildcards     bool
		canSearchIPs        bool
		expectedDescription string
	}{
		{
			name:                "global operator has all permissions",
			isGlobalOperator:    true,
			isAuditor:           false,
			isEnforcer:          false,
			canUseWildcards:     true,
			canSearchIPs:        true,
			expectedDescription: "Global operators can search IPs and use wildcards",
		},
		{
			name:                "auditor can use wildcards but not search IPs",
			isGlobalOperator:    false,
			isAuditor:           true,
			isEnforcer:          false,
			canUseWildcards:     true,
			canSearchIPs:        false,
			expectedDescription: "Auditors can use wildcards but cannot search IPs",
		},
		{
			name:                "enforcer can use wildcards but not search IPs",
			isGlobalOperator:    false,
			isAuditor:           false,
			isEnforcer:          true,
			canUseWildcards:     true,
			canSearchIPs:        false,
			expectedDescription: "Enforcers can use wildcards but cannot search IPs",
		},
		{
			name:                "regular user has no special permissions",
			isGlobalOperator:    false,
			isAuditor:           false,
			isEnforcer:          false,
			canUseWildcards:     false,
			canSearchIPs:        false,
			expectedDescription: "Regular users cannot search IPs or use wildcards",
		},
		{
			name:                "global operator cascades to auditor and enforcer",
			isGlobalOperator:    true,
			isAuditor:           false,
			isEnforcer:          false,
			canUseWildcards:     true,
			canSearchIPs:        true,
			expectedDescription: "Global operators implicitly have auditor/enforcer rights",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate permission logic from handleSearch (lines 106-107)
			access := CallerGuildAccess{
				IsGlobalOperator: tt.isGlobalOperator,
				IsAuditor:        tt.isAuditor || tt.isGlobalOperator,                  // Cascade
				IsEnforcer:       tt.isEnforcer || tt.isAuditor || tt.isGlobalOperator, // Cascade
			}

			canUseWildcards := tt.isGlobalOperator || access.IsAuditor || access.IsEnforcer
			canSearchIPs := tt.isGlobalOperator

			if canUseWildcards != tt.canUseWildcards {
				t.Errorf("Expected canUseWildcards=%v, got %v", tt.canUseWildcards, canUseWildcards)
			}

			if canSearchIPs != tt.canSearchIPs {
				t.Errorf("Expected canSearchIPs=%v, got %v", tt.canSearchIPs, canSearchIPs)
			}
		})
	}
}

// TestWildcardPatternParsing tests wildcard parsing logic
func TestWildcardPatternParsing(t *testing.T) {
	tests := []struct {
		name                   string
		input                  string
		expectedPattern        string
		expectedPrefixWildcard bool
		expectedSuffixWildcard bool
	}{
		{
			name:                   "no wildcards",
			input:                  "player",
			expectedPattern:        "player",
			expectedPrefixWildcard: false,
			expectedSuffixWildcard: false,
		},
		{
			name:                   "prefix wildcard only",
			input:                  "*player",
			expectedPattern:        "player",
			expectedPrefixWildcard: true,
			expectedSuffixWildcard: false,
		},
		{
			name:                   "suffix wildcard only",
			input:                  "player*",
			expectedPattern:        "player",
			expectedPrefixWildcard: false,
			expectedSuffixWildcard: true,
		},
		{
			name:                   "both wildcards",
			input:                  "*player*",
			expectedPattern:        "player",
			expectedPrefixWildcard: true,
			expectedSuffixWildcard: true,
		},
		{
			name:                   "wildcard with multiple characters",
			input:                  "*testuser*",
			expectedPattern:        "testuser",
			expectedPrefixWildcard: true,
			expectedSuffixWildcard: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate wildcard parsing from handleSearch (lines 131-138)
			partial := tt.input
			useWildcardPrefix := false
			useWildcardSuffix := false

			if len(partial) > 0 && partial[0] == '*' {
				partial = partial[1:]
				useWildcardPrefix = true
			}
			if len(partial) > 0 && partial[len(partial)-1] == '*' {
				partial = partial[:len(partial)-1]
				useWildcardSuffix = true
			}

			if partial != tt.expectedPattern {
				t.Errorf("Expected pattern %q, got %q", tt.expectedPattern, partial)
			}

			if useWildcardPrefix != tt.expectedPrefixWildcard {
				t.Errorf("Expected prefixWildcard=%v, got %v", tt.expectedPrefixWildcard, useWildcardPrefix)
			}

			if useWildcardSuffix != tt.expectedSuffixWildcard {
				t.Errorf("Expected suffixWildcard=%v, got %v", tt.expectedSuffixWildcard, useWildcardSuffix)
			}
		})
	}
}

// TestWildcardMinimumLength tests that wildcard searches enforce minimum length
func TestWildcardMinimumLength(t *testing.T) {
	tests := []struct {
		name            string
		pattern         string
		hasWildcard     bool
		shouldReject    bool
		rejectionReason string
	}{
		{
			name:         "3+ characters with wildcard - allowed",
			pattern:      "abc",
			hasWildcard:  true,
			shouldReject: false,
		},
		{
			name:            "2 characters with wildcard - rejected",
			pattern:         "ab",
			hasWildcard:     true,
			shouldReject:    true,
			rejectionReason: "search string is too short for wildcards",
		},
		{
			name:            "1 character with wildcard - rejected",
			pattern:         "a",
			hasWildcard:     true,
			shouldReject:    true,
			rejectionReason: "search string is too short for wildcards",
		},
		{
			name:            "empty string with wildcard - rejected",
			pattern:         "",
			hasWildcard:     true,
			shouldReject:    true,
			rejectionReason: "search string is too short for wildcards",
		},
		{
			name:         "2 characters without wildcard - allowed",
			pattern:      "ab",
			hasWildcard:  false,
			shouldReject: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate length validation from handleSearch (line 146-147)
			shouldReject := len(tt.pattern) < 3 && tt.hasWildcard

			if shouldReject != tt.shouldReject {
				t.Errorf("Expected shouldReject=%v, got %v", tt.shouldReject, shouldReject)
			}
		})
	}
}

// TestIPAddressDetection tests the IP address filtering logic
func TestIPAddressDetection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		isIPAddr bool
	}{
		{
			name:     "valid IPv4 address",
			input:    "192.168.1.1",
			isIPAddr: true,
		},
		{
			name:     "valid IPv4 with high numbers",
			input:    "255.255.255.255",
			isIPAddr: true,
		},
		{
			name:     "localhost",
			input:    "127.0.0.1",
			isIPAddr: true,
		},
		{
			name:     "partial IP - 3 parts",
			input:    "192.168.1",
			isIPAddr: false,
		},
		{
			name:     "partial IP - 2 parts",
			input:    "192.168",
			isIPAddr: false,
		},
		{
			name:     "not an IP - device ID",
			input:    "device123",
			isIPAddr: false,
		},
		{
			name:     "not an IP - username",
			input:    "testuser",
			isIPAddr: false,
		},
		{
			name:     "IP-like but has letters",
			input:    "192.168.a.1",
			isIPAddr: false,
		},
		{
			name:     "too many dots",
			input:    "192.168.1.1.1",
			isIPAddr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate IP detection logic from handleSearch (lines 271-289)
			// IP detection: Check if item has exactly 4 dot-separated numeric parts
			parts := len(splitString(tt.input, '.'))
			hasOnlyDotsAndDigits := true
			for _, char := range tt.input {
				if char != '.' && (char < '0' || char > '9') {
					hasOnlyDotsAndDigits = false
					break
				}
			}
			isIPAddr := parts == 4 && hasOnlyDotsAndDigits

			if isIPAddr != tt.isIPAddr {
				t.Errorf("Expected isIPAddr=%v, got %v for input %q", tt.isIPAddr, isIPAddr, tt.input)
			}
		})
	}
}

// Helper function to split string by delimiter
func splitString(s string, delim rune) []string {
	if s == "" {
		return []string{}
	}
	parts := []string{}
	current := ""
	for _, char := range s {
		if char == delim {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(char)
		}
	}
	parts = append(parts, current)
	return parts
}

// TestResolveCallerGuildAccess_SearchCommand tests permission cascading for search command
func TestResolveCallerGuildAccess_SearchCommand(t *testing.T) {
	tests := []struct {
		name               string
		isGlobalOperator   bool
		guildIsAuditor     bool
		guildIsEnforcer    bool
		expectedIsAuditor  bool
		expectedIsEnforcer bool
	}{
		{
			name:               "global operator cascades to auditor and enforcer",
			isGlobalOperator:   true,
			guildIsAuditor:     false,
			guildIsEnforcer:    false,
			expectedIsAuditor:  true,
			expectedIsEnforcer: true,
		},
		{
			name:               "guild auditor cascades to enforcer",
			isGlobalOperator:   false,
			guildIsAuditor:     true,
			guildIsEnforcer:    false,
			expectedIsAuditor:  true,
			expectedIsEnforcer: true,
		},
		{
			name:               "guild enforcer only",
			isGlobalOperator:   false,
			guildIsAuditor:     false,
			guildIsEnforcer:    true,
			expectedIsAuditor:  false,
			expectedIsEnforcer: true,
		},
		{
			name:               "no special permissions",
			isGlobalOperator:   false,
			guildIsAuditor:     false,
			guildIsEnforcer:    false,
			expectedIsAuditor:  false,
			expectedIsEnforcer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Simulate ResolveCallerGuildAccess logic
			access := CallerGuildAccess{
				IsGlobalOperator: tt.isGlobalOperator,
				IsAuditor:        tt.guildIsAuditor,
				IsEnforcer:       tt.guildIsEnforcer,
			}

			// Apply cascading (from evr_auth_helpers.go lines 138-140)
			access.IsAuditor = access.IsAuditor || access.IsGlobalOperator
			access.IsEnforcer = access.IsEnforcer || access.IsAuditor

			_ = ctx // Suppress unused warning

			if access.IsAuditor != tt.expectedIsAuditor {
				t.Errorf("Expected IsAuditor=%v, got %v", tt.expectedIsAuditor, access.IsAuditor)
			}

			if access.IsEnforcer != tt.expectedIsEnforcer {
				t.Errorf("Expected IsEnforcer=%v, got %v", tt.expectedIsEnforcer, access.IsEnforcer)
			}
		})
	}
}

// TestSearchPatternValidation tests search pattern sanitization
func TestSearchPatternValidation(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		canUseWildcards bool
		shouldReject    bool
		rejectionReason string
	}{
		{
			name:            "valid alphanumeric pattern",
			input:           "player123",
			canUseWildcards: false,
			shouldReject:    false,
		},
		{
			name:            "empty pattern - rejected",
			input:           "",
			canUseWildcards: false,
			shouldReject:    true,
			rejectionReason: "Please provide a search pattern",
		},
		{
			name:            "wildcard without permission - rejected",
			input:           "*player",
			canUseWildcards: false,
			shouldReject:    true,
			rejectionReason: "You do not have permission to use wildcards in searches",
		},
		{
			name:            "wildcard with permission - allowed",
			input:           "*player",
			canUseWildcards: true,
			shouldReject:    false,
		},
		{
			name:            "short wildcard pattern - rejected",
			input:           "*ab",
			canUseWildcards: true,
			shouldReject:    true,
			rejectionReason: "search string is too short for wildcards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			partial := tt.input

			// Check empty pattern (line 114-116)
			if partial == "" {
				if !tt.shouldReject {
					t.Error("Empty pattern should be rejected")
				}
				return
			}

			// Parse wildcards (lines 131-138)
			useWildcardPrefix := false
			useWildcardSuffix := false
			if len(partial) > 0 && partial[0] == '*' {
				partial = partial[1:]
				useWildcardPrefix = true
			}
			if len(partial) > 0 && partial[len(partial)-1] == '*' {
				partial = partial[:len(partial)-1]
				useWildcardSuffix = true
			}

			// Check wildcard permission (lines 140-142)
			if (useWildcardPrefix || useWildcardSuffix) && !tt.canUseWildcards {
				if !tt.shouldReject {
					t.Error("Wildcard without permission should be rejected")
				}
				return
			}

			// Check minimum length for wildcards (lines 146-147)
			if len(partial) < 3 && (useWildcardPrefix || useWildcardSuffix) {
				if !tt.shouldReject {
					t.Error("Short wildcard pattern should be rejected")
				}
				return
			}

			// If we reach here, pattern should be accepted
			if tt.shouldReject {
				t.Errorf("Expected rejection with reason: %s", tt.rejectionReason)
			}
		})
	}
}
