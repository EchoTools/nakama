package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	protoLoadoutOwnedTag   = "rwd_tag_s1_vrml_s1_finalist"
	protoLoadoutUnownedTag = "rwd_tag_s1_vrml_s1_runnerup"
)

// protoLoadoutPresence is the small runtime.Presence the gate reads: a status
// string plus the identity/session ids a real stream presence carries.
type protoLoadoutPresence struct {
	status    string
	userID    uuid.UUID
	sessionID uuid.UUID
	node      string
}

func (p protoLoadoutPresence) GetHidden() bool      { return false }
func (p protoLoadoutPresence) GetPersistence() bool { return false }
func (p protoLoadoutPresence) GetUsername() string  { return "" }
func (p protoLoadoutPresence) GetStatus() string    { return p.status }
func (p protoLoadoutPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReasonUnknown
}
func (p protoLoadoutPresence) GetUserId() string    { return p.userID.String() }
func (p protoLoadoutPresence) GetSessionId() string { return p.sessionID.String() }
func (p protoLoadoutPresence) GetNodeId() string    { return p.node }

// protoLoadoutNK is an EVR test module with programmable streams, so the gate
// sees a game server, an entrant presence and the entrant's service presence.
type protoLoadoutNK struct {
	*evrTestNakamaModule
	account *api.Account
	streams map[string][]runtime.Presence
	users   map[string]runtime.PresenceMeta
}

func newProtoLoadoutNK() *protoLoadoutNK {
	return &protoLoadoutNK{
		evrTestNakamaModule: newEvrTestNakamaModule(),
		account:             &api.Account{User: &api.User{Id: uuid.Must(uuid.NewV4()).String(), Username: "entrant"}},
		streams:             map[string][]runtime.Presence{},
		users:               map[string]runtime.PresenceMeta{},
	}
}

func (m *protoLoadoutNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return m.account, nil
}

func streamKey(mode uint8, subject, label string) string {
	return string(rune(mode)) + "|" + subject + "|" + label
}

func (m *protoLoadoutNK) add(mode uint8, subject, label string, presence runtime.Presence) {
	k := streamKey(mode, subject, label)
	m.streams[k] = append(m.streams[k], presence)
}

func (m *protoLoadoutNK) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	return m.streams[streamKey(mode, subject, label)], nil
}

func (m *protoLoadoutNK) StreamUserGet(mode uint8, subject, subcontext, label, userID, sessionID string) (runtime.PresenceMeta, error) {
	return m.users[streamKey(mode, subject, label)+"|"+userID+"|"+sessionID], nil
}

// protoLoadoutFixture is one sender+entrant pair in the same match.
type protoLoadoutFixture struct {
	nk          *protoLoadoutNK
	matchID     MatchID
	senderID    uuid.UUID
	entrantID   uuid.UUID
	entrantSID  uuid.UUID
	entrantUser uuid.UUID
	msg         *rtapi.GameServerSaveLoadoutMessage
	profile     *EVRProfile
}

func newProtoLoadoutFixture(t *testing.T) *protoLoadoutFixture {
	t.Helper()
	nk := newProtoLoadoutNK()
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	senderID := uuid.Must(uuid.NewV4())
	entrantID := uuid.Must(uuid.NewV4())
	entrantSID := uuid.Must(uuid.NewV4())
	entrantUser := uuid.Must(uuid.NewV4())
	nk.account = &api.Account{User: &api.User{Id: entrantUser.String(), Username: "entrant"}, Wallet: `{"cosmetic:arena:` + protoLoadoutOwnedTag + `":1}`}

	// The game server's own presence carries its match ID as the status.
	nk.add(StreamModeGameServer, senderID.String(), StreamLabelMatchService,
		protoLoadoutPresence{status: matchID.String(), userID: uuid.Nil, sessionID: senderID, node: matchID.Node})
	// The entrant's stream presence.
	nk.add(StreamModeEntrant, entrantID.String(), matchID.Node,
		&EvrMatchPresence{Node: matchID.Node, SessionID: entrantSID, UserID: entrantUser})
	// The entrant's service presence, which GetMatchIDBySessionID verifies.
	nk.add(StreamModeService, entrantSID.String(), StreamLabelMatchService,
		protoLoadoutPresence{status: matchID.String(), userID: entrantUser, sessionID: entrantSID, node: matchID.Node})
	nk.users[streamKey(StreamModeMatchAuthoritative, matchID.UUID.String(), matchID.Node)+"|"+entrantUser.String()+"|"+entrantSID.String()] = protoLoadoutPresence{}

	profile := &EVRProfile{
		account:          nk.account,
		LoadoutCosmetics: AccountCosmetics{Loadout: evr.DefaultCosmeticLoadout()},
	}
	seedProtoLoadoutProfile(t, nk, entrantUser, profile)

	msg := &rtapi.GameServerSaveLoadoutMessage{
		LobbySessionId: matchID.UUID.String(),
		EntrantId:      entrantID.String(),
		JerseyNumber:   12,
		LoadoutInstances: []*rtapi.LoadoutInstance{{
			Items: []*rtapi.LoadoutItem{{
				SlotType:     uint64(evr.ToSymbol("tag")),
				EquippedItem: uint64(evr.ToSymbol(protoLoadoutOwnedTag)),
			}},
		}},
	}
	return &protoLoadoutFixture{nk: nk, matchID: matchID, senderID: senderID, entrantID: entrantID,
		entrantSID: entrantSID, entrantUser: entrantUser, msg: msg, profile: profile}
}

func seedProtoLoadoutProfile(t *testing.T, nk *protoLoadoutNK, userID uuid.UUID, profile *EVRProfile) {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	meta := profile.StorageMeta()
	if _, err := nk.StorageWrite(context.Background(), []*runtime.StorageWrite{{
		Collection: meta.Collection, Key: meta.Key, UserID: userID.String(), Value: string(data),
	}}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
}

func loadProtoLoadoutProfile(t *testing.T, nk *protoLoadoutNK, userID uuid.UUID) *EVRProfile {
	t.Helper()
	profile, err := EVRProfileLoad(context.Background(), nk, userID.String())
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	return profile
}

func TestProcessProtobufRequestHandlesGameServerSaveLoadout(t *testing.T) {
	p := &EvrPipeline{}
	ws := &sessionWS{id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()), ctx: context.Background()}
	msg := &rtapi.Envelope{Message: &rtapi.Envelope_GameServerSaveLoadout{GameServerSaveLoadout: &rtapi.GameServerSaveLoadoutMessage{EntrantId: "not-a-uuid"}}}
	err := p.ProcessProtobufRequest(zap.NewNop(), ws, msg)
	if err != nil && strings.Contains(err.Error(), "unhandled protobuf message type") {
		t.Fatalf("SaveLoadout is unhandled: %v", err)
	}
}

func TestProcessRequestEVRKeepsSessionForSaveLoadout(t *testing.T) {
	p := &EvrPipeline{}
	sessionID := uuid.Must(uuid.NewV4())
	ws := &sessionWS{id: sessionID, userID: uuid.Must(uuid.NewV4()), ctx: context.Background()}
	envelope := &rtapi.Envelope{Message: &rtapi.Envelope_GameServerSaveLoadout{GameServerSaveLoadout: &rtapi.GameServerSaveLoadoutMessage{EntrantId: "not-a-uuid"}}}
	payload, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if !p.ProcessRequestEVR(zap.NewNop(), ws, &evr.NEVRProtobufMessageV1{Payload: payload}) {
		t.Fatal("ProcessRequestEVR returned false; the session read loop would break")
	}
}

func TestGameServerSaveLoadoutProtoOwnMatchPersistsAndSanitizes(t *testing.T) {
	f := newProtoLoadoutFixture(t)
	ctx := context.Background()
	if err := applyGameServerSaveLoadoutProto(ctx, zap.NewNop(), f.nk, nil, f.senderID, f.msg); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := loadProtoLoadoutProfile(t, f.nk, f.entrantUser)
	if got.LoadoutCosmetics.Loadout.Tag != protoLoadoutOwnedTag {
		t.Fatalf("owned tag was not persisted: %q", got.LoadoutCosmetics.Loadout.Tag)
	}
	if got.LoadoutCosmetics.JerseyNumber != 12 {
		t.Fatalf("jersey number = %d, want 12", got.LoadoutCosmetics.JerseyNumber)
	}

	// The same handler, now with an unowned item: it must reset to the default.
	f.profile.account.Wallet = "{}"
	seedProtoLoadoutProfile(t, f.nk, f.entrantUser, f.profile)
	f.msg.LoadoutInstances[0].Items[0].EquippedItem = uint64(evr.ToSymbol(protoLoadoutUnownedTag))
	if err := applyGameServerSaveLoadoutProto(ctx, zap.NewNop(), f.nk, nil, f.senderID, f.msg); err != nil {
		t.Fatalf("handler (unowned): %v", err)
	}
	got = loadProtoLoadoutProfile(t, f.nk, f.entrantUser)
	if got.LoadoutCosmetics.Loadout.Tag == protoLoadoutUnownedTag {
		t.Fatal("unowned tag persisted")
	}
	if got.LoadoutCosmetics.Loadout.Tag != evr.DefaultCosmeticLoadout().Tag {
		t.Fatalf("unowned tag reset to %q, want default %q", got.LoadoutCosmetics.Loadout.Tag, evr.DefaultCosmeticLoadout().Tag)
	}
}

func TestGameServerSaveLoadoutProtoRejectsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(f *protoLoadoutFixture)
	}{
		{"entrant in another match", func(f *protoLoadoutFixture) {
			other := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "othernode"}
			// Replace the entrant presence with one in a different match stream.
			delete(f.nk.streams, streamKey(StreamModeEntrant, f.entrantID.String(), f.matchID.Node))
			f.nk.add(StreamModeEntrant, f.entrantID.String(), other.Node, &EvrMatchPresence{Node: other.Node, SessionID: f.entrantSID, UserID: f.entrantUser})
		}},
		{"sender has no game-server presence", func(f *protoLoadoutFixture) {
			delete(f.nk.streams, streamKey(StreamModeGameServer, f.senderID.String(), StreamLabelMatchService))
		}},
		{"non-UUID entrant", func(f *protoLoadoutFixture) {
			f.msg.EntrantId = "not-a-uuid"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProtoLoadoutFixture(t)
			tc.setup(f)
			before := f.nk.writeAttempts
			if err := applyGameServerSaveLoadoutProto(ctx, zap.NewNop(), f.nk, nil, f.senderID, f.msg); err != nil {
				t.Fatalf("handler returned an error (would drop the session): %v", err)
			}
			if f.nk.writeAttempts != before {
				t.Fatalf("profile was written %d time(s) for a rejected request", f.nk.writeAttempts-before)
			}
		})
	}
}

func TestLegacySaveLoadoutRequiresSenderGameServerOfMatch(t *testing.T) {
	f := newProtoLoadoutFixture(t)
	// Remove the sender's game-server presence: it is not the server of the match.
	delete(f.nk.streams, streamKey(StreamModeGameServer, f.senderID.String(), StreamLabelMatchService))
	before := f.nk.writeAttempts
	req := &evr.GameServerSaveLoadoutRequest{
		EntrantSessionID: f.entrantSID,
		EvrID:            evr.EvrId{},
		LoadoutNumber:    1,
		Loadout:          []byte(`{"slot":0,"number":1,"loadout_instances":[]}`),
	}
	if err := applyGameServerSaveLoadoutLegacy(context.Background(), zap.NewNop(), f.nk, nil, f.senderID, req); err != nil {
		t.Fatalf("legacy handler returned an error (would drop the session): %v", err)
	}
	if f.nk.writeAttempts != before {
		t.Fatalf("legacy handler wrote %d time(s) from a non-server session", f.nk.writeAttempts-before)
	}
}
