package evr

import (
	"fmt"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const MaxProtobufPayloadSize = MaxMessageLength // protobuf payload can't exceed a single message

type NEVRProtobufJSONMessageV1 struct {
	Payload []byte
}

func (NEVRProtobufJSONMessageV1) String() string {
	return "NEVRProtobufJSONMessageV1"
}

func (m *NEVRProtobufJSONMessageV1) Stream(s *EasyStream) error {
	if s.Mode == DecodeMode {
		if s.Len() > MaxProtobufPayloadSize {
			return fmt.Errorf("payload size %d exceeds maximum %d", s.Len(), MaxProtobufPayloadSize)
		}
		m.Payload = make([]byte, s.Len())
	}
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamBytes(&m.Payload, len(m.Payload)) },
	})
}

func NewNEVRProtobufJSONMessageV1(envelope *rtapi.Envelope) (*NEVRProtobufJSONMessageV1, error) {
	data, err := protojson.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GameServerEntrantAcceptMessage: %w", err)
	}
	return &NEVRProtobufJSONMessageV1{
		Payload: data,
	}, nil
}

type NEVRProtobufMessageV1 struct {
	Payload []byte
}

func (NEVRProtobufMessageV1) String() string {
	return "NEVRProtobufMessageV1"
}

func (m *NEVRProtobufMessageV1) Stream(s *EasyStream) error {
	if s.Mode == DecodeMode {
		if s.Len() > MaxProtobufPayloadSize {
			return fmt.Errorf("payload size %d exceeds maximum %d", s.Len(), MaxProtobufPayloadSize)
		}
		m.Payload = make([]byte, s.Len())
	}
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamBytes(&m.Payload, len(m.Payload)) },
	})
}

func NewNEVRProtobufMessageV1(envelope *rtapi.Envelope) (*NEVRProtobufMessageV1, error) {
	data, err := proto.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal GameServerEntrantAcceptMessage: %w", err)
	}
	return &NEVRProtobufMessageV1{
		Payload: data,
	}, nil
}

func NewMessageFromHash(hash uint64) Message {

	switch hash {
	case 0x0dabc24265508a82:
		return &ReconcileIAPResult{}
	case 0x1225133828150da3:
		return &OtherUserProfileFailure{}
	case 0x1230073227050cb5:
		return &OtherUserProfileSuccess{}
	case 0x1231172031050cb2:
		return &OtherUserProfileRequest{}
	case 0x128b777ae0ebb650:
		return &LobbyMatchmakerStatusRequest{}
	case 0x1bd0fc454c85573c:
		return &ReconcileIAP{}
	case 0x244b47685187eae1:
		return &RemoteLogSet{}
	case 0x2f03468f77ffb211:
		return &LobbyJoinSessionRequest{}
	case 0x312c2a01819aa3f5:
		return &LobbyFindSessionRequest{}
	case 0x43e6963ac76beee4:
		return &STcpConnectionUnrequireEvent{}
	case 0xb99f11d6ea5cb1f1:
		return &LobbySessionFailurev1{}
	case 0x4ae8365ebc45f96a:
		return &LobbySessionFailurev2{}
	case 0x4ae8365ebc45f96b:
		return &LobbySessionFailurev3{}
	case 0x4ae8365ebc45f96c:
		return &LobbySessionFailurev4{}
	case 0x599a6b1bbda3cc13:
		return &LobbyCreateSessionRequest{}
	case 0xfabf5f8719bfebf3:
		return &LobbyPingRequest{}
	case 0x6047d0043033ae4f:
		return &LobbyPingResponse{}
	case 0x6c8f16cd9f8964c5:
		return &ChannelInfoResponse{}
	case 0x6d4de3650ee3110f:
		return &LobbySessionSuccessv5{}
	case 0x6d54a19a3ff24415:
		return &UpdateClientProfile{}
	case 0x7777777777770000:
		return &GameServerSessionStart{}
	case 0x7777777777770200:
		return &BroadcasterSessionEnded{}
	case 0x7777777777770300:
		return &BroadcasterPlayerSessionsLocked{}
	case 0x7777777777770400:
		return &BroadcasterPlayerSessionsUnlocked{}
	case 0x7777777777770500:
		return &GameServerJoinAttempt{}
	case 0x7777777777770600:
		return &GameServerJoinAllowed{}
	case 0x7777777777770700:
		return &GameServerJoinRejected{}
	case 0x7777777777770800:
		return &GameServerPlayerRemoved{}
	case 0x7777777777770B00:
		return &GameServerSaveLoadoutRequest{} // Custom - loadout update from game server
	case 0x7777777777777777:
		return &BroadcasterRegistrationRequest{}
	case 0x82869f0b37eb4378:
		return &ConfigRequest{}
	case 0xb9cdaf586f7bd012:
		return &ConfigSuccess{}
	case 0x9e687a63dddd3870:
		return &ConfigFailure{}
	case 0x8d5ad3c4f2166c6c:
		return &FindServerRegionInfo{}
	case 0x8da9eb83ffee9fd6:
		return &LobbyPendingSessionCancel{}
	case 0x8f28cf33dabfbecb:
		return &LobbyMatchmakerStatus{}
	case 0x90758e58515724e0:
		return &ChannelInfoRequest{}
	case 0x9af2fab2a0c81a05:
		return &LobbyPlayerSessionsRequest{}
	case 0xa1b9cae1f8588968:
		return &LobbyEntrantsV2{}
	case 0xa1b9cae1f8588969:
		return &LobbyEntrantsV3{}
	case 0xbdb41ea9e67b200a:
		return &LoginRequest{}
	case 0xa5acc1a90d0cce47:
		return &LoginSuccess{}
	case 0xa5b9d5a3021ccf51:
		return &LoginFailure{}
	case 0xb56f25c7dfe6ffc9:
		return &BroadcasterRegistrationFailure{}
	case 0xb57a31cdd0f6fedf:
		return &BroadcasterRegistrationSuccess{}
	case 0xd06ae97220a7b41f:
		return &DocumentFailure{}
	case 0xd07ffd782fb7b509:
		return &DocumentSuccess{}
	case 0xd2986849b36b9c72:
		return &UserServerProfileUpdateRequest{}
	case 0xd299785ba56b9c75:
		return &UserServerProfileUpdateSuccess{}
	case 0xe4b9b1cab57e8988:
		return &LobbyStatusNotify{}
	case 0xed5be2c3632155f1:
		return &GameSettings{}
	case 0xf24185da0edef641:
		return &UpdateProfileFailure{}
	case 0xf25491d001cef757:
		return &UpdateProfileSuccess{}
	case 0xfb632e5a38ec8c61:
		return &LoggedInUserProfileFailure{}
	case 0xfb763a5037fc8d77:
		return &LoggedInUserProfileSuccess{}
	case 0xfb772a4221fc8d70:
		return &LoggedInUserProfileRequest{}
	case 0xfcced6f169822bb8:
		return &DocumentRequest{}
	case 0xff71856af7e0fbd9:
		return &LobbyEntrantsV0{}
	case 0x080495a43a6b7251:
		return &SNSEarlyQuitConfig{}
	case 0x1f81b54c35788eaa:
		return &SNSEarlyQuitUpdateNotification{}
	case 0xd9a955895caccac3:
		return &SNSEarlyQuitFeatureFlags{}
	case 0x9ee5107d9e29fd63:
		return &NEVRProtobufMessageV1{}
	case 0xc6b3710cd9c4ef47:
		return &NEVRProtobufJSONMessageV1{}
	case 0xa0687d9799640878:
		return &SNSLobbySetSpawnBotOnServer{}
	// SNS Party messages — client requests
	case 0xb57b22cc5352e00c:
		return &SNSPartyJoinRequest{}
	case 0xb77b0be7a94a9fb6:
		return &SNSPartyLeaveRequest{}
	case 0xcf13f934540b5f5e:
		return &SNSPartySendInviteRequest{}
	case 0xc2478aa479f3e16a:
		return &SNSPartyLockRequest{}
	case 0x5a4e99802fa3d704:
		return &SNSPartyUnlockRequest{}
	case 0xfaf57beb59917d64:
		return &SNSPartyKickRequest{}
	case 0x518543cd886a6946:
		return &SNSPartyPassOwnershipRequest{}
	case 0xe3654a09203555a3:
		return &SNSPartyRespondToInviteRequest{}
	// SNS Party messages — server responses/notifications
	case 0xb57a32de4552e00b:
		return &SNSPartyJoinSuccess{}
	case 0xb56f26d44a42e11d:
		return &SNSPartyJoinFailure{}
	case 0xb77a1bf5bf4a9fb1:
		return &SNSPartyLeaveSuccess{}
	case 0x05315abefc8f804b:
		return &SNSPartyLeaveNotify{}
	case 0x28cb04891f93dc81:
		return &SNSPartyKickNotify{}
	case 0x9d946c88d5a8aca5:
		return &SNSPartyPassNotify{}
	case 0x93a6b1a6cd4ef8dd:
		return &SNSPartyLockNotify{}
	case 0xd8cfd3795010481f:
		return &SNSPartyUnlockNotify{}
	// SNS Party messages — additional requests
	case 0x0b7bd21332523994:
		return &SNSPartyCreateRequest{}
	case 0xdee761a021a5278a:
		return &SNSPartyUpdateRequest{}
	case 0x4edeeb8ddecc8736:
		return &SNSPartyUpdateMemberRequest{}
	case 0xd8cbc44959e25da8:
		return &SNSPartyInviteListRefreshRequest{}
	// SNS Party messages — additional responses/notifications
	case 0x0b7ac20124523993:
		return &SNSPartyCreateSuccess{}
	case 0x0b6fd60b2b423885:
		return &SNSPartyCreateFailure{}
	case 0xcc38103e64879e53:
		return &SNSPartyJoinNotify{}
	case 0xb76f0fffb05a9ea7:
		return &SNSPartyLeaveFailure{}
	case 0xfaf46bf94f917d63:
		return &SNSPartyKickSuccess{}
	case 0xfae17ff340817c75:
		return &SNSPartyKickFailure{}
	case 0x518453df9e6a6941:
		return &SNSPartyPassSuccess{}
	case 0x519147d5917a6857:
		return &SNSPartyPassFailure{}
	case 0xc2469ab66ff3e16d:
		return &SNSPartyLockSuccess{}
	case 0xc2538ebc60e3e07b:
		return &SNSPartyLockFailure{}
	case 0x5a4f899239a3d703:
		return &SNSPartyUnlockSuccess{}
	case 0x5a5a9d9836b3d615:
		return &SNSPartyUnlockFailure{}
	case 0x218f721f09026dab:
		return &SNSPartyInviteNotify{}
	case 0x685a5fb8447b1155:
		return &SNSPartyInviteListResponse{}
	case 0xdee671b237a5278d:
		return &SNSPartyUpdateSuccess{}
	case 0xdef365b838b5269b:
		return &SNSPartyUpdateFailure{}
	case 0x23c834cb3bc6ecf5:
		return &SNSPartyUpdateNotify{}
	case 0x4edffb9fc8cc8731:
		return &SNSPartyUpdateMemberSuccess{}
	case 0x4ecaef95c7dc8627:
		return &SNSPartyUpdateMemberFailure{}
	case 0x451eb6ca40dde289:
		return &SNSPartyUpdateMemberNotify{}
	// SNS Friends messages — client requests
	case 0xcdc02fd1dbee3aaa:
		return &SNSFriendListSubscribeRequest{}
	case 0xdcfa94680e8d19fc:
		return &SNSFriendListRefreshRequest{}
	case 0x7f0d7a28de3c6f70:
		return &SNSFriendInviteRequest{}
	case 0x1bbcb7e810af4620:
		return &SNSFriendAcceptRequest{}
	case 0x78908988b7fe6db4:
		return &SNSFriendRemoveRequest{}
	// SNS Friends messages — server responses/notifications
	case 0xa78aeb2a4e89b10b:
		return &SNSFriendListResponse{}
	case 0x26a19dc4d2d5579d:
		return &SNSFriendStatusNotify{}
	case 0x7f0c6a3ac83c6f77:
		return &SNSFriendInviteSuccess{}
	case 0x7f197e30c72c6e61:
		return &SNSFriendInviteFailure{}
	case 0xca09b0b36bd981b7:
		return &SNSFriendInviteNotify{}
	case 0x1bbda7fa06af4627:
		return &SNSFriendAcceptSuccess{}
	case 0x1ba8b3f009bf4731:
		return &SNSFriendAcceptFailure{}
	case 0xc237c84c31d3ae05:
		return &SNSFriendAcceptNotify{}
	case 0xc2bf83a08ea3a955:
		return &SNSFriendRemoveResponse{}
	case 0xe06972f49cd72265:
		return &SNSFriendRemoveNotify{}
	case 0x191aa30801ec6d03:
		return &SNSFriendWithdrawnNotify{}
	case 0xb9b86c0ce8e8d0c1:
		return &SNSFriendRejectNotify{}
	default:
		return nil
	}
}
