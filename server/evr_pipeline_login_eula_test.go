package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// eulaSeedNK serves the EULA document read and records how the seed-on-miss
// write is committed.
//
// It embeds a nil runtime.NakamaModule (AGENTS.md defect class 5) so an
// undefined method panics rather than no-opping. No method it defines calls
// another of its own, so defect class 1 does not apply.
type eulaSeedNK struct {
	runtime.NakamaModule

	stored *api.StorageObject // nil means the read misses

	reads            [][]*runtime.StorageRead
	rawStorageWrites int
	multiWrites      [][]*runtime.StorageWrite
	failMultiUpdate  error
}

func (m *eulaSeedNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	m.reads = append(m.reads, reads)
	if m.stored == nil {
		return nil, nil
	}
	return []*api.StorageObject{m.stored}, nil
}

func (m *eulaSeedNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	m.rawStorageWrites++
	return nil, nil
}

func (m *eulaSeedNK) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	m.multiWrites = append(m.multiWrites, storageWrites)
	if m.failMultiUpdate != nil {
		return nil, nil, m.failMultiUpdate
	}
	return nil, nil, nil
}

// TestEULADocumentLoadOrSeed_SeedsThroughMultiUpdate: the seed-on-miss was the
// login path's last raw nk.StorageWrite (#394 task 2). It now commits through
// MultiUpdate, the entry point that can widen to carry account, wallet and
// delete operations without the caller changing shape.
func TestEULADocumentLoadOrSeed_SeedsThroughMultiUpdate(t *testing.T) {
	nk := &eulaSeedNK{}

	before := time.Now().UTC()
	doc, ts, err := eulaDocumentLoadOrSeed(context.Background(), nk, "en")
	if err != nil {
		t.Fatalf("eulaDocumentLoadOrSeed: %v", err)
	}

	if nk.rawStorageWrites != 0 {
		t.Fatalf("%d raw StorageWrite calls, want 0", nk.rawStorageWrites)
	}
	if len(nk.multiWrites) != 1 || len(nk.multiWrites[0]) != 1 {
		t.Fatalf("want one MultiUpdate carrying one write, got %v", nk.multiWrites)
	}

	w := nk.multiWrites[0][0]
	if w.Collection != DocumentStorageCollection || w.Key != "eula,en" {
		t.Fatalf("seed targets the wrong object: collection=%q key=%q", w.Collection, w.Key)
	}
	// The owner must resolve to the system user, which is what the read at the
	// top of this function looks under. An empty UserID and SystemUserID are
	// equivalent here — runtime_go_nakama.go:2380 maps "" to uuid.Nil — but the
	// read asks for SystemUserID explicitly, so the write says so too.
	if w.UserID != SystemUserID && w.UserID != "" {
		t.Fatalf("seed owner = %q, want the system user", w.UserID)
	}
	if w.PermissionRead != 0 || w.PermissionWrite != 0 {
		t.Fatalf("seed permissions = %d/%d, want 0/0", w.PermissionRead, w.PermissionWrite)
	}

	if doc.Text != evr.DefaultEULADocument("en").Text {
		t.Fatalf("seeded document is not the default: %q", doc.Text)
	}
	if ts.Before(before) {
		t.Fatalf("seed timestamp %v predates the call at %v", ts, before)
	}

	if len(nk.reads) != 1 {
		t.Fatalf("%d storage reads, want 1", len(nk.reads))
	}
	if nk.reads[0][0].UserID != SystemUserID {
		t.Fatalf("read owner = %q, want the system user", nk.reads[0][0].UserID)
	}
}

// TestEULADocumentLoadOrSeed_HitDoesNotWrite: a stored document is returned
// as-is with its storage timestamp, and nothing is written.
func TestEULADocumentLoadOrSeed_HitDoesNotWrite(t *testing.T) {
	stored := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	nk := &eulaSeedNK{stored: &api.StorageObject{
		Collection: DocumentStorageCollection,
		Key:        "eula,en",
		Value:      `{"text":"stored text"}`,
		UpdateTime: timestamppb.New(stored),
	}}

	doc, ts, err := eulaDocumentLoadOrSeed(context.Background(), nk, "en")
	if err != nil {
		t.Fatalf("eulaDocumentLoadOrSeed: %v", err)
	}
	if len(nk.multiWrites) != 0 || nk.rawStorageWrites != 0 {
		t.Fatalf("a cache hit wrote to storage: multi=%v raw=%d", nk.multiWrites, nk.rawStorageWrites)
	}
	if doc.Text != "stored text" {
		t.Fatalf("document text = %q, want the stored value", doc.Text)
	}
	if !ts.Equal(stored) {
		t.Fatalf("timestamp = %v, want the stored update time %v", ts, stored)
	}
}

// TestEULADocumentLoadOrSeed_SeedFailurePropagates: a rejected seed is an error,
// not a silently unstored default.
func TestEULADocumentLoadOrSeed_SeedFailurePropagates(t *testing.T) {
	sentinel := errors.New("write rejected")
	nk := &eulaSeedNK{failMultiUpdate: sentinel}

	if _, _, err := eulaDocumentLoadOrSeed(context.Background(), nk, "en"); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap %v with %%w", err, sentinel)
	}
}
