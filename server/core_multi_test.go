package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// WHAT THESE TESTS PROVE, AND WHAT THEY DO NOT.
//
// #394 asks for confirmation that "wallet changes revert when storage writes
// fail and vice versa". That claim has two halves, and only one of them is this
// codebase's to keep:
//
//  1. OURS: every operation group — account updates, storage writes, storage
//     deletes, wallet updates — executes against ONE pgx.Tx, and the first
//     failure aborts the whole unit by returning an error, so no later group
//     runs and no earlier group is committed. That is what multiUpdateTx does,
//     and it is what these tests pin, with a fake pgx.Tx that records every
//     statement and can fail at a chosen point.
//
//  2. POSTGRES'S: an aborted transaction discards the statements already issued
//     inside it. ExecuteInTxPgx rolls back on a non-nil closure error
//     (db.go:428-432, 440-454) and never commits. Nothing here re-tests the
//     database's own atomicity, and a test that claimed to would be testing a
//     mock of Postgres rather than Postgres.
//
// So the assertion is: a failure anywhere leaves the transaction with a non-nil
// error and every prior statement inside it. Given (2), that IS the revert.
//
// These are the first tests core_multi.go has ever had.

// fakeTx is a pgx.Tx that records the statements issued against it and can fail
// on a chosen one. It exists so the MultiUpdate transaction body can be driven
// without a database — MEMORY: DB-reliant tests are bad tests, and the property
// under test here is about control flow inside one transaction, not about SQL.
type fakeTx struct {
	// statements records every statement issued against this transaction, in
	// order. That they all land on THIS object is the point: one transaction.
	statements []string

	// failExecContaining, failBatchScan and failQueryContaining choose where the
	// transaction breaks.
	failExecContaining  string
	failQueryContaining string
	failBatchScan       error

	batchRowsScanned int

	committed  bool
	rolledBack bool
}

func (t *fakeTx) record(kind, sql string) {
	t.statements = append(t.statements, kind+": "+strings.Join(strings.Fields(sql), " "))
}

func (t *fakeTx) has(substr string) bool {
	for _, s := range t.statements {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func (t *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.record("exec", sql)
	if t.failExecContaining != "" && strings.Contains(sql, t.failExecContaining) {
		return pgconn.CommandTag{}, errors.New("exec rejected: " + t.failExecContaining)
	}
	return pgconn.CommandTag{}, nil
}

func (t *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	t.record("query", sql)
	if t.failQueryContaining != "" && strings.Contains(sql, t.failQueryContaining) {
		return nil, errors.New("query rejected: " + t.failQueryContaining)
	}
	return nil, errors.New("fakeTx: unexpected successful Query; the test did not model rows for " + sql)
}

func (t *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	t.record("queryrow", sql)
	return fakeRow{err: errors.New("fakeTx: unexpected QueryRow")}
}

func (t *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	t.record("batch", "storage write batch of "+string(rune('0'+b.Len())))
	return &fakeBatchResults{tx: t}
}

func (t *fakeTx) Commit(ctx context.Context) error   { t.committed = true; return nil }
func (t *fakeTx) Rollback(ctx context.Context) error { t.rolledBack = true; return nil }
func (t *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, errors.New("fakeTx: Begin not modelled")
}

func (t *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("fakeTx: CopyFrom not modelled")
}
func (t *fakeTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }
func (t *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("fakeTx: Prepare not modelled")
}
func (t *fakeTx) Conn() *pgx.Conn { return nil }

type fakeRow struct{ err error }

func (r fakeRow) Scan(dest ...any) error { return r.err }

// fakeBatchResults answers the storage-write batch. A successful row reports an
// upsert, which is what storageWriteObjects needs to produce an ack.
type fakeBatchResults struct{ tx *fakeTx }

func (b *fakeBatchResults) Exec() (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("fakeBatchResults: Exec not modelled")
}
func (b *fakeBatchResults) Query() (pgx.Rows, error) {
	return nil, errors.New("fakeBatchResults: Query not modelled")
}
func (b *fakeBatchResults) Close() error { return nil }

func (b *fakeBatchResults) QueryRow() pgx.Row {
	b.tx.batchRowsScanned++
	if b.tx.failBatchScan != nil {
		return fakeRow{err: b.tx.failBatchScan}
	}
	return storageWriteOKRow{}
}

// storageWriteOKRow fills the six values storageWriteObjects scans out of each
// batch row: read, write, version, create_time, update_time, is_upsert.
type storageWriteOKRow struct{}

func (storageWriteOKRow) Scan(dest ...any) error {
	if len(dest) != 6 {
		return errors.New("storageWriteOKRow: unexpected scan arity")
	}
	*(dest[0].(*int32)) = 2
	*(dest[1].(*int32)) = 1
	*(dest[2].(*string)) = "v1"
	*(dest[3].(*time.Time)) = time.Unix(0, 0).UTC()
	*(dest[4].(*time.Time)) = time.Unix(0, 0).UTC()
	*(dest[5].(*bool)) = true
	return nil
}

func multiUpdateTestOps() (uuid.UUID, []*accountUpdate, StorageOpWrites, []*walletUpdate) {
	userID := uuid.Must(uuid.NewV4())
	accounts := []*accountUpdate{{userID: userID, metadata: wrapperspb.String(`{"seen":true}`)}}
	writes := StorageOpWrites{{
		OwnerID: userID.String(),
		Object: &api.WriteStorageObject{
			Collection:      "TestCollection",
			Key:             "TestKey",
			Value:           `{"a":1}`,
			PermissionRead:  &wrapperspb.Int32Value{Value: 1},
			PermissionWrite: &wrapperspb.Int32Value{Value: 0},
		},
	}}
	wallets := []*walletUpdate{{UserID: userID, Changeset: map[string]int64{"coin": 5}, Metadata: "{}"}}
	return userID, accounts, writes, wallets
}

// TestMultiUpdateTx_StorageWriteFailureStopsTheWalletUpdate is the first half of
// the #394 rollback requirement: when the storage write is rejected, the wallet
// is not changed.
//
// It is not changed for the strongest available reason — updateWallets is never
// reached, so no wallet statement exists to revert — and the account update that
// DID already run is left inside a transaction that returns an error and is
// therefore rolled back rather than committed.
func TestMultiUpdateTx_StorageWriteFailureStopsTheWalletUpdate(t *testing.T) {
	_, accounts, writes, wallets := multiUpdateTestOps()

	// A unique-violation on a storage batch row is how Postgres reports a
	// version mismatch; storageWriteObjects maps it to ErrStorageRejectedVersion.
	tx := &fakeTx{failBatchScan: &pgconn.PgError{Code: dbErrorUniqueViolation}}

	_, _, _, err := multiUpdateTx(context.Background(), zap.NewNop(), &testMetrics{}, tx, accounts, writes, nil, wallets, true)
	if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
		t.Fatalf("error = %v, want runtime.ErrStorageRejectedVersion", err)
	}

	// The account update ran first, on this same transaction.
	if !tx.has("UPDATE users SET update_time") {
		t.Fatalf("the account update never ran; statements: %v", tx.statements)
	}
	// The storage write was attempted.
	if tx.batchRowsScanned != 1 {
		t.Fatalf("storage batch rows scanned = %d, want 1", tx.batchRowsScanned)
	}
	// And the wallet was never touched: updateWallets' first statement is the
	// SELECT ... FOR UPDATE that loads the wallets.
	if tx.has("FOR UPDATE") {
		t.Fatalf("the wallet update ran after the storage write failed; statements: %v", tx.statements)
	}
	if tx.has("wallet_ledger") {
		t.Fatalf("a wallet ledger row was written after the storage write failed; statements: %v", tx.statements)
	}
	// Nothing committed. The transaction is the caller's to finish, and
	// ExecuteInTxPgx rolls it back on this error.
	if tx.committed {
		t.Fatal("the transaction was committed despite the storage write failing")
	}
}

// TestMultiUpdateTx_WalletFailureLeavesTheStorageWriteUncommitted is the
// converse half: when the wallet update is rejected, the storage write that
// already ran is inside the same transaction and is not committed.
func TestMultiUpdateTx_WalletFailureLeavesTheStorageWriteUncommitted(t *testing.T) {
	_, accounts, writes, wallets := multiUpdateTestOps()

	// updateWallets' first statement is "SELECT id, wallet FROM users ... FOR UPDATE".
	tx := &fakeTx{failQueryContaining: "FOR UPDATE"}

	_, _, _, err := multiUpdateTx(context.Background(), zap.NewNop(), &testMetrics{}, tx, accounts, writes, nil, wallets, true)
	if err == nil {
		t.Fatal("wallet failure did not propagate out of the transaction body")
	}

	// The storage write really did execute, on this same transaction, before the
	// wallet failed. That is what makes it something the rollback has to undo.
	if tx.batchRowsScanned != 1 {
		t.Fatalf("storage batch rows scanned = %d, want 1 — the storage write must have run before the wallet failure", tx.batchRowsScanned)
	}
	if !tx.has("UPDATE users SET update_time") {
		t.Fatalf("the account update never ran; statements: %v", tx.statements)
	}
	if !tx.has("FOR UPDATE") {
		t.Fatalf("the wallet load never ran; statements: %v", tx.statements)
	}
	// No wallet was actually written, and nothing was committed.
	if tx.has("wallet_ledger") {
		t.Fatalf("a wallet ledger row was written despite the failure; statements: %v", tx.statements)
	}
	if tx.committed {
		t.Fatal("the transaction was committed despite the wallet update failing")
	}
}

// TestMultiUpdateTx_AccountFailureStopsEverything: the first group failing means
// no storage and no wallet statement is issued at all.
func TestMultiUpdateTx_AccountFailureStopsEverything(t *testing.T) {
	_, accounts, writes, wallets := multiUpdateTestOps()

	tx := &fakeTx{failExecContaining: "UPDATE users SET update_time"}

	_, _, _, err := multiUpdateTx(context.Background(), zap.NewNop(), &testMetrics{}, tx, accounts, writes, nil, wallets, true)
	if err == nil {
		t.Fatal("account update failure did not propagate out of the transaction body")
	}
	if tx.batchRowsScanned != 0 {
		t.Fatalf("storage batch rows scanned = %d, want 0", tx.batchRowsScanned)
	}
	if tx.has("FOR UPDATE") {
		t.Fatalf("the wallet update ran after the account update failed; statements: %v", tx.statements)
	}
}

// TestMultiUpdateTx_AllGroupsShareOneTransaction: with everything succeeding up
// to the wallet load, the account update, the storage batch and the wallet load
// have all been issued against the same pgx.Tx, in that order. Sharing the
// transaction is what makes a later failure revert an earlier write; if any
// group were given its own transaction the reverts above would be vacuous.
func TestMultiUpdateTx_AllGroupsShareOneTransaction(t *testing.T) {
	_, accounts, writes, wallets := multiUpdateTestOps()

	tx := &fakeTx{failQueryContaining: "FOR UPDATE"}
	_, _, _, _ = multiUpdateTx(context.Background(), zap.NewNop(), &testMetrics{}, tx, accounts, writes, nil, wallets, true)

	if len(tx.statements) != 3 {
		t.Fatalf("statements on the transaction = %d, want 3 (account, storage batch, wallet load): %v", len(tx.statements), tx.statements)
	}
	if !strings.HasPrefix(tx.statements[0], "exec: UPDATE users SET update_time") {
		t.Fatalf("statement 0 is not the account update: %q", tx.statements[0])
	}
	if !strings.HasPrefix(tx.statements[1], "batch: storage write batch") {
		t.Fatalf("statement 1 is not the storage write batch: %q", tx.statements[1])
	}
	if !strings.Contains(tx.statements[2], "FOR UPDATE") {
		t.Fatalf("statement 2 is not the wallet load: %q", tx.statements[2])
	}
}
