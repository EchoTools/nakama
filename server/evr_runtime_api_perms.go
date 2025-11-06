package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// AfterReadStorageObjectsHook is a hook that runs after reading storage objects.
// It checks if the intent includes storage objects access and retries the request as the system user if any objects are missing from the response.
func AfterReadStorageObjectsHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.StorageObjects, in *api.ReadStorageObjectsRequest) error {
	if out == nil || in == nil {
		return nil
	}

	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return err
	}

	type objCompact struct {
		Collection string
		Key        string
		UserID     string
	}
	isAuthoritative := false

	if vars != nil && (vars.Intents.StorageObjects || vars.Intents.IsGlobalOperator) {
		isAuthoritative = true
	}

	if isAuthoritative {
		// If the returned value was empty, retry the request as the system user.
		returnedMap := make(map[objCompact]struct{})
		for _, obj := range out.Objects {
			returnedMap[objCompact{
				Collection: obj.Collection,
				Key:        obj.Key,
				UserID:     obj.UserId,
			}] = struct{}{}
		}

		// Find any missing objects in the response.
		ops := make([]*runtime.StorageRead, 0)
		for _, obj := range in.ObjectIds {
			if _, exists := returnedMap[objCompact{
				Collection: obj.Collection,
				Key:        obj.Key,
				UserID:     obj.UserId,
			}]; !exists {
				ops = append(ops, &runtime.StorageRead{
					Collection: obj.Collection,
					Key:        obj.Key,
					UserID:     obj.UserId,
				})
			}
		}
		if len(ops) > 0 {
			// retry as the system user
			objs, err := nk.StorageRead(ctx, ops)
			if err != nil {
				logger.Error("Failed to read storage objects as system user", zap.Error(err))
				return err
			}
			for _, obj := range objs {
				out.Objects = append(out.Objects, &api.StorageObject{
					Collection:      obj.Collection,
					Key:             obj.Key,
					UserId:          obj.UserId,
					Value:           obj.Value,
					Version:         obj.Version,
					PermissionRead:  obj.PermissionRead,
					PermissionWrite: obj.PermissionWrite,
				})
			}
		}
	}

	return nil
}

// BeforeWriteStorageObjectsHook is a hook that runs before writing storage objects.
// It checks if the intent includes storage objects access and blocks the request if not authorized.
func BeforeWriteStorageObjectsHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.WriteStorageObjectsRequest) (*api.WriteStorageObjectsRequest, error) {
	if in == nil {
		return nil, nil
	}

	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return nil, err
	}

	isAuthoritative := false

	if vars != nil && (vars.Intents.StorageObjects || vars.Intents.IsGlobalOperator) {
		isAuthoritative = true
	}

	if !isAuthoritative {
		// Block the request by returning nil
		return nil, nil
	}

	return in, nil
}

// BeforeDeleteStorageObjectsHook is a hook that runs before deleting storage objects.
// It checks if the intent includes storage objects access and blocks the request if not authorized.
func BeforeDeleteStorageObjectsHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.DeleteStorageObjectsRequest) (*api.DeleteStorageObjectsRequest, error) {
	if in == nil {
		return nil, nil
	}

	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return nil, err
	}

	isAuthoritative := false

	if vars != nil && (vars.Intents.StorageObjects || vars.Intents.IsGlobalOperator) {
		isAuthoritative = true
	}

	if !isAuthoritative {
		// Block the request by returning nil
		return nil, nil
	}

	return in, nil
}

// BeforeListMatchesHook is a hook that runs before listing matches.
func BeforeListMatchesHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListMatchesRequest) (*api.ListMatchesRequest, error) {
	if in == nil {
		return nil, nil
	}

	// TODO: Probably better to just restrict the matches that are returned.
	in.Limit = wrapperspb.Int32(min(in.GetLimit().GetValue(), 100)) // Enforce a maximum limit of 100 matches.
	// Set the default values for the request.
	in.Label = nil                           // Clear the label to prevent any filtering by label.
	in.Authoritative = wrapperspb.Bool(true) // Set authoritative to false to allow listing all matches.

	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return nil, err
	}

	// Append restrictions to the query
	var query string
	if in.GetQuery() != nil {
		query = in.GetQuery().GetValue()
	}

	if !vars.Intents.Matches {
		// No limits
	} else if vars.Intents.GuildMatches {
		// Limit to guild matches only (including private matches).
		// TODO
	} else {
		// Limit to public matches only.
		query = query + ` +label.mode:public`
	}

	in.Query = &wrapperspb.StringValue{Value: query}
	// Otherwise, we return an empty response.
	return in, nil
}

func RestrictAPIFunctionAccess[T any](beforeFn func(fn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (T, error)) error) error {

	noopFn := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (v T, err error) {
		return v, nil
	}

	if err := beforeFn(noopFn); err != nil {
		return err
	}
	return nil
}

func registerAPIGuards(initializer runtime.Initializer) error {
	rtMessages := []string{
		"ChannelJoin",
		"ChannelLeave",
		"ChannelMessageSend",
		"ChannelMessageUpdate",
		"ChannelMessageRemove",
		"MatchCreate",
		"MatchDataSend",
		"MatchJoin",
		"MatchLeave",
		"MatchmakerAdd",
		"MatchmakerRemove",
		"PartyCreate",
		"PartyCreate",
		"PartyJoin",
		"PartyLeave",
		"PartyPromote",
		"PartyAccept",
		"PartyRemove",
		"PartyClose",
		"PartyJoinRequestList",
		"PartyMatchmakerAdd",
		"PartyMatchmakerRemove",
		"PartyDataSend",
		//"Ping",
		//"Pong",
		//"Rpc",
		"StatusFollow",
		"StatusUnfollow",
		"StatusUpdate",
	}

	RestrictAPIFunctionAccess(initializer.RegisterBeforeAddGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateApple)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateSteam)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeBanGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeBlockFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeCreateGroup)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteLeaderboardRecord)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteNotifications)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteTournamentRecord)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDemoteGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDemoteGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeGetSubscription)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeGetUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeImportFacebookFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeImportSteamFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeJoinGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeJoinTournament)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeKickGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLeaveGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkSteam)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListChannelMessages)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListFriends)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListGroups)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListLeaderboardRecords)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListLeaderboardRecordsAroundOwner)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListMatches)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListNotifications)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListStorageObjects)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListSubscriptions)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournamentRecords)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournamentRecordsAroundOwner)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournaments)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListUserGroups)
	RestrictAPIFunctionAccess(initializer.RegisterBeforePromoteGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeReadStorageObjects)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeSessionLogout)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeSessionRefresh)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkSteam)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUpdateAccount)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUpdateGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseFacebookInstant)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseHuawei)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidateSubscriptionApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidateSubscriptionGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteLeaderboardRecord)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteTournamentRecord)

	for _, rtMessage := range rtMessages {
		if err := initializer.RegisterBeforeRt(rtMessage, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, envelope *rtapi.Envelope) (*rtapi.Envelope, error) {
			return nil, nil
		}); err != nil {
			return err
		}
	}

	return nil
}
