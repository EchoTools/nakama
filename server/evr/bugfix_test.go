package evr

import (
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestGameServerSessionStart_RoundTrip(t *testing.T) {
	// Verify entrant data survives encode -> decode round-trip (bug: range copy loop)
	original := &GameServerSessionStart{
		MatchID:     uuid.Must(uuid.NewV4()),
		GroupID:     uuid.Must(uuid.NewV4()),
		PlayerLimit: 8,
		LobbyType:   byte(PublicLobby),
		Settings:    LobbySessionSettings{AppID: "test", Mode: 1, Level: 2},
		Entrants: []EntrantDescriptor{
			{
				Unk0:  uuid.Must(uuid.NewV4()),
				EvrID: EvrId{PlatformCode: OVR, AccountId: 12345},
				Flags: 0x0044BB8000,
			},
			{
				Unk0:  uuid.Must(uuid.NewV4()),
				EvrID: EvrId{PlatformCode: STM, AccountId: 67890},
				Flags: 0x0044BB8001,
			},
		},
	}

	// Encode
	encStream := NewEasyStream(EncodeMode, []byte{})
	if err := original.Stream(encStream); err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	encoded := encStream.Bytes()

	// Decode
	decoded := &GameServerSessionStart{}
	decStream := NewEasyStream(DecodeMode, encoded)
	if err := decoded.Stream(decStream); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Verify entrants survived the round-trip
	if len(decoded.Entrants) != len(original.Entrants) {
		t.Fatalf("entrant count: got %d, want %d", len(decoded.Entrants), len(original.Entrants))
	}

	for i, want := range original.Entrants {
		got := decoded.Entrants[i]
		if got.Unk0 != want.Unk0 {
			t.Errorf("entrant[%d].Unk0: got %s, want %s", i, got.Unk0, want.Unk0)
		}
		if got.EvrID != want.EvrID {
			t.Errorf("entrant[%d].EvrID: got %s, want %s", i, got.EvrID, want.EvrID)
		}
		if got.Flags != want.Flags {
			t.Errorf("entrant[%d].Flags: got %d, want %d", i, got.Flags, want.Flags)
		}
	}
}

func TestEvrId_Valid_IncludesSteam(t *testing.T) {
	// Bug: Valid() used > STM, excluding Steam platform (code 0)
	tests := []struct {
		name string
		id   EvrId
		want bool
	}{
		{"Steam user", EvrId{PlatformCode: STM, AccountId: 1}, true},
		{"OVR user", EvrId{PlatformCode: OVR, AccountId: 1}, true},
		{"Nil ID", EvrId{PlatformCode: 0, AccountId: 0}, false},
		{"Zero account", EvrId{PlatformCode: OVR, AccountId: 0}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.id.Valid()
			if got != tt.want {
				t.Errorf("EvrId%v.Valid() = %v, want %v", tt.id, got, tt.want)
			}
			// Valid() should match IsValid()
			if got != tt.id.IsValid() {
				t.Errorf("Valid() = %v but IsValid() = %v — inconsistency", got, tt.id.IsValid())
			}
		})
	}
}

func TestSymbol_HexString(t *testing.T) {
	// Bug: HexString padded with spaces instead of zeros
	tests := []struct {
		sym  Symbol
		want string
	}{
		{Symbol(0), "0x0000000000000000"},
		{Symbol(0xff), "0x00000000000000ff"},
		{Symbol(0xdeadbeef), "0x00000000deadbeef"},
		{Symbol(0xFFFFFFFFFFFFFFFF), "0xffffffffffffffff"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.sym.HexString()
			if got != tt.want {
				t.Errorf("Symbol(%d).HexString() = %q, want %q", tt.sym, got, tt.want)
			}
		})
	}
}
