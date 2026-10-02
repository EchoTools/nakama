package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Recently met (proposal §2): who a player shared a match with, newest first, for the game's
// recently-met list (social slots 56-67; pnsovr read Oculus' list). Nothing else in Nakama records
// who played with whom, so each player's list is a storage object written when they leave a match.

const (
	StorageCollectionRecentlyMet = "RecentlyMet"
	StorageKeyRecentlyMet        = "list"
	recentlyMetCap               = 50 // pnsovr followed Oculus' pages; no cap of its own was found
)

// RecentlyMetUser is one person on a player's list.
type RecentlyMetUser struct {
	UserID      string    `json:"user_id"`
	AccountID   uint64    `json:"account_id"` // the Discord id, the account id on the wire
	DisplayName string    `json:"display_name"`
	LastMet     time.Time `json:"last_met"`
}

// RecentlyMetList is a player's list, newest meeting first. Only the server writes it; the owner may
// read it.
type RecentlyMetList struct {
	Users   []RecentlyMetUser `json:"users"`
	version string
}

func (l *RecentlyMetList) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionRecentlyMet,
		Key:             StorageKeyRecentlyMet,
		PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         l.version,
	}
}

func (l *RecentlyMetList) SetStorageMeta(meta StorableMetadata) { l.version = meta.Version }

// Add puts `met` at the front (each person once, their newest meeting kept), drops the owner and
// anyone in `blocked`, and keeps at most `limit`.
func (l *RecentlyMetList) Add(met []RecentlyMetUser, owner string, blocked map[string]bool, limit int) {
	merged := make([]RecentlyMetUser, 0, len(met)+len(l.Users))
	seen := map[string]bool{owner: true}
	for _, list := range [][]RecentlyMetUser{met, l.Users} {
		for _, u := range list {
			if seen[u.UserID] || blocked[u.UserID] || u.AccountID == 0 {
				continue
			}
			seen[u.UserID] = true
			merged = append(merged, u)
		}
	}
	if len(merged) > limit {
		merged = merged[:limit]
	}
	l.Users = merged
}

// recentlyMetIn is who `self` met in a match, from the match's participations (everyone who ever
// joined): each other player whose time in the match overlapped self's (still present, or left after
// self joined). Moderators are invisible to players and are not met. Ordered by user id.
func recentlyMetIn(self string, participations map[string]*PlayerParticipation, now time.Time) []RecentlyMetUser {
	me, ok := participations[self]
	if !ok || me == nil {
		return nil
	}
	met := []RecentlyMetUser{}
	for userID, p := range participations {
		if p == nil || userID == self || p.Team == Moderator {
			continue
		}
		if !p.LeaveTime.IsZero() && !p.LeaveTime.After(me.JoinTime) {
			continue // left before self arrived
		}
		accountID, ok := discordAccountID(p.DiscordID)
		if !ok {
			continue
		}
		met = append(met, RecentlyMetUser{UserID: userID, AccountID: accountID, DisplayName: p.DisplayName, LastMet: now})
	}
	sort.Slice(met, func(i, j int) bool { return met[i].UserID < met[j].UserID })
	return met
}

// blockedBetween is which of `others` have a block with `userID` in either direction (user_edge state
// 3): neither should see the other on their list.
func blockedBetween(ctx context.Context, db *sql.DB, userID string, others []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(others) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT destination_id FROM user_edge WHERE source_id = $1 AND destination_id = ANY($2::UUID[]) AND state = 3
UNION
SELECT source_id FROM user_edge WHERE destination_id = $1 AND source_id = ANY($2::UUID[]) AND state = 3`, userID, others)
	if err != nil {
		return nil, fmt.Errorf("block lookup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("block lookup: %w", err)
		}
		out[id.String()] = true
	}
	return out, rows.Err()
}

func recentlyMetIDs(users []RecentlyMetUser) []string {
	ids := make([]string, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.UserID)
	}
	return ids
}

// readRecentlyMet is the user's list, empty when they have none yet.
func readRecentlyMet(ctx context.Context, nk runtime.NakamaModule, userID string) (*RecentlyMetList, error) {
	list := &RecentlyMetList{}
	if err := StorableRead(ctx, nk, userID, list, false); err != nil {
		if status.Code(err) == codes.NotFound {
			return &RecentlyMetList{}, nil
		}
		return nil, err
	}
	return list, nil
}

// storeRecentlyMet adds `met` to the user's list, retrying once if another write got there first.
func storeRecentlyMet(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, userID string, met []RecentlyMetUser) (int, error) {
	blocked, err := blockedBetween(ctx, db, userID, recentlyMetIDs(met))
	if err != nil {
		return 0, err
	}
	for attempt := 0; ; attempt++ {
		list, err := readRecentlyMet(ctx, nk, userID)
		if err != nil {
			return 0, err
		}
		list.Add(met, userID, blocked, recentlyMetCap)
		err = StorableWrite(ctx, nk, userID, list)
		if err == nil {
			return len(list.Users), nil
		}
		if attempt > 0 || !errors.Is(err, runtime.ErrStorageRejectedVersion) {
			return 0, err
		}
	}
}

// recordRecentlyMet adds who the leaving player met to their list. The participants are read here, on
// the match loop; the storage work runs off it.
func recordRecentlyMet(logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, state *MatchLabel, userID string) {
	met := recentlyMetIn(userID, state.participations, time.Now().UTC())
	if len(met) == 0 {
		return
	}
	matchID := state.ID.String()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		total, err := storeRecentlyMet(ctx, nk, db, userID, met)
		if err != nil {
			logger.WithFields(map[string]any{"user_id": userID, "mid": matchID, "met": len(met), "error": err}).
				Warn("Recently met not recorded")
			return
		}
		logger.WithFields(map[string]any{"user_id": userID, "mid": matchID, "met": len(met), "total": total}).
			Info("Recently met recorded")
	}()
}

// recentlyMetEntries is the list as the viewer's client shows it: each person's name, and while they
// are online their presence text and their party if the viewer may join it (as §1 gives friends);
// online people first, each group newest meeting first.
func (p *EvrPipeline) recentlyMetEntries(ctx context.Context, viewer uuid.UUID, users []RecentlyMetUser) []evr.RecentlyMetEntry {
	online, offline := []evr.RecentlyMetEntry{}, []evr.RecentlyMetEntry{}
	for _, u := range users {
		entry := evr.RecentlyMetEntry{AccountID: u.AccountID, Status: friendStatusCode(false), Name: []byte(u.DisplayName)}
		userID := uuid.FromStringOrNil(u.UserID)
		params := p.userSessionParams(userID)
		if params == nil {
			offline = append(offline, entry)
			continue
		}
		entry.Status = friendStatusCode(true)
		entry.Text = []byte(friendPresenceText(p.userCurrentMatch(ctx, userID)))
		if partyID, joinable := p.friendPartyFor(ctx, viewer, params); joinable {
			entry.PartyID = partyID
			entry.Joinable = 1
		}
		online = append(online, entry)
	}
	return append(online, offline...)
}

// snsRecentlyMetRefreshRequest answers with the user's recently-met list (people they have since
// blocked, or who blocked them, left out).
func (p *EvrPipeline) snsRecentlyMetRefreshRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	userID := session.UserID().String()
	list, err := readRecentlyMet(ctx, p.nk, userID)
	if err != nil {
		logger.Warn("Recently met list unreadable", zap.Error(err))
		list = &RecentlyMetList{}
	}
	blocked, err := blockedBetween(ctx, p.db, userID, recentlyMetIDs(list.Users))
	if err != nil {
		logger.Warn("Recently met block lookup failed", zap.Error(err))
		blocked = map[string]bool{}
	}
	users := make([]RecentlyMetUser, 0, len(list.Users))
	for _, u := range list.Users {
		if !blocked[u.UserID] {
			users = append(users, u)
		}
	}
	entries := p.recentlyMetEntries(ctx, session.UserID(), users)
	onlineCount := 0
	for _, e := range entries {
		if e.Status == friendStatusCode(true) {
			onlineCount++
		}
	}
	logger.Info("Recently met list sent", zap.Int("count", len(entries)), zap.Int("online", onlineCount),
		zap.Int("blocked_dropped", len(list.Users)-len(users)))
	return SendEVRMessages(session, false, &evr.SNSRecentlyMetListResponse{Entries: entries})
}
