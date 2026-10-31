package evr

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

// SNSPurchaseItems is the client's store asking to buy items
// (CR15NetStoreTransactions::PurchaseItemImpl). The client then waits for
// SNSPurchaseItemsResult with no timer and refuses every further purchase
// until it gets one, so every request must be answered.
//
// Wire layout, taken from the client's sender signature
// SNSPurchaseItems::Send(CTcpBroadcaster&, SNSLoginId, SNSUserID, long long, CJson, CJson, CJson):
// login session GUID, XPID, an int64 transaction id, then the JSON documents
// (items, balance, context), each NUL-terminated. The layout past the header has
// not been checked against a captured packet, so Stream never fails on it: a
// request that cannot be parsed still has to be answered, and a Stream error
// closes the connection.
type SNSPurchaseItems struct {
	LoginSessionID uuid.UUID
	EvrID          EvrId
	TransactionID  int64
	Items          PurchaseItemsRequest
	// ParseErr is set when the JSON after the header could not be read. It is
	// never returned from Stream.
	ParseErr error
}

// PurchaseItemsRequest is the union of the client's JSON documents. Unknown
// fields are ignored. The price is nested inside the items
// (itemskus[i].price.currency.echopoints.val) and is kept raw.
type PurchaseItemsRequest struct {
	ItemSKUs      json.RawMessage `json:"itemskus,omitempty"`
	Balance       IAPBalance      `json:"balance"`
	Context       json.RawMessage `json:"context,omitempty"`
	TransactionID int64           `json:"transactionid,omitempty"`
}

func (m SNSPurchaseItems) Token() string   { return "SNSPurchaseItems" }
func (m *SNSPurchaseItems) Symbol() Symbol { return SymbolOf(m) }

func (m SNSPurchaseItems) String() string {
	return fmt.Sprintf("%s(evr_id=%s, transaction_id=%d, items=%s)", m.Token(), m.EvrID.String(), m.TransactionID, m.Items.ItemSKUs)
}

func (m *SNSPurchaseItems) Stream(s *EasyStream) error {
	if s.Mode == EncodeMode {
		body, err := json.Marshal(&m.Items)
		if err != nil {
			return err
		}
		return RunErrorFunctions([]func() error{
			func() error { return s.StreamGUID(&m.LoginSessionID) },
			func() error { return s.StreamStruct(&m.EvrID) },
			func() error { return s.StreamNumber(binary.LittleEndian, &m.TransactionID) },
			func() error { return s.StreamBytes(&body, len(body)) },
			func() error { b := []byte{0}; return s.StreamBytes(&b, 1) },
		})
	}

	// Decode. The header is best effort: a short packet leaves zero values.
	if s.Len() < 32 {
		m.ParseErr = fmt.Errorf("purchase request is %d bytes, want at least 32", s.Len())
		return nil
	}
	if err := RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSessionID) },
		func() error { return s.StreamStruct(&m.EvrID) },
	}); err != nil {
		m.ParseErr = err
		return nil
	}

	var rest []byte
	if s.Len() > 0 {
		if err := s.StreamBytes(&rest, -1); err != nil {
			m.ParseErr = err
			return nil
		}
	}
	body := rest
	if !startsJSONObject(rest) && len(rest) >= 9 && rest[8] == '{' {
		m.TransactionID = int64(binary.LittleEndian.Uint64(rest[:8]))
		body = rest[8:]
	}

	for doc := range bytes.SplitSeq(body, []byte{0}) {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		if err := json.Unmarshal(doc, &m.Items); err != nil {
			m.ParseErr = fmt.Errorf("purchase request json: %w", err)
		}
	}
	if m.TransactionID == 0 {
		m.TransactionID = m.Items.TransactionID
	}
	return nil
}

// startsJSONObject reports whether b opens a JSON object, as opposed to an
// int64 whose low byte happens to be '{' (0x7b).
func startsJSONObject(b []byte) bool {
	if len(b) < 2 || b[0] != '{' {
		return false
	}
	switch b[1] {
	case '"', '}', ' ', '\n', '\r', '\t':
		return true
	}
	return false
}

// SNSPurchaseItemsResult answers SNSPurchaseItems. The client treats an empty
// Reasons as a successful purchase and anything else as a failure; either way
// the reply clears its pending-transaction state.
//
// Wire layout follows ReconcileIAPResult, the other reply from the same
// transaction service: XPID, then the JSON document, NUL-terminated.
type SNSPurchaseItemsResult struct {
	EvrID  EvrId
	Result PurchaseItemsResult
}

type PurchaseItemsResult struct {
	Reasons       []string   `json:"reasons"`
	Balance       IAPBalance `json:"balance"`
	TransactionID int64      `json:"transactionid"`
}

// NewSNSPurchaseItemsFailure refuses a purchase for reason, leaving the
// balance as the client reported it and echoing its transaction id. There is
// deliberately no success constructor: the server sells nothing, and the client
// would treat a success as the item bought.
func NewSNSPurchaseItemsFailure(req *SNSPurchaseItems, reason string) *SNSPurchaseItemsResult {
	return &SNSPurchaseItemsResult{
		EvrID: req.EvrID,
		Result: PurchaseItemsResult{
			Reasons:       []string{reason},
			Balance:       req.Items.Balance,
			TransactionID: req.TransactionID,
		},
	}
}

func (m SNSPurchaseItemsResult) Token() string   { return "SNSPurchaseItemsResult" }
func (m *SNSPurchaseItemsResult) Symbol() Symbol { return SymbolOf(m) }

func (m SNSPurchaseItemsResult) String() string {
	return fmt.Sprintf("%s(evr_id=%s, transaction_id=%d, reasons=%v)", m.Token(), m.EvrID.String(), m.Result.TransactionID, m.Result.Reasons)
}

func (m *SNSPurchaseItemsResult) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamStruct(&m.EvrID) },
		func() error { return s.StreamJson(&m.Result, true, NoCompression) },
	})
}
