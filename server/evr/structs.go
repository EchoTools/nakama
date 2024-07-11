package evr

import (
	"encoding/binary"

	"github.com/go-restruct/restruct"
)

type Packet struct {
	Chunks []Chunk `struct-while:"!_eof"`
}

type Chunk struct {
	Magic [8]byte
	Type  uint64
	Len   uint64
	Data  struct {
		GenericMessage                     *GenericMessage                    `struct-case:"0x013e99cb47eb3669" json:",omitempty"`
		MatchEnded                         *MatchEnded                        `struct-case:"0x80119c19ac72d695" json:",omitempty"`
		ReconcileIAPResult                 *ReconcileIAPResult                `struct-case:"0x0dabc24265508a82" json:",omitempty"`
		OtherUserProfileFailure            *OtherUserProfileFailure           `struct-case:"0x1225133828150da3" json:",omitempty"`
		OtherUserProfileSuccess            *OtherUserProfileSuccess           `struct-case:"0x1230073227050cb5" json:",omitempty"`
		OtherUserProfileRequest            *OtherUserProfileRequest           `struct-case:"0x1231172031050cb2" json:",omitempty"`
		LobbyMatchmakerStatusRequest       *LobbyMatchmakerStatusRequest      `struct-case:"0x128b777ae0ebb650" json:",omitempty"`
		ReconcileIAP                       *ReconcileIAP                      `struct-case:"0x1bd0fc454c85573c" json:",omitempty"`
		RemoteLogSet                       *RemoteLogSet                      `struct-case:"0x244b47685187eae1" json:",omitempty"`
		LobbyJoinSessionRequest            *LobbyJoinSessionRequest           `struct-case:"0x2f03468f77ffb211" json:",omitempty"`
		LobbyFindSessionRequest            *LobbyFindSessionRequest           `struct-case:"0x312c2a01819aa3f5" json:",omitempty"`
		STcpConnectionUnrequireEvent       *STcpConnectionUnrequireEvent      `struct-case:"0x43e6963ac76beee4" json:",omitempty"`
		LobbySessionFailurev1              *LobbySessionFailurev1             `struct-case:"0xb99f11d6ea5cb1f1" json:",omitempty"`
		LobbySessionFailurev2              *LobbySessionFailurev2             `struct-case:"0x4ae8365ebc45f96a" json:",omitempty"`
		LobbySessionFailurev3              *LobbySessionFailurev3             `struct-case:"0x4ae8365ebc45f96b" json:",omitempty"`
		LobbySessionFailurev4              *LobbySessionFailurev4             `struct-case:"0x4ae8365ebc45f96c" json:",omitempty"`
		LobbyCreateSessionRequest          *LobbyCreateSessionRequest         `struct-case:"0x599a6b1bbda3cc13" json:",omitempty"`
		LobbyPingResponse                  *LobbyPingResponse                 `struct-case:"0x6047d0043033ae4f" json:",omitempty"`
		ChannelInfoResponse                *ChannelInfoResponse               `struct-case:"0x6c8f16cd9f8964c5" json:",omitempty"`
		LobbySessionSuccessv4              *LobbySessionSuccessv4             `struct-case:"0x6d4de3650ee3110e" json:",omitempty"`
		LobbySessionSuccessv5              *LobbySessionSuccessv5             `struct-case:"0x6d4de3650ee3110f" json:",omitempty"`
		UpdateProfile                      *UpdateClientProfile               `struct-case:"0x6d54a19a3ff24415" json:",omitempty"`
		BroadcasterStartSession            *BroadcasterStartSession           `struct-case:"0x7777777777770000" json:",omitempty"`
		GameServerSessionStarted           *BroadcasterSessionStarted         `struct-case:"0x7777777777770100" json:",omitempty"`
		BroadcasterSessionEnded            *BroadcasterSessionEnded           `struct-case:"0x7777777777770200" json:",omitempty"`
		GameServerPlayerSessionsLocked     *BroadcasterPlayerSessionsLocked   `struct-case:"0x7777777777770300" json:",omitempty"`
		ERGameServerPlayerSessionsUnlocked *BroadcasterPlayerSessionsUnlocked `struct-case:"0x7777777777770400" json:",omitempty"`
		BroadcasterPlayersAccept           *BroadcasterPlayersAccept          `struct-case:"0x7777777777770500" json:",omitempty"`
		BroadcasterPlayersAccepted         *BroadcasterPlayersAccepted        `struct-case:"0x7777777777770600" json:",omitempty"`
		BroadcasterPlayersRejected         *BroadcasterPlayersRejected        `struct-case:"0x7777777777770700" json:",omitempty"`
		BroadcasterPlayerRemoved           *BroadcasterPlayerRemoved          `struct-case:"0x7777777777770800" json:",omitempty"`
		GameServerChallengeRequest         *BroadcasterChallengeRequest       `struct-case:"0x7777777777770900" json:",omitempty"`
		GameServerChallengeResponse        *GameServerChallengeResponse       `struct-case:"0x7777777777770a00" json:",omitempty"`
		BroadcasterRegistrationRequest     *BroadcasterRegistrationRequest    `struct-case:"0x7777777777777777" json:",omitempty"`
		ConfigRequest                      *ConfigRequest                     `struct-case:"0x82869f0b37eb4378" json:",omitempty"`
		ConfigSuccess                      *ConfigSuccess                     `struct-case:"0xb9cdaf586f7bd012" json:",omitempty"`
		ConfigFailure                      *ConfigFailure                     `struct-case:"0x9e687a63dddd3870" json:",omitempty"`
		FindServerRegionInfo               *FindServerRegionInfo              `struct-case:"0x8d5ad3c4f2166c6c" json:",omitempty"`
		LobbyPendingSessionCancel          *LobbyPendingSessionCancel         `struct-case:"0x8da9eb83ffee9fd6" json:",omitempty"`
		LobbyMatchmakerStatus              *LobbyMatchmakerStatus             `struct-case:"0x8f28cf33dabfbecb" json:",omitempty"`
		ChannelInfoRequest                 *ChannelInfoRequest                `struct-case:"0x90758e58515724e0" json:",omitempty"`
		LobbyPlayerSessionsRequest         *LobbyPlayerSessionsRequest        `struct-case:"0x9af2fab2a0c81a05" json:",omitempty"`
		LobbyPlayerSessionsSuccessv2       *LobbyPlayerSessionsSuccessv2      `struct-case:"0xa1b9cae1f8588968" json:",omitempty"`
		LobbyPlayerSessionsSuccessv3       *LobbyPlayerSessionsSuccessv3      `struct-case:"0xa1b9cae1f8588969" json:",omitempty"`
		LoginRequest                       *LoginRequest                      `struct-case:"0xbdb41ea9e67b200a" json:",omitempty"`
		LoginSuccess                       *LoginSuccess                      `struct-case:"0xa5acc1a90d0cce47" json:",omitempty"`
		LoginFailure                       *LoginFailure                      `struct-case:"0xa5b9d5a3021ccf51" json:",omitempty"`
		BroadcasterRegistrationFailure     *BroadcasterRegistrationFailure    `struct-case:"0xb56f25c7dfe6ffc9" json:",omitempty"`
		BroadcasterRegistrationSuccess     *BroadcasterRegistrationSuccess    `struct-case:"0xb57a31cdd0f6fedf" json:",omitempty"`
		DocumentFailure                    *DocumentFailure                   `struct-case:"0xd06ae97220a7b41f" json:",omitempty"`
		DocumentSuccess                    *DocumentSuccess                   `struct-case:"0xd07ffd782fb7b509" json:",omitempty"`
		UserServerProfileUpdateRequest     *UserServerProfileUpdateRequest    `struct-case:"0xd2986849b36b9c72" json:",omitempty"`
		UserServerProfileUpdateSuccess     *UserServerProfileUpdateSuccess    `struct-case:"0xd299785ba56b9c75" json:",omitempty"`
		LobbyStatusNotify                  *LobbyStatusNotify                 `struct-case:"0xe4b9b1cab57e8988" json:",omitempty"`
		LoginSettings                      *GameSettings                      `struct-case:"0xed5be2c3632155f1" json:",omitempty"`
		UpdateProfileFailure               *UpdateProfileFailure              `struct-case:"0xf24185da0edef641" json:",omitempty"`
		UpdateProfileSuccess               *UpdateProfileSuccess              `struct-case:"0xf25491d001cef757" json:",omitempty"`
		LobbyPingRequest                   *LobbyPingRequest                  `struct-case:"0xfabf5f8719bfebf3" json:",omitempty"`
		LoggedInUserProfileFailure         *LoggedInUserProfileFailure        `struct-case:"0xfb632e5a38ec8c61" json:",omitempty"`
		LoggedInUserProfileSuccess         *LoggedInUserProfileSuccess        `struct-case:"0xfb763a5037fc8d77" json:",omitempty"`
		LoggedInUserProfileRequest         *LoggedInUserProfileRequest        `struct-case:"0xfb772a4221fc8d70" json:",omitempty"`
		DocumentRequest                    *DocumentRequest                   `struct-case:"0xfcced6f169822bb8" json:",omitempty"`
		LobbyPlayerSessionsSuccessUnk1     *LobbyPlayerSessionsSuccessUnk1    `struct-case:"0xff71856af7e0fbd9" json:",omitempty"`
		Raw                                *RawMessage                        `struct:"default" json:",omitempty"`
	} `struct-switch:"Type"`
}

type RawMessage struct {
	Parent *Chunk `struct:"parent" json:"-"`
	Data   []byte `struct-size:"Parent.Len"`
}

func Unpack(data []byte) (p *Packet, err error) {
	err = restruct.Unpack(data, binary.LittleEndian, &p)
	return
}

func Pack(p *Packet) (data []byte, err error) {
	return restruct.Pack(binary.LittleEndian, p)
}
