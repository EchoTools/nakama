package evr

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
)

func TestGameServerSesssionParsers(t *testing.T) {
	testCases := []struct {
		name string
		data []byte
		want Message
	}{
		{
			name: "Game Server Registration",
			data: []byte{
				0xf6, 0x40, 0xbb, 0x78, 0xa2, 0xe7, 0x8c, 0xbb,
				0x35, 0x85, 0xf6, 0xeb, 0x9f, 0xba, 0x81, 0xe5,
				0x38, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0xea, 0xe4, 0x8f, 0x92, 0xbd, 0x7c, 0x2e, 0x4b,
				0xc0, 0xa8, 0x38, 0x01, 0x88, 0x1a, 0x00, 0x00, 0x2a, 0x12, 0x3c, 0xd3, 0x14, 0x01, 0xe6, 0xf1,
				0x0d, 0x91, 0x77, 0x8f, 0xd7, 0x01, 0x2f, 0xc6, 0xb3, 0x15, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			want: &LobbyFindSessionRequest{
				VersionLock:      0xc62f01d78f77910d,
				Mode:             ModeSocialPublic,
				Level:            LevelUnspecified,
				Platform:         ToSymbol("OVR"),
				CrossPlayEnabled: true,
				LoginSessionID:   uuid.FromStringOrNil("1251ac1f11bc11ef931a66d3ff8a653b"),
				GroupID:          uuid.UUID{0x2e, 0x74, 0x47, 0xd8, 0x4c, 0xe2, 0x48, 0x71, 0x9e, 0x4b, 0x13, 0xa4, 0x7a, 0x5a, 0x6f, 0xa6},
				SessionSettings: LobbySessionSettings{
					AppID: "1369078409873402",
					Mode:  int64(ModeSocialPublic),
					Level: int64(LevelUnspecified),
				},
				Entrants: []Entrant{
					{
						EvrID: EvrId{
							PlatformCode: 4,
							AccountId:    3963667097037078,
						},
						Role: int8(TeamUnassigned),
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := WrapBytes(SymbolOf(&LobbyFindSessionRequest{}), tc.data)

			messages, err := ParsePacket(data)
			if err != nil {
				t.Fatalf(err.Error())
			}

			if diff := cmp.Diff(tc.want, messages[0]); diff != "" {
				t.Errorf("unexpected LobbyFindSessionRequest (-want +got):\n%s", diff)
			}
		})
	}

}
