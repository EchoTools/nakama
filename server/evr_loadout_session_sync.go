package server

import (
	"context"
	"database/sql"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// adoptSavedLoadout hands a just-persisted profile to every live login session of
// userID, and re-stores the server profile other players and game servers are served.
//
// It exists because a session's params.profile is not a cache of storage: the lobby
// join path (evr_lobby_joinentrant.go) and the logged-in profile request both build
// the served ServerProfile FROM params.profile and write it back with
// ServerProfileStore. A loadout persisted without updating the session is therefore
// undone on the player's next lobby join -- the session's pre-save copy is rebuilt
// and stored over it, the next game server is handed the old loadout, and the
// player sees their cosmetics revert. The game server save path
// (gameServerSaveLoadoutRequest) never updated the session at all.
//
// Every save path calls this after its write commits, so a save is visible to the
// player's next join immediately, whichever path made it. It never writes the
// EVRProfile itself; a session that is not found is skipped, because the save is
// already in storage and the next login reads it from there.
func adoptSavedLoadout(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, sessionRegistry SessionRegistry, userID string, saved *EVRProfile) {
	if saved == nil {
		return
	}
	presences, err := nk.StreamUserList(StreamModeService, userID, "", StreamLabelLoginService, false, true)
	if err != nil {
		logger.Warn("Failed to list login sessions for saved loadout", zap.String("user_id", userID), zap.Error(err))
		return
	}
	for _, presence := range presences {
		if presence.GetUserId() != userID {
			continue
		}
		session := sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId()))
		if session == nil {
			continue
		}
		params, ok := LoadParams(session.Context())
		if !ok || params == nil {
			continue
		}
		// The session may run on a fallback active group that was deliberately not
		// persisted (see the group auto-assignment in initializeSession); keep it.
		if params.profile != nil {
			saved.SetActiveGroupID(params.profile.GetActiveGroupID())
		}
		params.profile = saved
		StoreParams(session.Context(), params)

		if err := storeSessionServerProfile(ctx, logger, db, nk, userID, params); err != nil {
			logger.Warn("Failed to store server profile for saved loadout", zap.String("user_id", userID), zap.Error(err))
		}
	}
}

// storeSessionServerProfile rebuilds the server profile from the session's
// parameters and stores it for other players and game servers to be served.
func storeSessionServerProfile(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, userID string, params *SessionParameters) error {
	modes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeCombatPublic,
	}
	groupID := params.profile.GetActiveGroupID().String()
	serverProfile, err := UserServerProfileFromParameters(ctx, logger, db, nk, params, groupID, modes, 0)
	if err != nil {
		return err
	}
	if gg, ok := params.guildGroups[groupID]; ok {
		if gg.IsEnforcer(userID) && params.isGoldNameTag.Load() && serverProfile.DeveloperFeatures == nil {
			// Give the user a gold name if they are enabled as a moderator in the guild, and want it.
			serverProfile.DeveloperFeatures = &evr.DeveloperFeatures{}
		}
	}
	return ServerProfileStore(ctx, nk, userID, serverProfile)
}
