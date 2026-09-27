package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blugelabs/bluge"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// blugeAltCandidates indexes each history exactly as the production storage
// index does (MarshalJSON -> mapIndexStorageFields with the LoginHistory index
// fields) into an in-memory Bluge index, then runs the discovery query that
// LoginAlternatePatternSearch builds for searcher through ParseQueryString,
// and returns the user IDs it hits.
//
// Every other alt test fakes StorageIndexList, so none of them can say whether
// the real query language finds a given stored value. This one can.
func blugeAltCandidates(t *testing.T, searcher *LoginHistory, stored ...*LoginHistory) map[string]bool {
	t.Helper()

	si := &LocalStorageIndex{config: &StorageConfig{}}
	meta := searcher.StorageIndexes()[0]

	writer, err := bluge.OpenWriter(BlugeInMemoryConfig())
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer writer.Close()

	for _, h := range stored {
		value, err := json.Marshal(h)
		if err != nil {
			t.Fatalf("marshal %s: %v", h.userID, err)
		}
		doc, err := si.mapIndexStorageFields(h.userID, meta.Collection, meta.Key, "v1", string(value), 0, 0, time.Now(), time.Now(), meta.Fields, meta.SortableFields, meta.IndexOnly)
		if err != nil || doc == nil {
			t.Fatalf("map %s: doc=%v err=%v", h.userID, doc, err)
		}
		if err := writer.Update(doc.ID(), doc); err != nil {
			t.Fatalf("index %s: %v", h.userID, err)
		}
	}

	query := fmt.Sprintf("+value.cache:%s", Query.CreateMatchPattern(searcher.AltSearchPatterns()))
	parsed, err := ParseQueryString(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}

	reader, err := writer.Reader()
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer reader.Close()

	it, err := reader.Search(context.Background(), bluge.NewTopNSearch(100, parsed))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	hits := make(map[string]bool)
	for m, err := it.Next(); m != nil && err == nil; m, err = it.Next() {
		_ = m.VisitStoredFields(func(field string, value []byte) bool {
			if field == "user_id" {
				hits[string(value)] = true
			}
			return true
		})
	}
	t.Logf("query=%s hits=%v", query, hits)
	return hits
}

func altTestHistory(userID string, xpid evr.EvrId, ip, serial string) *LoginHistory {
	h := NewLoginHistory(userID)
	h.History = map[string]*LoginHistoryEntry{
		loginHistoryEntryKey(xpid, ip): {
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			XPID:      xpid,
			ClientIP:  ip,
			LoginData: &evr.LoginProfile{HMDSerialNumber: serial},
		},
	}
	h.rebuildCache()
	return h
}

// An XPID shared by two accounts is, on its own, an alt link: nothing else in
// common is required. Both halves are exercised -- discovery through the real
// Bluge query, and loginHistoryCompare on what discovery returns.
func TestAltLink_XPIDAloneLinks(t *testing.T) {
	shared := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 1000000000000001}
	a := altTestHistory("00000000-0000-0000-0000-00000000000a", shared, "203.0.113.10", "SERIALAAAA")
	b := altTestHistory("00000000-0000-0000-0000-00000000000b", shared, "198.51.100.7", "SERIALBBBB")
	stranger := altTestHistory("00000000-0000-0000-0000-00000000000c", evr.EvrId{PlatformCode: evr.OVR, AccountId: 42}, "192.0.2.3", "SERIALCCCC")

	hits := blugeAltCandidates(t, a, b, stranger)
	if !hits[b.userID] {
		t.Errorf("discovery: account sharing only XPID %s was not returned by the index query", shared.Token())
	}
	if hits[stranger.userID] {
		t.Errorf("discovery: account sharing nothing was returned")
	}

	matches := loginHistoryCompare(a, b)
	if len(matches) != 1 || len(matches[0].Items) != 1 || matches[0].Items[0] != shared.Token() {
		t.Errorf("compare: want one match on [%s], got %+v", shared.Token(), matches)
	}
}
