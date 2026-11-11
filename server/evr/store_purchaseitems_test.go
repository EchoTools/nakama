package evr

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid/v5"
)

func purchaseHeader(t *testing.T, session uuid.UUID, id EvrId) []byte {
	t.Helper()
	s := NewEasyStream(EncodeMode, nil)
	if err := RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&session) },
		func() error { return s.StreamStruct(&id) },
	}); err != nil {
		t.Fatal(err)
	}
	return s.Bytes()
}

const purchaseItemsJSON = `{"itemskus":[{"sku":"rwd_tint_0001","price":{"currency":{"echopoints":{"val":500}}}}]}`
const purchaseBalanceJSON = `{"balance":{"currency":{"echopoints":{"val":1200}}}}`

func TestPurchaseItemsSymbols(t *testing.T) {
	if got := ToSymbol("SNSPurchaseItems"); got != 0xf1cefc446985b4f5 {
		t.Errorf("ToSymbol(SNSPurchaseItems) = %v", got.HexString())
	}
	if got := ToSymbol("SNSPurchaseItemsResult"); got != 0x8d049b8991fc695a {
		t.Errorf("ToSymbol(SNSPurchaseItemsResult) = %v", got.HexString())
	}
	if got := (&SNSPurchaseItems{}).Symbol(); got != 0xf1cefc446985b4f5 {
		t.Errorf("SNSPurchaseItems.Symbol() = %v, registered under the wrong hash", got.HexString())
	}
	if got := (&SNSPurchaseItemsResult{}).Symbol(); got != 0x8d049b8991fc695a {
		t.Errorf("SNSPurchaseItemsResult.Symbol() = %v, registered under the wrong hash", got.HexString())
	}
}

func TestSNSPurchaseItems_Decode(t *testing.T) {
	session := uuid.Must(uuid.NewV4())
	id := EvrId{PlatformCode: OVR_ORG, AccountId: 1234567890}
	header := purchaseHeader(t, session, id)
	txid := make([]byte, 8)
	binary.LittleEndian.PutUint64(txid, 7)
	txid123 := make([]byte, 8) // low byte is '{'
	binary.LittleEndian.PutUint64(txid123, 123)

	cases := []struct {
		name     string
		payload  []byte
		wantTx   int64
		wantBal  int64
		wantItem bool
		wantErr  bool
	}{
		{"txid then three documents", bytes.Join([][]byte{header, txid, []byte(purchaseItemsJSON), {0}, []byte(purchaseBalanceJSON), {0}, []byte(`{"context":{"appid":1}}`), {0}}, nil), 7, 1200, true, false},
		{"txid whose low byte is a brace", bytes.Join([][]byte{header, txid123, []byte(purchaseItemsJSON), {0}}, nil), 123, 0, true, false},
		{"no txid prefix", bytes.Join([][]byte{header, []byte(purchaseItemsJSON), {0}, []byte(purchaseBalanceJSON), {0}}, nil), 0, 1200, true, false},
		{"txid inside the json", bytes.Join([][]byte{header, []byte(`{"transactionid":99,"balance":{"currency":{"echopoints":{"val":5}}}}`), {0}}, nil), 99, 5, false, false},
		{"malformed json is noted, not fatal", bytes.Join([][]byte{header, txid, []byte(`{"itemskus":[`), {0}}, nil), 7, 0, false, true},
		{"header only", header, 0, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &SNSPurchaseItems{}
			if err := m.Stream(NewEasyStream(DecodeMode, tc.payload)); err != nil {
				t.Fatalf("Stream returned %v; a Stream error closes the client's connection", err)
			}
			if m.LoginSessionID != session || m.EvrID != id {
				t.Errorf("header = %v %v, want %v %v", m.LoginSessionID, m.EvrID, session, id)
			}
			if m.TransactionID != tc.wantTx {
				t.Errorf("TransactionID = %d, want %d", m.TransactionID, tc.wantTx)
			}
			if got := m.Items.Balance.Currency.EchoPoints.Value; got != tc.wantBal {
				t.Errorf("balance = %d, want %d", got, tc.wantBal)
			}
			if (len(m.Items.ItemSKUs) > 0) != tc.wantItem {
				t.Errorf("ItemSKUs = %s, want present=%v", m.Items.ItemSKUs, tc.wantItem)
			}
			if (m.ParseErr != nil) != tc.wantErr {
				t.Errorf("ParseErr = %v, want error=%v", m.ParseErr, tc.wantErr)
			}
		})
	}
}

func TestSNSPurchaseItems_ShortPacketIsNotFatal(t *testing.T) {
	m := &SNSPurchaseItems{}
	if err := m.Stream(NewEasyStream(DecodeMode, []byte{1, 2, 3})); err != nil {
		t.Fatalf("Stream returned %v; a Stream error closes the client's connection", err)
	}
	if m.ParseErr == nil {
		t.Error("a 3-byte packet should be noted in ParseErr")
	}
}

// The round trip the order asks for: a client request in, the refusal out, both
// through the real packet codec.
func TestPurchaseItems_RequestToRefusalRoundTrip(t *testing.T) {
	req := &SNSPurchaseItems{
		LoginSessionID: uuid.Must(uuid.NewV4()),
		EvrID:          EvrId{PlatformCode: OVR_ORG, AccountId: 42},
		TransactionID:  7,
		Items: PurchaseItemsRequest{
			ItemSKUs: json.RawMessage(`[{"sku":"rwd_tint_0001"}]`),
			Balance:  IAPBalance{Currency: IAPCurrency{EchoPoints: IAPEchoPoints{Value: 1200}}},
		},
	}

	wire, err := Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	parsed, err := ParsePacket(wire)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("ParsePacket = %v, %v", parsed, err)
	}
	got, ok := parsed[0].(*SNSPurchaseItems)
	if !ok {
		t.Fatalf("parsed %T, want *SNSPurchaseItems", parsed[0])
	}
	if got.TransactionID != 7 || got.EvrID != req.EvrID || got.LoginSessionID != req.LoginSessionID || got.ParseErr != nil {
		t.Fatalf("round-tripped request = %+v", got)
	}
	if got.Items.Balance.Currency.EchoPoints.Value != 1200 || string(got.Items.ItemSKUs) != `[{"sku":"rwd_tint_0001"}]` {
		t.Fatalf("round-tripped items = %+v", got.Items)
	}

	reply := NewSNSPurchaseItemsFailure(got, "items are not for sale")
	wire, err = Marshal(reply)
	if err != nil {
		t.Fatalf("Marshal reply: %v", err)
	}
	parsed, err = ParsePacket(wire)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("ParsePacket reply = %v, %v", parsed, err)
	}
	res, ok := parsed[0].(*SNSPurchaseItemsResult)
	if !ok {
		t.Fatalf("parsed %T, want *SNSPurchaseItemsResult", parsed[0])
	}
	if len(res.Result.Reasons) != 1 || res.Result.Reasons[0] != "items are not for sale" {
		t.Errorf("reasons = %v, want one refusal: the client reads an empty list as success", res.Result.Reasons)
	}
	if res.Result.TransactionID != 7 {
		t.Errorf("transactionid = %d, want the request's 7", res.Result.TransactionID)
	}
	if res.Result.Balance.Currency.EchoPoints.Value != 1200 {
		t.Errorf("balance = %d, want the client's 1200 unchanged", res.Result.Balance.Currency.EchoPoints.Value)
	}
	if res.EvrID != req.EvrID {
		t.Errorf("EvrID = %v, want %v", res.EvrID, req.EvrID)
	}
}

// reasons must serialize as a non-empty array under that exact key, and the
// other fields under the names the client reads.
func TestSNSPurchaseItemsResult_JSONKeys(t *testing.T) {
	reply := NewSNSPurchaseItemsFailure(&SNSPurchaseItems{TransactionID: 3}, "no")
	b, err := json.Marshal(reply.Result)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"reasons":["no"],"balance":{"currency":{"echopoints":{"val":0}}},"transactionid":3}`
	if string(b) != want {
		t.Errorf("result json = %s, want %s", b, want)
	}
}
