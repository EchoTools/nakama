package server

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// --- Bluge regex injection on the login path --------------------------------
//
// LoginDeniedClientIPAddressSearch interpolates the client IP into a Bluge
// regex query. It used Query.QuoteStringValue, which escapes with a single
// backslash; Bluge's query lexer strips a single backslash from every
// character in its reserved set, so the metacharacter arrives at the regex
// engine bare. The repo already documents this in
// TestRegexEscapeForBluge_QuoteStringValueBroken. regexEscapeForBluge is the
// correct helper and double-escapes exactly those characters.
//
// This function is called on EVERY login (evr_pipeline_login.go), with a value
// a client can influence, so a crafted address is either a match-everything
// query over the whole login-cache index or a query parse failure.

// queryCapturingNK records the query strings passed to StorageIndexList and
// returns no results, so a caller's paging loop terminates on the first call.
type queryCapturingNK struct {
	runtime.NakamaModule
	queries []string
}

func (n *queryCapturingNK) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	n.queries = append(n.queries, query)
	return &api.StorageObjects{}, "", nil
}

// blugeRegexBody returns the regex source from a `+field:/pattern/` clause, as
// the Bluge lexer would hand it to the regex engine.
func blugeRegexBody(t *testing.T, query string) string {
	t.Helper()
	start := strings.Index(query, ":/")
	if start < 0 || !strings.HasSuffix(query, "/") {
		t.Fatalf("query %q is not a /regex/ clause", query)
	}
	return simulateBlugeUnescape(query[start+2 : len(query)-1])
}

func TestLoginDeniedClientIPAddressSearch_EscapesRegexMetacharacters(t *testing.T) {
	// Each of these is a value that reaches the query builder as a "client IP".
	// They are the shapes an attacker sends, not the shapes a real address has.
	testCases := []struct {
		name  string
		input string
	}{
		{"match-everything", "[^x]*"},
		{"alternation", "1.2.3.4|.*"},
		{"unbalanced group", "1.2.3.4("},
		{"quantifier", "1.2.3.4+"},
		{"character class", "1.2.3.4[a-z]"},
		{"catastrophic backtracking", "(a+)+$"},
		{"plain address still works", "203.0.113.7"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nk := &queryCapturingNK{}
			if _, err := LoginDeniedClientIPAddressSearch(context.Background(), nk, tc.input); err != nil {
				t.Fatalf("search returned an error: %v", err)
			}
			if len(nk.queries) != 1 {
				t.Fatalf("expected exactly one index query, got %d", len(nk.queries))
			}
			query := nk.queries[0]

			pattern := blugeRegexBody(t, query)
			re, err := regexp.Compile(pattern)
			if err != nil {
				t.Fatalf("input %q produced an uncompilable regex %q (query %q): %v", tc.input, pattern, query, err)
			}

			// The pattern must be a literal: it matches the input and nothing
			// else. A single foreign string that matches is a match-everything
			// query over the entire login-cache index.
			if !re.MatchString(tc.input) {
				t.Errorf("pattern %q does not match its own input %q (query %q)", pattern, tc.input, query)
			}
			for _, foreign := range []string{"198.51.100.200", "", "zzz", "a"} {
				if foreign == tc.input {
					continue
				}
				if re.MatchString(foreign) {
					t.Errorf("input %q escaped to pattern %q, which also matches the unrelated value %q (query %q)", tc.input, pattern, foreign, query)
				}
			}
		})
	}
}

// The audit swapped the same substitution into every other /regex/ clause that
// was built with QuoteStringValue. Those all carry UUIDs or pattern-validated
// feature names, so the swap must be a no-op for those value shapes -- this
// pins that the audit changed no behaviour where it was applied.
func TestRegexEscapeMatchesQuoteStringValueForSafeValues(t *testing.T) {
	for _, input := range []string{
		"7d8c7e37-2d69-4b57-8f0d-6b8d8b6b5e2a", // group / user UUID
		"echo_taxi",                            // feature name, ^[a-z0-9_]+$
		"203.0.113.7",                          // plain IPv4 address
		"default",
	} {
		t.Run(input, func(t *testing.T) {
			// Semantic, not byte, equivalence: the two helpers differ on '_'
			// (QuoteStringValue emits "\_", which Bluge does not strip and Go's
			// regexp reads as a literal underscore). What must not change is the
			// language the pattern accepts.
			for name, escaped := range map[string]string{
				"regexEscapeForBluge": regexEscapeForBluge(input),
				"QuoteStringValue":    Query.QuoteStringValue(input),
			} {
				re, err := regexp.Compile(simulateBlugeUnescape(escaped))
				if err != nil {
					t.Fatalf("%s(%q) does not compile after Bluge unescape: %v", name, input, err)
				}
				if !re.MatchString(input) {
					t.Errorf("%s(%q) no longer matches its own input", name, input)
				}
				if re.MatchString("some unrelated value") {
					t.Errorf("%s(%q) matches an unrelated value", name, input)
				}
			}
		})
	}
}

// Any other call site interpolating a value into a /regex/ clause with
// QuoteStringValue has the identical defect. This pins the audit: the helper
// that survives Bluge's lexer is regexEscapeForBluge, and QuoteStringValue is
// not it.
func TestQuoteStringValueIsNotARegexEscape(t *testing.T) {
	for _, input := range []string{"[^x]*", "1.2.3.4(", "a+b"} {
		quoted := Query.QuoteStringValue(input)
		afterBluge := simulateBlugeUnescape(quoted)
		re, err := regexp.Compile(afterBluge)
		if err == nil && !re.MatchString("some unrelated value") && re.MatchString(input) {
			t.Errorf("QuoteStringValue(%q) unexpectedly produced a safe literal regex %q; if this now holds, the audit assumption in this file needs revisiting", input, afterBluge)
		}
	}
}
