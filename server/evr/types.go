package evr

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

type LoginIdentifier interface {
	GetLoginSessionID() uuid.UUID
}

type XPIdentifier interface {
	LoginIdentifier
	GetEvrID() EvrId
}

type LobbySessionMessage interface {
	LoginIdentifier
	LobbyID() uuid.UUID
}

type LobbySessionRequest interface {
	LoginIdentifier
	GetVersionLock() Symbol
	GetAppID() Symbol
	GetGroupID() uuid.UUID
	GetMode() Symbol
	GetLevel() Symbol
	GetFeatures() []string
	GetCurrentLobbyID() uuid.UUID
	GetEntrants() []Entrant
	GetEntrantRole(idx int) int
	GetRegion() Symbol
}
type Entrant struct {
	EvrID EvrId
	Role  int8 // -1 for any team
}

func (e Entrant) String() string {
	return fmt.Sprintf("Entrant(evr_id=%s, role=%d)", e.EvrID, e.Role)
}

type GUID uuid.UUID

// GUID is a wrapper around uuid.UUID to provide custom JSON marshaling and unmarshaling. (i.e. uppercase it)

func (g GUID) String() string {
	return strings.ToUpper(uuid.UUID(g).String())
}

func (g GUID) MarshalJSON() ([]byte, error) {
	return json.Marshal(g.String())
}

func (g *GUID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	u, err := uuid.FromString(s)
	if err != nil {
		return err
	}
	*g = GUID(u)
	return nil
}

func (g GUID) MarshalBytes() []byte {
	b := uuid.UUID(g).Bytes()
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]
	return b
}

func (g *GUID) UnmarshalBytes(b []byte) error {
	if len(b) != 16 {
		return fmt.Errorf("GUID: UUID must be exactly 16 bytes long, got %d bytes", len(b))
	}
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]
	u, err := uuid.FromBytes(b)
	if err != nil {
		return err
	}
	*g = GUID(u)
	return nil
}

/*

SNSActivityDailyListRequest
SNSActivityDailyListResponse
SNSActivityDailyRewardFailure
SNSActivityDailyRewardRequest
SNSActivityDailyRewardSuccess
SNSActivityEventListRequest
SNSActivityEventListResponse
SNSActivityEventRewardFailure
SNSActivityEventRewardRequest
SNSActivityEventRewardSuccess
SNSActivityWeeklyListRequest
SNSActivityWeeklyListResponse
SNSActivityWeeklyRewardFailure
SNSActivityWeeklyRewardRequest
SNSActivityWeeklyRewardSuccess
SNSAddTournament

SNSEarlyQuitConfig
SNSEarlyQuitFeatureFlags
SNSEarlyQuitUpdateNotification

SNSFriendAcceptFailure
SNSFriendAcceptNotify
SNSFriendAcceptRequest
SNSFriendAcceptSuccess
SNSFriendInviteFailure
SNSFriendInviteNotify
SNSFriendInviteRequest
SNSFriendInviteSuccess
SNSFriendListRefreshRequest
SNSFriendListResponse
SNSFriendListSubscribeRequest
SNSFriendListUnsubscribeRequest
SNSFriendRejectNotify
SNSFriendRemoveNotify
SNSFriendRemoveRequest
SNSFriendRemoveResponse
SNSFriendStatusNotify
SNSFriendWithdrawnNotify
SNSGenericMessage
SNSGenericMessageNotify
SNSLeaderboardRequestv2
SNSLeaderboardResponse

SNSLobbyDirectoryJson
SNSLobbyDirectoryRequestJsonv2

SNSLobbyPlayerSessionsFailurev3

SNSLoginRemovedNotify

SNSLogOut
SNSMatchEndedv5
SNSNewUnlocksNotification

SNSPartyCreateFailure
SNSPartyCreateRequest
SNSPartyCreateSuccess
SNSPartyInviteListRefreshRequest
SNSPartyInviteListResponse
SNSPartyInviteNotify
SNSPartyInviteRequest
SNSPartyInviteResponse
SNSPartyJoinFailure
SNSPartyJoinNotify
SNSPartyJoinRequest
SNSPartyJoinSuccess
SNSPartyKickFailure
SNSPartyKickNotify
SNSPartyKickRequest
SNSPartyKickSuccess
SNSPartyLeaveFailure
SNSPartyLeaveNotify
SNSPartyLeaveRequest
SNSPartyLeaveSuccess
SNSPartyLockFailure
SNSPartyLockNotify
SNSPartyLockRequest
SNSPartyLockSuccess
SNSPartyPassFailure
SNSPartyPassNotify
SNSPartyPassRequest
SNSPartyPassSuccess
SNSPartyUnlockFailure
SNSPartyUnlockNotify
SNSPartyUnlockRequest
SNSPartyUnlockSuccess
SNSPartyUpdateFailure
SNSPartyUpdateMemberFailure
SNSPartyUpdateMemberNotify
SNSPartyUpdateMemberRequest
SNSPartyUpdateMemberSuccess
SNSPartyUpdateNotify
SNSPartyUpdateRequest
SNSPartyUpdateSuccess
SNSPurchaseItems
SNSPurchaseItemsResult

SNSRemoveTournament
SNSRewardsSettings
SNSServerSettingsResponsev2
SNSTelemetryEvent
SNSTelemetryNotify

SNSUserServerProfileUpdateFailure

*/
