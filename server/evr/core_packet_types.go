package evr

import (
	"fmt"

	"github.com/echotools/nevr-common/rtapi"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type NEVRProtobufJSONMessageV1 struct {
	Payload []byte
}

func (NEVRProtobufJSONMessageV1) String() string {
	return "NEVRProtobufJSONMessageV1"
}

func (m *NEVRProtobufJSONMessageV1) Stream(s *EasyStream) error {
	if s.Mode == DecodeMode {
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
	case 0x6d4de3650ee3110e:
		return &LobbySessionSuccessv4{}
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
	case 0x9ee5107d9e29fd63:
		return &NEVRProtobufMessageV1{}
	case 0xc6b3710cd9c4ef47:
		return &NEVRProtobufJSONMessageV1{}
	default:
		return nil
	}
}
