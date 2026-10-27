package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A client's store waits for SNSPurchaseItemsResult with no timer. A request
// that reaches the pipeline, even on the unauthenticated transaction
// connection, must be answered with a failure that echoes its transaction id,
// and must be logged.
func TestPurchaseItems_RefusedAndLogged(t *testing.T) {
	p := &EvrPipeline{}
	session, captured := newNilIdentityConfigSession(t, p)

	req := &evr.SNSPurchaseItems{
		LoginSessionID: uuid.Must(uuid.NewV4()),
		EvrID:          evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 42},
		TransactionID:  7,
		Items: evr.PurchaseItemsRequest{
			Balance: evr.IAPBalance{Currency: evr.IAPCurrency{EchoPoints: evr.IAPEchoPoints{Value: 1200}}},
		},
	}
	wire, err := evr.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := evr.ParsePacket(wire)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("ParsePacket = %v, %v", parsed, err)
	}

	core, logs := observer.New(zapcore.InfoLevel)
	if !p.ProcessRequestEVR(zap.New(core), session, parsed[0]) {
		t.Fatal("ProcessRequestEVR returned false; the session read loop would close the connection")
	}

	if len(*captured) != 1 {
		t.Fatalf("sent %d messages, want exactly one reply", len(*captured))
	}
	res, ok := (*captured)[0].(*evr.SNSPurchaseItemsResult)
	if !ok {
		t.Fatalf("sent %T, want *evr.SNSPurchaseItemsResult", (*captured)[0])
	}
	if len(res.Result.Reasons) == 0 {
		t.Fatal("reply has no reasons: the client reads that as a successful purchase")
	}
	if res.Result.TransactionID != 7 || res.Result.Balance.Currency.EchoPoints.Value != 1200 || res.EvrID != req.EvrID {
		t.Errorf("reply = %+v, want transaction 7, balance 1200, evr id %v", res, req.EvrID)
	}

	recs := logs.FilterMessage("Refused purchase").All()
	if len(recs) != 1 {
		t.Fatalf("want one 'Refused purchase' record, got %v", logs.All())
	}
	fields := recs[0].ContextMap()
	if fields["transaction_id"] != int64(7) || fields["evrid"] != req.EvrID.String() {
		t.Errorf("record fields = %v", fields)
	}
}

// A request the server cannot fully parse is still answered, and does not
// disconnect the client.
func TestPurchaseItems_UnparseableBodyStillAnswered(t *testing.T) {
	p := &EvrPipeline{}
	session, captured := newNilIdentityConfigSession(t, p)

	req := &evr.SNSPurchaseItems{}
	if err := req.Stream(evr.NewEasyStream(evr.DecodeMode, []byte{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	if req.ParseErr == nil {
		t.Fatal("setup: expected a ParseErr")
	}

	core, logs := observer.New(zapcore.WarnLevel)
	if !p.ProcessRequestEVR(zap.New(core), session, req) {
		t.Fatal("ProcessRequestEVR returned false; the session read loop would close the connection")
	}
	if len(*captured) != 1 {
		t.Fatalf("sent %d messages, want exactly one reply", len(*captured))
	}
	if res, ok := (*captured)[0].(*evr.SNSPurchaseItemsResult); !ok || len(res.Result.Reasons) == 0 {
		t.Fatalf("reply = %#v, want a failure result", (*captured)[0])
	}
	if logs.FilterMessageSnippet("could not be fully parsed").Len() != 1 {
		t.Errorf("want one parse warning, got %v", logs.All())
	}
}
