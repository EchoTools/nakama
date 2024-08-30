package server

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
)

func RestrictAPIFunctionAccess[T any](beforeFn func(fn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (T, error)) error) error {

	noopFn := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (v T, err error) {
		return v, nil
	}

	if err := beforeFn(noopFn); err != nil {
		return err
	}
	return nil
}

func registerGuard(initializer runtime.Initializer) error {
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
		"Rpc",
		"StatusFollow",
		"StatusUnfollow",
		"StatusUpdate",
	}

	RestrictAPIFunctionAccess(initializer.RegisterBeforeAddGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateSteam)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeBanGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeBlockFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeCreateGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteLeaderboardRecord)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteNotifications)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteTournamentRecord)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDemoteGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteLeaderboardRecord)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteNotifications)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteTournamentRecord)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDemoteGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeGetAccount)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeGetSubscription)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeGetUsers)
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
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListChannelMessages)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListGroups)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListLeaderboardRecords)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListLeaderboardRecordsAroundOwner)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListMatches)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListNotifications)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListSubscriptions)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournamentRecords)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournamentRecordsAroundOwner)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournaments)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListUserGroups)
	RestrictAPIFunctionAccess(initializer.RegisterBeforePromoteGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeReadStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeSessionLogout)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeSessionRefresh)
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
	RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteStorageObjects)
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
