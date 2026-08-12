package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// --- Shadowed paging cursor -------------------------------------------------
//
// Both LoginDeniedClientIPAddressSearch and LoginHistoryRegexSearch paged the
// login-cache index with
//
//	cursor := ""
//	for {
//	    result, cursor, err := nk.StorageIndexList(ctx, ..., cursor)
//	    ...
//	    if cursor == "" { break }
//	}
//
// The `:=` declares a NEW cursor scoped to the loop body. The outer cursor --
// the one passed back into StorageIndexList -- is never assigned, so it stays
// "" forever: page one is re-fetched, its user IDs re-appended, and the break
// never fires. It only triggers when the result set exceeds one page, so it
// stayed dormant until a query matched enough documents. The correct form is
// 40 lines above in LoginAlternatePatternSearch: `var cursor string` with `=`.
//
// LoginDeniedClientIPAddressSearch is called on EVERY login.

// errCursorLoopRunaway bounds the test. Without it a genuinely non-terminating
// loop hangs the whole suite instead of failing.
var errCursorLoopRunaway = errors.New("paging loop exceeded its call budget")

// pagingIndexNK serves a fixed list of pages, keyed on the cursor it is handed.
// A caller that ignores the returned cursor is served page one forever.
type pagingIndexNK struct {
	runtime.NakamaModule
	pages       [][]string // user IDs per page
	maxCalls    int
	calls       int
	seenCursors []string
}

func (n *pagingIndexNK) StorageIndexList(ctx context.Context, callerID, indexName, query string, limit int, order []string, cursor string) (*api.StorageObjects, string, error) {
	n.calls++
	n.seenCursors = append(n.seenCursors, cursor)
	if n.calls > n.maxCalls {
		return nil, "", errCursorLoopRunaway
	}

	page := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "page-%d", &page); err != nil {
			return nil, "", fmt.Errorf("index served an unrecognised cursor %q", cursor)
		}
	}
	if page >= len(n.pages) {
		return nil, "", fmt.Errorf("index served a cursor past the last page: %q", cursor)
	}

	objects := make([]*api.StorageObject, 0, len(n.pages[page]))
	for _, userID := range n.pages[page] {
		objects = append(objects, &api.StorageObject{UserId: userID})
	}

	next := ""
	if page+1 < len(n.pages) {
		next = fmt.Sprintf("page-%d", page+1)
	}
	return &api.StorageObjects{Objects: objects}, next, nil
}

// threePages is a result set larger than one page: exactly the condition that
// arms the bug. Page size for LoginDeniedClientIPAddressSearch is 10.
func threePages() ([][]string, []string) {
	pages := [][]string{
		{"u01", "u02", "u03", "u04", "u05", "u06", "u07", "u08", "u09", "u10"},
		{"u11", "u12", "u13", "u14", "u15", "u16", "u17", "u18", "u19", "u20"},
		{"u21", "u22"},
	}
	return pages, slices.Concat(pages...)
}

func TestLoginDeniedClientIPAddressSearch_TerminatesAcrossPages(t *testing.T) {
	pages, want := threePages()
	nk := &pagingIndexNK{pages: pages, maxCalls: 10}

	got, err := LoginDeniedClientIPAddressSearch(context.Background(), nk, "203.0.113.7")

	if errors.Is(err, errCursorLoopRunaway) {
		t.Fatalf("paging loop never terminated: %d calls, cursors handed to the index were %v (all empty means the outer cursor is shadowed)", nk.calls, nk.seenCursors)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nk.calls != len(pages) {
		t.Errorf("index queried %d times for %d pages; cursors seen: %v", nk.calls, len(pages), nk.seenCursors)
	}
	if !slices.Equal(got, want) {
		t.Errorf("user IDs = %v, want %v", got, want)
	}
	if want, got := []string{"", "page-1", "page-2"}, nk.seenCursors; !slices.Equal(got, want) {
		t.Errorf("cursors handed back to the index = %v, want %v", got, want)
	}
}

func TestLoginHistoryRegexSearch_TerminatesAcrossPages(t *testing.T) {
	pages, want := threePages()
	nk := &pagingIndexNK{pages: pages, maxCalls: 10}

	got, err := LoginHistoryRegexSearch(context.Background(), nk, "203\\.0\\.113\\.7", 10)

	if errors.Is(err, errCursorLoopRunaway) {
		t.Fatalf("paging loop never terminated: %d calls, cursors handed to the index were %v (all empty means the outer cursor is shadowed)", nk.calls, nk.seenCursors)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nk.calls != len(pages) {
		t.Errorf("index queried %d times for %d pages; cursors seen: %v", nk.calls, len(pages), nk.seenCursors)
	}
	if !slices.Equal(got, want) {
		t.Errorf("user IDs = %v, want %v", got, want)
	}
	if want, got := []string{"", "page-1", "page-2"}, nk.seenCursors; !slices.Equal(got, want) {
		t.Errorf("cursors handed back to the index = %v, want %v", got, want)
	}
}

// A single-page result must still work: the loop has to break on the empty
// cursor the index returns, not on a page-count assumption.
func TestPagingSearches_SinglePage(t *testing.T) {
	for name, run := range map[string]func(runtime.NakamaModule) ([]string, error){
		"LoginDeniedClientIPAddressSearch": func(nk runtime.NakamaModule) ([]string, error) {
			return LoginDeniedClientIPAddressSearch(context.Background(), nk, "203.0.113.7")
		},
		"LoginHistoryRegexSearch": func(nk runtime.NakamaModule) ([]string, error) {
			return LoginHistoryRegexSearch(context.Background(), nk, "203\\.0\\.113\\.7", 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			nk := &pagingIndexNK{pages: [][]string{{"u01", "u02"}}, maxCalls: 10}
			got, err := run(nk)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if nk.calls != 1 {
				t.Errorf("index queried %d times for a single page", nk.calls)
			}
			if !slices.Equal(got, []string{"u01", "u02"}) {
				t.Errorf("user IDs = %v, want [u01 u02]", got)
			}
		})
	}
}
