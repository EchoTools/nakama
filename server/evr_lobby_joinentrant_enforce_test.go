package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// --- mocks ---

// seatTestNK is a minimal NakamaModule mock for enforcement tests.
type seatTestNK struct {
	runtime.NakamaModule
	objects map[string]*api.StorageObject
}

func newSeatTestNK() *seatTestNK {
	return &seatTestNK{objects: make(map[string]*api.StorageObject)}
}

func seatKey(userID, collection, key string) string {
	return userID + ":" + collection + ":" + key
}

func (m *seatTestNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	acks := make([]*api.StorageObjectAck, len(writes))
	for i, w := range writes {
		k := seatKey(w.UserID, w.Collection, w.Key)
		m.objects[k] = &api.StorageObject{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Value:      w.Value,
			Version:    "v1",
		}
		acks[i] = &api.StorageObjectAck{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
			Version:    "v1",
		}
	}
	return acks, nil
}

func (m *seatTestNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	out := make([]*api.StorageObject, 0, len(reads))
	for _, r := range reads {
		if obj, ok := m.objects[seatKey(r.UserID, r.Collection, r.Key)]; ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// seatTestSession is a minimal Session mock carrying a context with SessionParameters.
type seatTestSession struct {
	Session // embed interface — only Context() and UserID() need to be real
	ctx     context.Context
	userID  uuid.UUID
}

func (s *seatTestSession) Context() context.Context { return s.ctx }
func (s *seatTestSession) UserID() uuid.UUID        { return s.userID }

// newSeatTestSession builds a session whose context contains SessionParameters
// with the given enforcementUserIDs.
func newSeatTestSession(userID uuid.UUID, enforcementUserIDs []string) *seatTestSession {
	params := &SessionParameters{
		enforcementUserIDs: enforcementUserIDs,
	}
	ptr := atomic.NewPointer(params)
	ctx := context.WithValue(context.Background(), ctxSessionParametersKey{}, ptr)
	return &seatTestSession{ctx: ctx, userID: userID}
}

// writeSuspension writes a suspension enforcement journal for a user in a guild.
func writeSuspension(t *testing.T, nk *seatTestNK, userID, groupID string, expiry time.Time, notice string) {
	t.Helper()
	record := GuildEnforcementRecord{
		ID:             uuid.Must(uuid.NewV4()).String(),
		UserID:         userID,
		GroupID:        groupID,
		Expiry:         expiry,
		UserNoticeText: notice,
		CreatedAt:      time.Now().Add(-1 * time.Hour),
	}
	journal := NewGuildEnforcementJournal(userID)
	journal.RecordsByGroupID = map[string][]GuildEnforcementRecord{
		groupID: {record},
	}

	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatalf("marshal journal: %v", err)
	}
	_, err = nk.StorageWrite(context.Background(), []*runtime.StorageWrite{{
		Collection: StorageCollectionEnforcementJournal,
		Key:        StorageKeyEnforcementJournal,
		UserID:     userID,
		Value:      string(data),
	}})
	if err != nil {
		t.Fatalf("write journal: %v", err)
	}
}

// seatTestGuildGroupRegistry builds a minimal GuildGroupRegistry with the given guilds.
func seatTestGuildGroupRegistry(guilds map[string]*GuildGroup) *GuildGroupRegistry {
	guildMap := make(map[string]*GuildGroup, len(guilds))
	for k, v := range guilds {
		guildMap[k] = v
	}
	inheritance := make(map[string][]string)
	return &GuildGroupRegistry{
		guildGroups:    atomic.NewPointer(&guildMap),
		inheritanceMap: atomic.NewPointer(&inheritance),
	}
}

func seatTestGuildGroup(groupID, name string, rejectSuspendedAlts bool) *GuildGroup {
	return &GuildGroup{
		GroupMetadata: GroupMetadata{
			RejectPlayersWithSuspendedAlternates: rejectSuspendedAlts,
		},
		State: &GuildGroupState{},
		Group: &api.Group{
			Id:   groupID,
			Name: name,
		},
	}
}

func makeLabel(groupID string, mode evr.Symbol) *MatchLabel {
	id := uuid.FromStringOrNil(groupID)
	return &MatchLabel{
		Mode:    mode,
		GroupID: &id,
	}
}

// --- tests ---

func TestEnforceJoinSuspension_RejectsSuspendedPlayer(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newSeatTestNK()

	writeSuspension(t, nk, userID, groupID, time.Now().Add(24*time.Hour), "test suspension")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})

	session := newSeatTestSession(uuid.FromStringOrNil(userID), []string{userID})

	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session)
	if err == nil {
		t.Fatal("expected enforceJoinSuspension to reject a suspended player, got nil")
	}
	if lobbyErr, ok := err.(LobbyError); !ok || lobbyErr.Code() != KickedFromLobbyGroup {
		t.Fatalf("expected KickedFromLobbyGroup LobbyError, got %T: %v", err, err)
	}
}

func TestEnforceJoinSuspension_AllowsCleanPlayer(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newSeatTestNK()

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})

	session := newSeatTestSession(uuid.FromStringOrNil(userID), []string{userID})

	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session)
	if err != nil {
		t.Fatalf("expected nil for clean player, got: %v", err)
	}
}

func TestEnforceJoinSuspension_GuildIsolation(t *testing.T) {
	t.Parallel()

	groupA := uuid.Must(uuid.NewV4()).String()
	groupB := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newSeatTestNK()

	writeSuspension(t, nk, userID, groupA, time.Now().Add(24*time.Hour), "guild A suspension")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupA: seatTestGuildGroup(groupA, "GuildA", false),
		groupB: seatTestGuildGroup(groupB, "GuildB", false),
	})

	session := newSeatTestSession(uuid.FromStringOrNil(userID), []string{userID})

	// Rejected from guild A.
	if err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupA, evr.ModeArenaPublic), session); err == nil {
		t.Fatal("expected rejection from guild A, got nil")
	}

	// Allowed into guild B.
	if err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupB, evr.ModeArenaPublic), session); err != nil {
		t.Fatalf("expected clean join to guild B, got: %v", err)
	}
}

func TestEnforceJoinSuspension_AltRejectedWhenGuildRequires(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	mainUserID := uuid.Must(uuid.NewV4()).String()
	altUserID := uuid.Must(uuid.NewV4()).String()
	nk := newSeatTestNK()

	writeSuspension(t, nk, mainUserID, groupID, time.Now().Add(24*time.Hour), "main suspended")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "StrictGuild", true),
	})

	// Alt's enforcementUserIDs includes both self and suspended main.
	session := newSeatTestSession(uuid.FromStringOrNil(altUserID), []string{altUserID, mainUserID})

	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session)
	if err == nil {
		t.Fatal("expected alt to be rejected when guild requires it, got nil")
	}
}

func TestEnforceJoinSuspension_AltAllowedWhenGuildPermits(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	mainUserID := uuid.Must(uuid.NewV4()).String()
	altUserID := uuid.Must(uuid.NewV4()).String()
	nk := newSeatTestNK()

	writeSuspension(t, nk, mainUserID, groupID, time.Now().Add(24*time.Hour), "main suspended")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "LenientGuild", false),
	})

	session := newSeatTestSession(uuid.FromStringOrNil(altUserID), []string{altUserID, mainUserID})

	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session)
	if err != nil {
		t.Fatalf("expected alt to be allowed when guild permits it, got: %v", err)
	}
}

func TestEnforceJoinSuspension_ExpiredSuspensionAllowed(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV4()).String()
	userID := uuid.Must(uuid.NewV4()).String()
	nk := newSeatTestNK()

	writeSuspension(t, nk, userID, groupID, time.Now().Add(-1*time.Hour), "expired")

	ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
		groupID: seatTestGuildGroup(groupID, "TestGuild", false),
	})

	session := newSeatTestSession(uuid.FromStringOrNil(userID), []string{userID})

	err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
		makeLabel(groupID, evr.ModeArenaPublic), session)
	if err != nil {
		t.Fatalf("expected expired suspension to be allowed, got: %v", err)
	}
}
