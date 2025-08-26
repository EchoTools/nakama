package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageKeyLinkTickets   = "linkTickets"
	StorageIndexLinkTickets = "idx_link_ticket_code"
)

var (
	ErrLinkTicketNotFound = fmt.Errorf("link ticket not found")
)

// LinkTicket represents a ticket used for linking accounts to Discord.
// It contains the link code, xplatform ID string, and HMD serial number.
// TODO move this to evr-common
type LinkTicket struct {
	Code         string            `json:"link_code"`          // the code the user will exchange to link the account
	XPID         evr.XPID          `json:"xp_id"`              // the xplatform ID used by the client/server
	ClientIP     string            `json:"client_ip"`          // the client IP address that generated this link ticket
	LoginProfile *evr.LoginProfile `json:"game_login_request"` // the login request payload that generated this link ticket
	CreatedAt    time.Time         `json:"created_at"`         // the time the link ticket was created
}

type LinkTicketStore struct {
	Tickets map[string]*LinkTicket `json:"tickets"`
	meta    StorableMetadata
}

// Ensure LinkTicketStore implements StorableIndexer.
var _ StorableIndexer = (*LinkTicketStore)(nil)

// StorageMeta returns the metadata for the LinkTicketStore storage object.
func (l *LinkTicketStore) StorageMeta() StorableMetadata {
	return StorableMetadata{
		UserID:          SystemUserID,
		Collection:      AuthorizationCollection,
		Key:             StorageKeyLinkTickets,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         l.meta.Version,
	}
}

// SetStorageMeta sets the storage metadata for the LinkTicketStore.
func (l *LinkTicketStore) SetStorageMeta(meta StorableMetadata) {
	l.meta = meta
}

// StorageIndexes returns the storage indexes for LinkTicketStore.
func (l *LinkTicketStore) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{
		{
			Name:       StorageIndexLinkTickets,
			Collection: AuthorizationCollection,
			Key:        StorageKeyLinkTickets,
			Fields:     []string{"link_code", "xp_id"},
			MaxEntries: 1000,
			IndexOnly:  false,
		},
	}
}

func LinkTicketByCode(ctx context.Context, nk runtime.NakamaModule, code string) (*LinkTicket, error) {
	// Use the storage index to find the link ticket by code or xp_id
	result, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexLinkTickets, fmt.Sprintf(`+link_code:%s`, code), 1, nil, "")
	if err != nil {
		return nil, err
	}
	if len(result.GetObjects()) == 0 {
		return nil, ErrLinkTicketNotFound
	}
	obj := result.GetObjects()[0]

	var ticket LinkTicket
	if err := json.Unmarshal([]byte(obj.Value), &ticket); err != nil {
		return nil, err
	}
	return &ticket, nil
}

func LinkTicketByXPID(ctx context.Context, nk runtime.NakamaModule, xpid evr.XPID) (*LinkTicket, error) {
	result, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexLinkTickets, fmt.Sprintf("xp_id:%s", xpid), 1, nil, "")
	if err != nil {
		return nil, err
	}
	if len(result.GetObjects()) == 0 {
		return nil, ErrLinkTicketNotFound
	}
	obj := result.GetObjects()[0]

	var ticket LinkTicket
	if err := json.Unmarshal([]byte(obj.Value), &ticket); err != nil {
		return nil, err
	}
	return &ticket, nil
}

// linkTicket generates a link ticket for the provided xplatformId and hmdSerialNumber.
func IssueLinkTicket(ctx context.Context, nk runtime.NakamaModule, xpid evr.XPID, clientIP string, loginData *evr.LoginProfile) (*LinkTicket, error) {

	if loginData == nil {
		// This should't happen. A login request is required to create a link ticket.
		return nil, fmt.Errorf("loginData is nil")
	}

	// Check if a link ticket already exists for the given XPID
	if ticket, err := LinkTicketByXPID(ctx, nk, xpid); err == nil {
		// Link ticket already exists for this XPID, return it
		return ticket, nil
	}

	// Generate a unique link code
	var code string
	for {
		code = generateLinkCode()
		if _, err := LinkTicketByCode(ctx, nk, code); err == ErrLinkTicketNotFound {
			break
		}
	}

	// Create a new link ticket
	ticket := &LinkTicket{
		Code:         code,
		XPID:         xpid,
		ClientIP:     clientIP,
		LoginProfile: loginData,
		CreatedAt:    time.Now(),
	}
	linkTicketStore := &LinkTicketStore{}
	if err := StorableReadNk(ctx, nk, SystemUserID, linkTicketStore, true); err != nil {
		return nil, err
	}

	if linkTicketStore.Tickets == nil {
		linkTicketStore.Tickets = make(map[string]*LinkTicket)
	}
	linkTicketStore.Tickets[code] = ticket

	if err := StorableWriteNk(ctx, nk, SystemUserID, linkTicketStore); err != nil {
		return nil, err
	}

	return ticket, nil
}

// generateLinkCode generates a 4 character random link code (excluding homoglyphs, vowels, and numbers).
// The character set excludes homoglyphs (B, O, Q, V, W) and vowels (A, E, I, O, U).
func generateLinkCode() string {
	// Define the set of valid validChars for the link code
	validChars := "CDFGHJKLMNPRSTXYZ"

	// Create a new local random generator with a known seed value
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Create a byte slice with 4 elements
	code := make([]byte, 4)

	// Randomly select an index from the array and generate the code
	for i := range code {
		code[i] = validChars[rng.Intn(len(validChars))]
	}

	return string(code)
}

// ExchangeLinkCode exchanges a link code for an auth token.
// Regardless of the outcome, it deletes the used link ticket from storage.
func ExchangeLinkCode(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, linkCode string) (*LinkTicket, error) {
	// Normalize the link code to uppercase.
	linkCode = strings.ToUpper(linkCode)

	// Check if a link ticket already exists for the given XPID
	if ticket, err := LinkTicketByCode(ctx, nk, linkCode); err == nil {
		// Link ticket already exists for this XPID, return it
		return ticket, nil
	}

	store := &LinkTicketStore{}
	if err := StorableReadNk(ctx, nk, SystemUserID, store, true); err != nil {
		return nil, err
	}
	if store.Tickets == nil {
		store.Tickets = make(map[string]*LinkTicket)
	}

	linkTicket, ok := store.Tickets[linkCode]
	if !ok {
		return nil, runtime.NewError(fmt.Sprintf("link code `%s` not found", linkCode), StatusNotFound)
	}

	delete(store.Tickets, linkCode)

	if err := StorableWriteNk(ctx, nk, SystemUserID, store); err != nil {
		return nil, err
	}

	return linkTicket, nil
}

// verifyJWT parses and verifies a JWT token using the provided key function.
// It returns the parsed token if it is valid, otherwise it returns an error.
// Nakama JWT's are signed by the `session.session_encryption_key` in the Nakama config.
func verifySignedJWT(rawToken string, secret string) (*jwt.Token, error) {
	token, err := jwt.Parse(rawToken, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return token, nil
}
