package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Invite codes for the game's CREATE GAME / JOIN VIA CODE wizard
// (EchoTools/nevr-runtime ADR 0017, issue #717).
//
// A code is a short alias for a private match: invite/create allocates the match
// and issues the code, invite/resolve turns a code back into the match, and
// invite/revoke ends it. A code is an address, not permission: joining the match
// it names runs the same guild authorization as any join by id (D2).
//
// A refusal is a runtime.Error whose message is exactly one token the client
// maps to the wizard's text: invalid, expired, closed, denied, limit, rate. The
// one extra, "unavailable", is create finding no server; the client reads
// nothing else from the message.

const (
	// InviteCodeCollection holds one object per code, owned by the system user,
	// key = the code.
	InviteCodeCollection = "InviteCodes"
	// InviteOwnerCollection holds, per owner, the codes that may still be live,
	// so the live-code limit does not scan every code.
	InviteOwnerCollection = "InviteCodeOwners"

	// inviteAlphabet is A-Z without I L O, and 2-9: players read codes aloud and
	// type them on a controller, so 0 O 1 I L are out. 23 letters + 8 digits.
	inviteAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// inviteCodeLength: the wizard's entry field holds N characters, which is
	// level data and not yet measured (ADR 0017 D1). echovr-modkit reports 5.
	// Set this to N once it is measured; 31^5 is 28.6 million codes.
	inviteCodeLength = 5
	// inviteMaxCodeInput is the text container's bound (63): longer input cannot
	// be a code and is refused before any lookup.
	inviteMaxCodeInput = 63

	inviteDefaultExpiry  = 24 * time.Hour
	inviteMaxExpiry      = 7 * 24 * time.Hour
	inviteMaxLivePerUser = 5
)

// Seams: everything the invite RPCs reach outside storage through, so tests
// can drive them without a Discord bot or a game server.
var (
	inviteNow     = time.Now
	inviteLimiter = newInviteAttemptLimiter(time.Now)

	// inviteActiveGroup is the guild group the caller is playing in.
	inviteActiveGroup = defaultInviteActiveGroup
	// inviteAllocate allocates the private match, through the Discord create path.
	inviteAllocate = defaultInviteAllocate
	// inviteNewCode draws a fresh code; tests make it collide.
	inviteNewCode = newInviteCode
)

// StoredInvite is the value stored under a code.
type StoredInvite struct {
	MatchID   string    `json:"match_id"`
	OwnerID   string    `json:"owner_id"`
	GroupID   string    `json:"group_id"`
	Mode      string    `json:"mode"`
	Level     string    `json:"level"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

type storedInviteOwner struct {
	Codes []string `json:"codes"`
}

type inviteAllocRequest struct {
	UserID  string
	GroupID uuid.UUID
	Region  string
	Mode    evr.Symbol
	Level   evr.Symbol
}

func inviteRefusal(token string) error {
	code := StatusInvalidArgument
	switch token {
	case "denied":
		code = StatusPermissionDenied
	case "expired", "closed":
		code = StatusFailedPrecondition
	case "limit", "rate":
		code = StatusResourceExhausted
	case "unavailable":
		code = StatusUnavailable
	}
	return runtime.NewError(token, code)
}

// ---- code ----------------------------------------------------------------

func newInviteCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(inviteAlphabet)))
	for i := 0; i < inviteCodeLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(inviteAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normalizeInviteCode upper-cases what was typed and drops spaces and hyphens,
// as the runtime does, and reports whether the rest can be a code.
func normalizeInviteCode(raw string) (string, bool) {
	if len(raw) > inviteMaxCodeInput*4 {
		return "", false
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if r == ' ' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	code := b.String()
	if len(code) != inviteCodeLength {
		return "", false
	}
	for _, r := range code {
		if !strings.ContainsRune(inviteAlphabet, r) {
			return "", false
		}
	}
	return code, true
}

// inviteCodeFingerprint identifies a code in logs without revealing it. A code
// is a bearer secret and the code space is small (31^5), so any long digest of
// it is reversible by enumeration; 16 bits leaves hundreds of codes per value.
func inviteCodeFingerprint(code string) string {
	sum := sha256.Sum256([]byte("invite-code:" + code))
	return hex.EncodeToString(sum[:2])
}

// ---- attempt limiter -----------------------------------------------------

// inviteAttemptLimiter is a token bucket per key (user id, source address).
// An attempt costs one token and a refusal costs inviteMissCost more, so a
// guesser runs out long before someone who types a code they were given.
type inviteAttemptLimiter struct {
	now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*inviteBucket
}

type inviteBucket struct {
	tokens float64
	last   time.Time
}

const (
	inviteBucketCapacity = 30.0
	inviteRefillPerSec   = 0.2 // one token per 5 s
	inviteMissCost       = 4.0
)

func newInviteAttemptLimiter(now func() time.Time) *inviteAttemptLimiter {
	return &inviteAttemptLimiter{now: now, buckets: make(map[string]*inviteBucket)}
}

func (l *inviteAttemptLimiter) bucketLocked(key string, now time.Time) *inviteBucket {
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 4096 {
			// A bucket idle long enough to be full again carries no information.
			full := time.Duration(inviteBucketCapacity/inviteRefillPerSec) * time.Second
			for k, v := range l.buckets {
				if now.Sub(v.last) > full {
					delete(l.buckets, k)
				}
			}
		}
		b = &inviteBucket{tokens: inviteBucketCapacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(inviteBucketCapacity, b.tokens+now.Sub(b.last).Seconds()*inviteRefillPerSec)
	b.last = now
	return b
}

// allow spends one token from every key, or none if any key is out.
func (l *inviteAttemptLimiter) allow(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bs := make([]*inviteBucket, len(keys))
	for i, k := range keys {
		bs[i] = l.bucketLocked(k, now)
		if bs[i].tokens < 1 {
			return false
		}
	}
	for _, b := range bs {
		b.tokens--
	}
	return true
}

// penalize charges the extra cost of a refused attempt.
func (l *inviteAttemptLimiter) penalize(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		b := l.bucketLocked(k, now)
		b.tokens = max(0, b.tokens-inviteMissCost)
	}
}

func inviteLimiterKeys(ctx context.Context, userID string) []string {
	ip, _ := ctx.Value(runtime.RUNTIME_CTX_CLIENT_IP).(string)
	if ip == "" {
		ip = "unknown"
	}
	return []string{"user:" + userID, "addr:" + ip}
}

// ---- the record ------------------------------------------------------------

// inviteRecord collects what one invite RPC call did, for its one log record.
type inviteRecord struct {
	args    map[string]any
	matchID string
	groupID string
	code    string // never logged; its fingerprint is
	errText string
}

type inviteRecordKey struct{}

func inviteRecordFrom(ctx context.Context) *inviteRecord {
	if r, ok := ctx.Value(inviteRecordKey{}).(*inviteRecord); ok {
		return r
	}
	return &inviteRecord{} // a handler called without the wrapper writes nowhere
}

// withInviteRecord wraps an invite RPC so that every call, accepted or refused,
// writes exactly one structured record: who (user, session, client, address),
// what (the operation, its arguments, the resolved match and guild ids), the
// outcome, the latency and the size of the answer. It is the single point the
// three RPCs pass through. The code is a bearer secret: only its fingerprint
// is written.
func withInviteRecord(op string, fn func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error)) func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error) {
	return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		start := inviteNow()
		rec := &inviteRecord{args: map[string]any{}}
		out, err := fn(context.WithValue(ctx, inviteRecordKey{}, rec), logger, db, nk, payload)

		outcome := "ok"
		if err != nil {
			var rerr *runtime.Error
			if errors.As(err, &rerr) {
				outcome = rerr.Message
			} else {
				outcome = "internal"
			}
		}
		userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
		username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)
		sessionID, _ := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)
		clientIP, _ := ctx.Value(runtime.RUNTIME_CTX_CLIENT_IP).(string)
		clientPort, _ := ctx.Value(runtime.RUNTIME_CTX_CLIENT_PORT).(string)

		fields := map[string]interface{}{
			"rpc_id":      "invite/" + op,
			"user_id":     userID,
			"username":    username,
			"session_id":  sessionID,
			"client_ip":   clientIP,
			"client_port": clientPort,
			"args":        rec.args,
			"outcome":     outcome,
			"latency_ms":  inviteNow().Sub(start).Milliseconds(),
			"result_size": len(out),
		}
		if rec.matchID != "" {
			fields["match_id"] = rec.matchID
		}
		if rec.groupID != "" {
			fields["group_id"] = rec.groupID
		}
		if rec.code != "" {
			fields["code_fp"] = inviteCodeFingerprint(rec.code)
			fields["code_len"] = len(rec.code)
		}
		if rec.errText != "" {
			fields["error"] = rec.errText
		}
		l := logger.WithFields(fields)
		if outcome == "internal" {
			l.Error("RPC")
		} else {
			l.Info("RPC")
		}
		return out, err
	}
}

// ---- storage -----------------------------------------------------------------

func loadInvite(ctx context.Context, nk runtime.NakamaModule, code string) (*StoredInvite, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: InviteCodeCollection, Key: code, UserID: SystemUserID}})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, nil
	}
	inv := &StoredInvite{}
	if err := json.Unmarshal([]byte(objs[0].Value), inv); err != nil {
		return nil, err
	}
	return inv, nil
}

func writeInvite(ctx context.Context, nk runtime.NakamaModule, code string, inv *StoredInvite, version string) error {
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: InviteCodeCollection, Key: code, UserID: SystemUserID,
		Value: string(data), Version: version, PermissionRead: 0, PermissionWrite: 0,
	}})
	return err
}

// updateInviteOwner applies mutate to the owner's index under optimistic
// concurrency, retrying when another writer got there first.
func updateInviteOwner(ctx context.Context, nk runtime.NakamaModule, ownerID string, mutate func(*storedInviteOwner)) error {
	for attempt := 0; attempt < 5; attempt++ {
		idx := &storedInviteOwner{}
		version := "*"
		objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: InviteOwnerCollection, Key: ownerID, UserID: SystemUserID}})
		if err != nil {
			return err
		}
		if len(objs) > 0 {
			if err := json.Unmarshal([]byte(objs[0].Value), idx); err != nil {
				return err
			}
			version = objs[0].Version
		}
		mutate(idx)
		data, err := json.Marshal(idx)
		if err != nil {
			return err
		}
		_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection: InviteOwnerCollection, Key: ownerID, UserID: SystemUserID,
			Value: string(data), Version: version, PermissionRead: 0, PermissionWrite: 0,
		}})
		if err == nil {
			return nil
		}
		if !errors.Is(err, runtime.ErrStorageRejectedVersion) {
			return err
		}
	}
	return fmt.Errorf("invite owner index for %s kept changing", ownerID)
}

// inviteLive reports whether the code still names a joinable match: not
// revoked, not expired, and the match still running.
func inviteLive(ctx context.Context, nk runtime.NakamaModule, inv *StoredInvite) (token string, err error) {
	switch {
	case inv.Revoked:
		return "closed", nil
	case !inviteNow().Before(inv.ExpiresAt):
		return "expired", nil
	}
	matchID := MatchIDFromStringOrNil(inv.MatchID)
	if matchID.IsNil() {
		return "closed", nil
	}
	if _, err := MatchLabelByID(ctx, nk, matchID); err != nil {
		if errors.Is(err, ErrMatchNotFound) {
			return "closed", nil
		}
		return "", err
	}
	return "", nil
}

// ---- create --------------------------------------------------------------------

var inviteUserLocks sync.Map // user id -> *sync.Mutex

// lockInviteUser serializes one user's creates, so two concurrent calls cannot
// both pass the live-code limit. The deployment runs one node.
func lockInviteUser(userID string) func() {
	m, _ := inviteUserLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type inviteCreateRequest struct {
	Experience string `json:"experience"`
	Mode       string `json:"mode"`
	Map        string `json:"map"`
	Region     string `json:"region"`
	ExpiresS   int    `json:"expires_s"`
}

type inviteCreateResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
	MatchID   string `json:"match_id"`
}

// parseInviteMode maps the wizard's experience (and optional mode text) to the
// private mode to allocate. Every invite game is private: a public mode asked
// for is allocated as its private twin. Mode text this service does not know is
// ignored in favour of the experience's default; a mode of another experience
// than the one chosen is a contradiction and is refused.
func parseInviteMode(experience, mode string) (evr.Symbol, bool) {
	var base evr.Symbol
	switch strings.ToLower(strings.TrimSpace(experience)) {
	case "arena":
		base = evr.ModeArenaPrivate
	case "combat":
		base = evr.ModeCombatPrivate
	case "social":
		base = evr.ModeSocialPrivate
	default:
		return 0, false
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return base, true
	}
	sym := evr.ToSymbol(mode)
	if _, known := evr.LevelsByMode[sym]; !known {
		var sg evr.StatisticsGroup
		if err := sg.UnmarshalText([]byte(mode)); err == nil {
			sym = sg.Mode
		}
	}
	switch sym {
	case evr.ModeArenaPublic, evr.ModeArenaPrivate:
		sym = evr.ModeArenaPrivate
	case evr.ModeCombatPublic, evr.ModeCombatPrivate:
		sym = evr.ModeCombatPrivate
	case evr.ModeSocialPublic, evr.ModeSocialPrivate:
		sym = evr.ModeSocialPrivate
	default:
		return base, true
	}
	return sym, sym == base
}

// InviteCreateRPC allocates a private match for the caller and issues a code for it.
func InviteCreateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	rec := inviteRecordFrom(ctx)
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}

	req := inviteCreateRequest{}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", inviteRefusal("invalid")
	}
	rec.args["experience"], rec.args["mode"], rec.args["map"], rec.args["region"], rec.args["expires_s"] = req.Experience, req.Mode, req.Map, req.Region, req.ExpiresS

	mode, ok := parseInviteMode(req.Experience, req.Mode)
	if !ok {
		return "", inviteRefusal("invalid")
	}
	level := evr.LevelUnspecified
	if req.Map != "" {
		level = evr.ToSymbol(req.Map)
		if levels := evr.LevelsByMode[mode]; !containsSymbol(levels, level) {
			return "", inviteRefusal("invalid")
		}
	}
	expiry := inviteDefaultExpiry
	switch {
	case req.ExpiresS < 0:
		return "", inviteRefusal("invalid")
	case req.ExpiresS > 0:
		expiry = min(time.Duration(req.ExpiresS)*time.Second, inviteMaxExpiry)
	}

	unlock := lockInviteUser(userID)
	defer unlock()

	groupID, err := inviteActiveGroup(ctx, nk, userID)
	if err != nil {
		return "", fmt.Errorf("invite create: active guild: %w", err)
	}
	if groupID == uuid.Nil {
		return "", inviteRefusal("denied")
	}
	rec.groupID = groupID.String()

	live, err := liveInviteCodes(ctx, nk, userID)
	if err != nil {
		return "", fmt.Errorf("invite create: live codes: %w", err)
	}
	if len(live) >= inviteMaxLivePerUser {
		return "", inviteRefusal("limit")
	}

	label, err := inviteAllocate(ctx, logger, nk, inviteAllocRequest{UserID: userID, GroupID: groupID, Region: req.Region, Mode: mode, Level: level})
	if err != nil || label == nil {
		if err != nil {
			rec.errText = err.Error()
		}
		return "", inviteRefusal(inviteAllocationToken(err))
	}
	rec.matchID = label.ID.String()

	now := inviteNow()
	inv := &StoredInvite{
		MatchID: label.ID.String(), OwnerID: userID, GroupID: groupID.String(),
		Mode: label.Mode.String(), Level: label.Level.String(),
		CreatedAt: now.UTC(), ExpiresAt: now.Add(expiry).UTC(),
	}
	code := ""
	for attempt := 0; attempt < 10 && code == ""; attempt++ {
		candidate, err := inviteNewCode()
		if err != nil {
			return "", fmt.Errorf("invite create: code: %w", err)
		}
		// Version "*" writes only if the code is not taken.
		switch err := writeInvite(ctx, nk, candidate, inv, "*"); {
		case err == nil:
			code = candidate
		case errors.Is(err, runtime.ErrStorageRejectedVersion):
		default:
			return "", fmt.Errorf("invite create: store: %w", err)
		}
	}
	if code == "" {
		return "", errors.New("invite create: no free code after 10 attempts")
	}
	rec.code = code

	if err := updateInviteOwner(ctx, nk, userID, func(idx *storedInviteOwner) {
		idx.Codes = append(slicesKeep(idx.Codes, live), code)
	}); err != nil {
		return "", fmt.Errorf("invite create: owner index: %w", err)
	}

	out, err := json.Marshal(inviteCreateResponse{Code: code, ExpiresAt: inv.ExpiresAt.Format(time.RFC3339), MatchID: inv.MatchID})
	return string(out), err
}

func containsSymbol(list []evr.Symbol, s evr.Symbol) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// slicesKeep returns the members of codes that are also in keep.
func slicesKeep(codes, keep []string) []string {
	out := make([]string, 0, len(keep))
	for _, c := range codes {
		for _, k := range keep {
			if c == k {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// liveInviteCodes returns the owner's codes that are still joinable.
func liveInviteCodes(ctx context.Context, nk runtime.NakamaModule, ownerID string) ([]string, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: InviteOwnerCollection, Key: ownerID, UserID: SystemUserID}})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, nil
	}
	idx := &storedInviteOwner{}
	if err := json.Unmarshal([]byte(objs[0].Value), idx); err != nil {
		return nil, err
	}
	var live []string
	for _, code := range idx.Codes {
		inv, err := loadInvite(ctx, nk, code)
		if err != nil {
			return nil, err
		}
		if inv == nil {
			continue
		}
		token, err := inviteLive(ctx, nk, inv)
		if err != nil {
			return nil, err
		}
		if token == "" {
			live = append(live, code)
		}
	}
	return live, nil
}

func inviteAllocationToken(err error) string {
	switch status.Code(err) {
	case codes.PermissionDenied:
		return "denied"
	case codes.ResourceExhausted:
		return "rate"
	case codes.InvalidArgument:
		return "invalid"
	}
	return "unavailable"
}

// ---- resolve -------------------------------------------------------------------

type inviteResolveRequest struct {
	Code string `json:"code"`
}

type inviteResolveResponse struct {
	MatchID string `json:"match_id"`
	Mode    string `json:"mode"`
	Level   string `json:"level"`
}

// refuseInviteAttempt charges the extra cost of a refused attempt, then refuses.
func refuseInviteAttempt(ctx context.Context, userID, token string) error {
	inviteLimiter.penalize(inviteLimiterKeys(ctx, userID)...)
	return inviteRefusal(token)
}

// InviteResolveRPC answers a code with the match it names. It is an address,
// not permission: the join that follows runs the guild authorization.
func InviteResolveRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	rec := inviteRecordFrom(ctx)
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}
	req := inviteResolveRequest{}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", inviteRefusal("invalid")
	}
	rec.args["code_len"] = len(req.Code)

	if !inviteLimiter.allow(inviteLimiterKeys(ctx, userID)...) {
		return "", inviteRefusal("rate")
	}
	code, ok := normalizeInviteCode(req.Code)
	if !ok {
		return "", refuseInviteAttempt(ctx, userID, "invalid")
	}
	rec.code = code

	inv, err := loadInvite(ctx, nk, code)
	if err != nil {
		return "", fmt.Errorf("invite resolve: %w", err)
	}
	if inv == nil {
		return "", refuseInviteAttempt(ctx, userID, "invalid")
	}
	rec.matchID, rec.groupID = inv.MatchID, inv.GroupID

	token, err := inviteLive(ctx, nk, inv)
	if err != nil {
		return "", fmt.Errorf("invite resolve: %w", err)
	}
	if token != "" {
		return "", refuseInviteAttempt(ctx, userID, token)
	}
	out, err := json.Marshal(inviteResolveResponse{MatchID: inv.MatchID, Mode: inv.Mode, Level: inv.Level})
	return string(out), err
}

// ---- revoke --------------------------------------------------------------------

// InviteRevokeRPC ends a code. Only its owner may.
func InviteRevokeRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	rec := inviteRecordFrom(ctx)
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}
	req := inviteResolveRequest{}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", inviteRefusal("invalid")
	}
	rec.args["code_len"] = len(req.Code)

	// Revoke answers denied for someone else's code and invalid for an unknown
	// one, which tells a guesser a code exists; it shares resolve's budget.
	if !inviteLimiter.allow(inviteLimiterKeys(ctx, userID)...) {
		return "", inviteRefusal("rate")
	}
	code, ok := normalizeInviteCode(req.Code)
	if !ok {
		return "", refuseInviteAttempt(ctx, userID, "invalid")
	}
	rec.code = code

	for attempt := 0; attempt < 5; attempt++ {
		objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: InviteCodeCollection, Key: code, UserID: SystemUserID}})
		if err != nil {
			return "", fmt.Errorf("invite revoke: %w", err)
		}
		if len(objs) == 0 {
			return "", refuseInviteAttempt(ctx, userID, "invalid")
		}
		inv := &StoredInvite{}
		if err := json.Unmarshal([]byte(objs[0].Value), inv); err != nil {
			return "", fmt.Errorf("invite revoke: %w", err)
		}
		rec.matchID, rec.groupID = inv.MatchID, inv.GroupID
		if inv.OwnerID != userID {
			return "", refuseInviteAttempt(ctx, userID, "denied")
		}
		if inv.Revoked {
			return partyOKResponse, nil
		}
		inv.Revoked = true
		switch err := writeInvite(ctx, nk, code, inv, objs[0].Version); {
		case err == nil:
			if err := updateInviteOwner(ctx, nk, userID, func(idx *storedInviteOwner) {
				idx.Codes = removeString(idx.Codes, code)
			}); err != nil {
				return "", fmt.Errorf("invite revoke: owner index: %w", err)
			}
			return partyOKResponse, nil
		case errors.Is(err, runtime.ErrStorageRejectedVersion):
			continue
		default:
			return "", fmt.Errorf("invite revoke: %w", err)
		}
	}
	return "", errors.New("invite revoke: the code kept changing")
}

func removeString(list []string, s string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// ---- defaults for the seams ----------------------------------------------------

func defaultInviteActiveGroup(ctx context.Context, nk runtime.NakamaModule, userID string) (uuid.UUID, error) {
	profile, err := EVRProfileLoad(ctx, nk, userID)
	if err != nil {
		return uuid.Nil, err
	}
	return profile.GetActiveGroupID(), nil
}

// defaultInviteAllocate allocates through the Discord create path
// (handleCreateMatch), so the guild's membership, suspension, level and rate
// checks apply exactly as they do to /create.
func defaultInviteAllocate(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, req inviteAllocRequest) (*MatchLabel, error) {
	appBot := globalAppBot.Load()
	if appBot == nil || appBot.guildGroupRegistry == nil {
		return nil, errors.New("the Discord bot is not running")
	}
	gg := appBot.guildGroupRegistry.Get(req.GroupID.String())
	if gg == nil {
		return nil, status.Error(codes.PermissionDenied, "the caller's guild is not known")
	}
	label, _, err := appBot.handleCreateMatch(ctx, logger, req.UserID, gg.GuildID, req.Region, req.Mode, req.Level, inviteNow().Add(90*time.Second), []string{req.UserID}, AnyTeam)
	return label, err
}
