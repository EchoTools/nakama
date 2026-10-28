package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// A party roster is the persisted shadow of an in-game (SNS) party: who is in
// it and who leads it. The in-memory party (PartyHandler plus its tracker
// stream) stays the source of truth while anyone is online and the roster
// follows it; the roster exists so the website can show a party whose members
// are all offline, which the in-memory party cannot (it ends and drops its data
// when its last session closes, see snsPartyEnded).
//
// Two objects per party:
//   - PartyRosterCollection/<party uuid>, owned by the system user: the roster.
//   - PartyRosterPointerCollection/current, owned by each member: which roster
//     that user is in, so "my party" is a point read, not a scan.
//
// The pointer is a hint. The roster is the truth, and party/mine only believes
// a pointer whose roster still lists the caller, so a crash between the two
// writes cannot show a player a party they are not in.
//
// A session closing does NOT remove anyone: a client that disconnects sends no
// leave, and the party is meant to outlive that. Only an explicit leave, a kick,
// or joining another party removes a member.
const (
	PartyRosterCollection        = "PartyRosters"
	PartyRosterPointerCollection = "PartyRosterPointers"
	partyRosterPointerKey        = "current"

	// partyRosterRetention is how long a roster with nobody online is kept after
	// its last change. Past it, party/mine treats the party as gone and deletes it.
	partyRosterRetention = 30 * 24 * time.Hour

	partyRosterWriteAttempts = 5
	partyRosterOpTimeout     = 5 * time.Second
)

// PartyRosterMember is one member of a persisted party.
type PartyRosterMember struct {
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	JoinedAt time.Time `json:"joined_at"`
}

// PartyRoster is the persisted membership of one in-game party.
type PartyRoster struct {
	PartyID   string              `json:"party_id"`
	LeaderID  string              `json:"leader_id"`
	Members   []PartyRosterMember `json:"members"`
	UpdatedAt time.Time           `json:"updated_at"`
}

func (r *PartyRoster) hasMember(userID string) bool {
	return slices.ContainsFunc(r.Members, func(m PartyRosterMember) bool { return m.UserID == userID })
}

type partyRosterPointer struct {
	PartyID string `json:"party_id"`
}

// PartyRosterStore reads and writes rosters. A nil store is a valid no-op
// store, so a pipeline built without one (most unit tests) records nothing.
type PartyRosterStore struct {
	nk  runtime.NakamaModule
	now func() time.Time
}

func NewPartyRosterStore(nk runtime.NakamaModule) *PartyRosterStore {
	return &PartyRosterStore{nk: nk, now: func() time.Time { return time.Now().UTC() }}
}

// readRoster returns the roster and its storage version, or (nil, "", nil) if there is none.
func (s *PartyRosterStore) readRoster(ctx context.Context, partyID string) (*PartyRoster, string, error) {
	objs, err := s.nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: PartyRosterCollection, Key: partyID, UserID: SystemUserID}})
	if err != nil {
		return nil, "", err
	}
	if len(objs) == 0 {
		return nil, "", nil
	}
	roster := &PartyRoster{}
	if err := json.Unmarshal([]byte(objs[0].Value), roster); err != nil {
		return nil, "", fmt.Errorf("decode roster %s: %w", partyID, err)
	}
	return roster, objs[0].Version, nil
}

// updateRoster applies fn to the roster under optimistic concurrency. fn gets a
// fresh roster (PartyID set, no members) when none exists, and returns false to
// make no write. A roster fn leaves with no members is deleted.
func (s *PartyRosterStore) updateRoster(ctx context.Context, partyID string, fn func(r *PartyRoster) bool) (*PartyRoster, error) {
	for range partyRosterWriteAttempts {
		roster, version, err := s.readRoster(ctx, partyID)
		if err != nil {
			return nil, err
		}
		if roster == nil {
			roster, version = &PartyRoster{PartyID: partyID}, "*"
		}
		if !fn(roster) {
			return roster, nil
		}
		roster.UpdatedAt = s.now()

		if len(roster.Members) == 0 {
			err = s.nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: PartyRosterCollection, Key: partyID, UserID: SystemUserID, Version: version}})
		} else {
			var data []byte
			if data, err = json.Marshal(roster); err != nil {
				return nil, err
			}
			_, err = s.nk.StorageWrite(ctx, []*runtime.StorageWrite{{
				Collection: PartyRosterCollection, Key: partyID, UserID: SystemUserID,
				Value: string(data), Version: version, PermissionRead: 0, PermissionWrite: 0,
			}})
		}
		if errors.Is(err, runtime.ErrStorageRejectedVersion) {
			continue
		}
		return roster, err
	}
	return nil, fmt.Errorf("roster %s: %d concurrent writers in a row", partyID, partyRosterWriteAttempts)
}

func (s *PartyRosterStore) readPointer(ctx context.Context, userID string) (string, error) {
	objs, err := s.nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: PartyRosterPointerCollection, Key: partyRosterPointerKey, UserID: userID}})
	if err != nil || len(objs) == 0 {
		return "", err
	}
	ptr := partyRosterPointer{}
	if err := json.Unmarshal([]byte(objs[0].Value), &ptr); err != nil {
		return "", fmt.Errorf("decode pointer of %s: %w", userID, err)
	}
	return ptr.PartyID, nil
}

func (s *PartyRosterStore) writePointer(ctx context.Context, userID, partyID string) error {
	data, err := json.Marshal(partyRosterPointer{PartyID: partyID})
	if err != nil {
		return err
	}
	_, err = s.nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: PartyRosterPointerCollection, Key: partyRosterPointerKey, UserID: userID,
		Value: string(data), PermissionRead: 1, PermissionWrite: 0,
	}})
	return err
}

func (s *PartyRosterStore) deletePointer(ctx context.Context, userID string) error {
	return s.nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: PartyRosterPointerCollection, Key: partyRosterPointerKey, UserID: userID}})
}

// Join records member as being in partyID, with leaderID leading it (empty keeps
// the roster's leader, or makes the member the leader of a new roster). Joining
// a party leaves the roster the member was in before, as it does in game.
func (s *PartyRosterStore) Join(partyID, leaderID string, member PartyRosterMember) error {
	if s == nil {
		return nil
	}
	if member.JoinedAt.IsZero() {
		member.JoinedAt = s.now()
	}
	ctx, cancel := context.WithTimeout(context.Background(), partyRosterOpTimeout)
	defer cancel()

	var prev string
	var prevErr error
	if prev, prevErr = s.readPointer(ctx, member.UserID); prev != "" && prev != partyID {
		if _, err := s.updateRoster(ctx, prev, func(r *PartyRoster) bool { return removeRosterMember(r, member.UserID, "") }); err != nil {
			prevErr = err
		}
	}

	_, err := s.updateRoster(ctx, partyID, func(r *PartyRoster) bool {
		if i := slices.IndexFunc(r.Members, func(m PartyRosterMember) bool { return m.UserID == member.UserID }); i >= 0 {
			member.JoinedAt = r.Members[i].JoinedAt
			r.Members[i] = member
		} else {
			r.Members = append(r.Members, member)
		}
		switch {
		case leaderID != "":
			r.LeaderID = leaderID
		case r.LeaderID == "":
			r.LeaderID = member.UserID
		}
		return true
	})
	if err != nil {
		return err
	}
	if err := s.writePointer(ctx, member.UserID, partyID); err != nil {
		return err
	}
	return prevErr
}

// Leave removes userID from partyID. leaderID is who leads it now, if the party
// still exists in memory; empty lets a roster that lost its leader take the
// longest-standing remaining member.
func (s *PartyRosterStore) Leave(partyID, userID, leaderID string) error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), partyRosterOpTimeout)
	defer cancel()

	if _, err := s.updateRoster(ctx, partyID, func(r *PartyRoster) bool { return removeRosterMember(r, userID, leaderID) }); err != nil {
		return err
	}
	// Clear the pointer only if it still points here: joining another party moved it.
	if cur, err := s.readPointer(ctx, userID); err != nil {
		return err
	} else if cur == partyID {
		return s.deletePointer(ctx, userID)
	}
	return nil
}

// SetLeader records a leadership change.
func (s *PartyRosterStore) SetLeader(partyID, leaderID string) error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), partyRosterOpTimeout)
	defer cancel()

	_, err := s.updateRoster(ctx, partyID, func(r *PartyRoster) bool {
		if r.LeaderID == leaderID || !r.hasMember(leaderID) {
			return false
		}
		r.LeaderID = leaderID
		return true
	})
	return err
}

// removeRosterMember drops userID from r and repairs the leader. It reports
// whether r changed.
func removeRosterMember(r *PartyRoster, userID, leaderID string) bool {
	if !r.hasMember(userID) {
		return false
	}
	r.Members = slices.DeleteFunc(r.Members, func(m PartyRosterMember) bool { return m.UserID == userID })
	switch {
	case len(r.Members) == 0:
		r.LeaderID = ""
	case leaderID != "" && r.hasMember(leaderID):
		r.LeaderID = leaderID
	case !r.hasMember(r.LeaderID):
		r.LeaderID = r.Members[0].UserID
	}
	return true
}

// Mine returns the roster userID is in, or nil. A pointer whose roster does not
// list the user is ignored.
func (s *PartyRosterStore) Mine(ctx context.Context, userID string) (*PartyRoster, error) {
	partyID, err := s.readPointer(ctx, userID)
	if err != nil || partyID == "" {
		return nil, err
	}
	roster, _, err := s.readRoster(ctx, partyID)
	if err != nil || roster == nil || !roster.hasMember(userID) {
		return nil, err
	}
	return roster, nil
}

// Delete removes a roster outright. Members' pointers are left: with no roster
// behind them they resolve to nothing.
func (s *PartyRosterStore) Delete(ctx context.Context, partyID string) error {
	return s.nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: PartyRosterCollection, Key: partyID, UserID: SystemUserID}})
}

// partyLeaderUserID is the user who leads the in-memory party, or "" when the
// party is gone or has no leader yet.
func (p *EvrPipeline) partyLeaderUserID(partyUUID uuid.UUID) string {
	ph, ok := p.nk.partyRegistry.Get(partyUUID)
	if !ok {
		return ""
	}
	ph.RLock()
	defer ph.RUnlock()
	if ph.leader == nil || ph.leader.UserPresence == nil {
		return ""
	}
	return ph.leader.UserPresence.UserId
}

// The in-game party flow calls these after it has changed the in-memory party.
// The roster follows the party and never decides anything for it, so a failed
// write is logged (Warn: someone should look) and the party flow carries on.

func (p *EvrPipeline) recordPartyJoin(logger *zap.Logger, session *sessionWS, partyUUID uuid.UUID) {
	if p.partyRoster == nil {
		return
	}
	member := PartyRosterMember{UserID: session.UserID().String(), Username: session.Username()}
	err := p.partyRoster.Join(partyUUID.String(), p.partyLeaderUserID(partyUUID), member)
	logPartyRosterChange(logger, "join", partyUUID, session.UserID(), err)
}

func (p *EvrPipeline) recordPartyLeave(logger *zap.Logger, partyUUID, userID uuid.UUID) {
	if p.partyRoster == nil {
		return
	}
	err := p.partyRoster.Leave(partyUUID.String(), userID.String(), p.partyLeaderUserID(partyUUID))
	logPartyRosterChange(logger, "leave", partyUUID, userID, err)
}

func (p *EvrPipeline) recordPartyLeader(logger *zap.Logger, partyUUID, leaderID uuid.UUID) {
	if p.partyRoster == nil {
		return
	}
	err := p.partyRoster.SetLeader(partyUUID.String(), leaderID.String())
	logPartyRosterChange(logger, "leader", partyUUID, leaderID, err)
}

func logPartyRosterChange(logger *zap.Logger, op string, partyUUID, userID uuid.UUID, err error) {
	fields := []zap.Field{zap.String("op", op), zap.String("party_id", partyUUID.String()), zap.String("uid", userID.String())}
	if err != nil {
		logger.Warn("Failed to record party roster change", append(fields, zap.Error(err))...)
		return
	}
	logger.Info("Party roster changed", fields...)
}
