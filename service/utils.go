package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/jackc/pgx/v5/pgtype"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "google.golang.org/protobuf/proto"
)

func GetUserIDByDiscordID(ctx context.Context, db *sql.DB, customID string) (userID, username string, err error) {

	// Look for an existing account.
	query := "SELECT id, username, disable_time FROM users WHERE custom_id = $1"
	var dbUserID string
	var dbUsername string
	var dbDisableTime pgtype.Timestamptz
	var found = true
	err = db.QueryRowContext(ctx, query, customID).Scan(&dbUserID, &dbUsername, &dbDisableTime)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", "", fmt.Errorf("error finding user by discord ID: %w", err)
		}
	}
	if !found {
		return "", "", server.ErrAccountNotFound
	}

	return dbUserID, dbUsername, nil
}

func GetGroupIDByGuildID(ctx context.Context, db *sql.DB, guildID string) (groupID string, err error) {
	// Look for an existing account.
	query := "SELECT id FROM groups WHERE lang_tag = 'guild' AND metadata->>'guild_id' = $1"
	var dbGroupID string
	var found = true
	if err = db.QueryRowContext(ctx, query, guildID).Scan(&dbGroupID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return uuid.Nil.String(), fmt.Errorf("error finding guild ID: %w", err)
		}
	}
	if !found {
		return uuid.Nil.String(), status.Error(codes.NotFound, "guild ID not found")
	}

	return dbGroupID, nil
}

func GetDiscordIDByUserID(ctx context.Context, db *sql.DB, userID string) (discordID string, err error) {
	// Look for an existing account.
	query := "SELECT custom_id FROM users WHERE id = $1"
	var dbCustomID sql.NullString
	var found = true
	if err = db.QueryRowContext(ctx, query, userID).Scan(&dbCustomID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", fmt.Errorf("error finding discord ID: %w", err)
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "discord ID not found")
	}

	return dbCustomID.String, nil
}

func GetGuildIDByGroupID(ctx context.Context, db *sql.DB, groupID string) (guildID string, err error) {
	// Look for an existing account.
	query := "SELECT metadata->>'guild_id' FROM groups WHERE id = $1"
	var dbGuildID string
	var found = true
	if err = db.QueryRowContext(ctx, query, groupID).Scan(&dbGuildID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", fmt.Errorf("error finding guild ID: %w", err)
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "guild ID not found")
	}

	return dbGuildID, nil
}

func MatchLabelByID(ctx context.Context, nk runtime.NakamaModule, matchID MatchID) (*MatchLabel, error) {
	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil {
		return nil, err
	} else if match == nil {
		return nil, ErrMatchNotFound
	}

	label := MatchLabel{}
	if err = json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return nil, err
	}
	if label.GroupID == nil {
		label.GroupID = &uuid.Nil
	}

	return &label, nil
}

func PartyMemberList(ctx context.Context, nk runtime.NakamaModule, partyID uuid.UUID) ([]runtime.Presence, error) {
	node, ok := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)
	if !ok {
		return nil, status.Error(codes.Internal, "error getting node from context")
	}
	// Get the MatchIDs for the user from it's presence
	presences, err := nk.StreamUserList(server.StreamModeParty, partyID.String(), "", node, true, true)
	if err != nil {
		return nil, err
	}
	return presences, nil
}

func CheckSystemGroupMembership(ctx context.Context, db *sql.DB, userID, groupName string) (bool, error) {
	return CheckGroupMembershipByName(ctx, db, userID, groupName, SystemGroupLangTag)
}

func CheckGroupMembershipByName(ctx context.Context, db *sql.DB, userID, groupName, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.name = $2 
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= 2;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupName)
	params = append(params, userID)
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	return true, nil
}

func CheckGroupMembershipByID(ctx context.Context, db *sql.DB, userID, groupID, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.id = $2
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= $4;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupID)
	params = append(params, userID)
	params = append(params, int32(api.UserGroupList_UserGroup_MEMBER))

	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	return true, nil
}

func PresenceByEntrantID(nk runtime.NakamaModule, matchID MatchID, entrantID uuid.UUID) (presence *LobbyPresence, err error) {

	presences, err := nk.StreamUserList(StreamModeEntrant, entrantID.String(), "", matchID.Node, false, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream presences for entrant %s: %w", entrantID.String(), err)
	}

	if len(presences) == 0 {
		return nil, ErrEntrantNotFound
	}

	if len(presences) > 1 {
		return nil, ErrMultipleEntrantsFound
	}

	mp := &LobbyPresence{}
	if err := json.Unmarshal([]byte(presences[0].GetStatus()), mp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal presence: %w", err)
	}

	return mp, nil
}

func GetMatchIDBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (matchID MatchID, presence runtime.Presence, err error) {

	presences, err := nk.StreamUserList(StreamModeService, sessionID.String(), "", "", false, true)
	if err != nil {
		return MatchID{}, nil, fmt.Errorf("failed to get stream presences: %w", err)
	}
	if len(presences) == 0 {
		return MatchID{}, nil, ErrMatchNotFound
	}
	presence = presences[0]
	matchID = MatchIDFromStringOrNil(presences[0].GetStatus())
	if !matchID.IsNil() {
		// Verify that the user is actually in the match
		if meta, err := nk.StreamUserGet(server.StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil || meta == nil {
			return MatchID{}, nil, ErrMatchNotFound
		}
		return matchID, presence, nil
	}

	return MatchID{}, nil, ErrMatchNotFound
}

func GetLobbyGroupID(ctx context.Context, db *sql.DB, userID string) (string, uuid.UUID, error) {
	query := "SELECT value->>'group_id' FROM storage WHERE collection = $1 AND key = $2 and user_id = $3"
	var dbPartyGroupName string
	var found = true
	err := db.QueryRowContext(ctx, query, MatchmakerStorageCollection, MatchmakingConfigStorageKey, userID).Scan(&dbPartyGroupName)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", uuid.Nil, fmt.Errorf("error finding lobby group id: %w", err)
		}
	}
	if !found {
		return "", uuid.Nil, status.Error(codes.NotFound, "lobby group id not found")
	}
	if dbPartyGroupName == "" {
		return "", uuid.Nil, nil
	}
	return dbPartyGroupName, uuid.NewV5(EntrantIDSalt, dbPartyGroupName), nil
}

// returns map[guildID]groupID
func GetGuildGroupIDsByUser(ctx context.Context, db *sql.DB, userID string) (map[string]string, error) {
	if userID == "" {
		return nil, status.Error(codes.InvalidArgument, "user ID is required")
	}
	query := `
	SELECT g.id, g.metadata->>'guild_id' 
	FROM group_edge ge, groups g 
	WHERE g.id = ge.source_id AND ge.destination_id = $1 AND g.lang_tag = 'guild' AND ge.state <= 2
						  `

	var dbGroupID string
	var dbGuildID string
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error finding guild groups: %w", err)
	}

	groups := make(map[string]string, 0)

	for rows.Next() {
		if err := rows.Scan(&dbGroupID, &dbGuildID); err != nil {
			return nil, err
		}
		groups[dbGuildID] = dbGroupID
	}
	_ = rows.Close()
	return groups, nil
}

func KickPlayerFromMatch(ctx context.Context, nk runtime.NakamaModule, matchID MatchID, userID string) error {
	// Get the user's presences

	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	presences, err := nk.StreamUserList(server.StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, false, true)
	if err != nil {
		return fmt.Errorf("failed to get stream presences: %w", err)
	}

	for _, presence := range presences {
		if presence.GetUserId() != userID {
			continue
		}
		if presence.GetSessionId() == label.GameServer.SessionID.String() {
			// Do not kick the game server
			continue
		}

		signal := SignalKickEntrantsPayload{
			UserIDs: []uuid.UUID{uuid.FromStringOrNil(userID)},
		}

		data := NewSignalEnvelope(userID, SignalKickEntrants, signal).String()

		// Signal the match to kick the entrants
		if _, err := nk.MatchSignal(ctx, matchID.String(), data); err != nil {
			return fmt.Errorf("failed to signal match: %w", err)
		}
	}

	return nil
}

func DisconnectUserID(ctx context.Context, nk runtime.NakamaModule, userID string, kickFirst bool, includeLogin bool, includeGameserver bool) (int, error) {

	if kickFirst {
		// Kick the user from any matches they are in
		if matchID, _, err := GetMatchIDBySessionID(nk, uuid.FromStringOrNil(userID)); err == nil && !matchID.IsNil() {
			if err := KickPlayerFromMatch(ctx, nk, matchID, userID); err != nil {
				return 0, fmt.Errorf("failed to kick player from match: %w", err)
			}
		}
	}

	presences, err := nk.StreamUserList(StreamModeService, userID, "", "", false, true)
	if err != nil {
		return 0, fmt.Errorf("failed to get stream presences: %w", err)
	}
	cnt := 0
	for _, presence := range presences {

		// Add a delay to allow the match to process the kick
		go func() {
			if kickFirst {
				<-time.After(5 * time.Second)
			}
			if err := nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect); err != nil {
				// Ignore the error
				return
			}
		}()

		cnt++
	}

	return cnt, nil
}

func GetUserIDByDeviceID(ctx context.Context, db *sql.DB, deviceID string) (string, error) {
	query := `
	SELECT ud.user_id FROM user_device ud WHERE ud.id = $1`
	var dbUserID string
	var found = true
	err := db.QueryRowContext(ctx, query, deviceID).Scan(&dbUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", fmt.Errorf("error finding user ID By Evr ID: %w", err)
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "user account not found")
	}
	if dbUserID == "" {
		return "", nil
	}
	return dbUserID, nil
}

func GetPartyGroupUserIDs(ctx context.Context, nk runtime.NakamaModule, groupName string) ([]string, error) {
	if groupName == "" {
		return nil, status.Error(codes.InvalidArgument, "user ID is required")
	}

	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, ActivePartyGroupIndex, fmt.Sprintf("+value.group_id:%s", groupName), 100, nil, "")
	if err != nil {
		return nil, fmt.Errorf("error listing party group users: %w", err)
	}

	if len(objs.Objects) == 0 {
		return nil, status.Error(codes.NotFound, "party group not found")
	}

	userIDs := make([]string, 0, len(objs.Objects))

	for _, obj := range objs.Objects {
		if obj.GetUserId() == SystemUserID {
			continue
		}
		userIDs = append(userIDs, obj.GetUserId())
	}

	return userIDs, nil
}

func SetNextMatchID(ctx context.Context, nk runtime.NakamaModule, userID string, matchID MatchID, role RoleIndex, hostDiscordID string) error {
	settings, err := LoadMatchmakingSettings(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("Error loading matchmaking settings: %w", err)
	}

	settings.NextMatchID = matchID
	settings.NextMatchRole = role.String()
	settings.NextMatchDiscordID = hostDiscordID

	if err = StoreMatchmakingSettings(ctx, nk, userID, settings); err != nil {
		return fmt.Errorf("Error storing matchmaking settings: %w", err)
	}

	return nil
}
