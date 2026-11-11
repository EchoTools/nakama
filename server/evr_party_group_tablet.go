package server

import (
	"context"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// tabletPartyGroup is the party group a player is on once they use the tablet's party.
//
// A player is either in a party group (the Discord `/party group` code) or in a tablet party, not
// both. The first tablet party action moves them to this value, and nothing moves them back: leaving
// the tablet party keeps it, and the previous code is never written back. It is a marker, not a group
// anyone joins: lobbyGroupNameFromSettings reads it as "no group", so the tablet rule
// (evr_lobby_tablet.go) is the only thing that groups these players.
//
// The group field is a bias at most, never a hard partition, and this change does not make it more
// load-bearing: it only writes the field. The readers that use it as a key (the find path, /party
// members, getPartyMembersForUser, GetPartyGroupUserIDs) are made to ignore or refuse this value;
// the group index still holds it, because it is stored like any other.
const tabletPartyGroup = "tablet"

// isTabletPartyAction is whether the message is an SNS party request a person makes on the tablet:
// sending an invite, joining, answering an invite, setting the join policy, kicking, passing
// ownership. EvrPipeline.ProcessRequest calls it once for every message, so the group is set at that
// one entry point and not in each handler.
//
// It is not every SNS party request. The client sends Create and DataUpdate by itself (prod log over
// 36 h: 94% of creates and 2,582 of 9,256 data updates arrive within 2 s of login) and Lock and Unlock
// look automatic too, so setting the group on them would put every nEVR player on "tablet" at login.
// Leave only leaves. Update, UpdateMember and InviteListRefresh were never seen. Each of those joins
// the list only once it is shown to be a person's action; evr_party_group_tablet_test.go classifies
// all 14 routed requests and fails on an unclassified one.
func isTabletPartyAction(in evr.Message) bool {
	switch in.(type) {
	case *evr.SNSPartySendInviteRequest,
		*evr.SNSPartyJoinRequest,
		*evr.SNSPartyRespondToInviteRequest,
		*evr.SNSPartySetJoinPolicyRequest,
		*evr.SNSPartyKickRequest,
		*evr.SNSPartyPassOwnershipRequest:
		return true
	}
	return false
}

// applyTabletPartyGroup is the ProcessRequest hook: for a tablet party action by a known user it sets
// the party group. It never fails the action; an error is logged as a Warn and dropped.
func applyTabletPartyGroup(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID uuid.UUID, in evr.Message) {
	if !isTabletPartyAction(in) || userID.IsNil() {
		return
	}
	if err := setPartyGroupTablet(ctx, logger, nk, userID.String()); err != nil {
		logger.Warn("Failed to set party group to tablet", zap.Error(err))
	}
}

// setPartyGroupTablet sets the user's party group to tabletPartyGroup, whatever it was. It writes only
// when the value changes, and logs (Info) only then, with the group it replaced. The rest of the user's
// matchmaking settings are kept as stored.
func setPartyGroupTablet(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string) error {
	settings, err := LoadMatchmakingSettings(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("load matchmaking settings: %w", err)
	}
	if settings.LobbyGroupName == tabletPartyGroup {
		return nil
	}
	previous := settings.LobbyGroupName
	settings.LobbyGroupName = tabletPartyGroup
	if err := StoreMatchmakingSettings(ctx, nk, userID, settings); err != nil {
		return fmt.Errorf("store matchmaking settings: %w", err)
	}
	logger.Info("Party group set to tablet", zap.String("uid", userID), zap.String("previous_group", previous))
	return nil
}

// lobbyGroupNameFromSettings is the party group name a find joins: the stored name, except that the
// tablet marker is no group.
func lobbyGroupNameFromSettings(s MatchmakingSettings) string {
	if s.LobbyGroupName == tabletPartyGroup {
		return ""
	}
	return s.LobbyGroupName
}
