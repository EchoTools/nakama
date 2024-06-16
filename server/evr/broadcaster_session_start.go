package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/gofrs/uuid/v5"
)

type LobbyType byte

const (
	PublicLobby     LobbyType = iota // An active public lobby
	PrivateLobby                     // An active private lobby
	UnassignedLobby                  // An unloaded lobby
)

const (
	TeamUnassigned int = iota - 1
	TeamBlue
	TeamOrange
	TeamSpectator
	TeamSocial
	TeamModerator
)

var (
	ModeUnloaded       Symbol = ToSymbol("")                      // Unloaded Lobby
	ModeSocialPublic   Symbol = ToSymbol("social_2.0")            // Public Social Lobby
	ModeSocialPrivate  Symbol = ToSymbol("social_2.0_private")    // Private Social Lobby
	ModeSocialNPE      Symbol = ToSymbol("social_2.0_npe")        // Social Lobby NPE
	ModeArenaPublic    Symbol = ToSymbol("echo_arena")            // Public Echo Arena
	ModeArenaPrivate   Symbol = ToSymbol("echo_arena_private")    // Private Echo Arena
	ModeArenaTournment Symbol = ToSymbol("echo_arena_tournament") // Echo Arena Tournament
	ModeArenaPublicAI  Symbol = ToSymbol("echo_arena_public_ai")  // Public Echo Arena AI

	ModeEchoCombatTournament Symbol = ToSymbol("echo_combat_tournament") // Echo Combat Tournament
	ModeCombatPublic         Symbol = ToSymbol("echo_combat")            // Echo Combat
	ModeCombatPrivate        Symbol = ToSymbol("echo_combat_private")    // Private Echo Combat

	LevelUnloaded     Symbol = Symbol(0)                          // Unloaded Lobby
	LevelSocial       Symbol = ToSymbol("mpl_lobby_b2")           // Social Lobby
	LevelUnspecified  Symbol = Symbol(0xffffffffffffffff)         // Unspecified Level
	LevelArena        Symbol = ToSymbol("mpl_arena_a")            // Echo Arena
	ModeArenaTutorial Symbol = ToSymbol("mpl_tutorial_arena")     // Echo Arena Tutorial
	LevelFission      Symbol = ToSymbol("mpl_combat_fission")     // Echo Combat
	LevelCombustion   Symbol = ToSymbol("mpl_combat_combustion")  // Echo Combat
	LevelDyson        Symbol = ToSymbol("mpl_combat_dyson")       // Echo Combat
	LevelGauss        Symbol = ToSymbol("mpl_combat_gauss")       // Echo Combat
	LevelPebbles      Symbol = ToSymbol("mpl_combat_pebbles")     // Echo Combat
	LevelPtyPebbles   Symbol = ToSymbol("pty_mpl_combat_pebbles") // Echo Combat
)

type BroadcasterStartSession struct {
	MatchID     uuid.UUID           // The identifier for the game server session to start.
	Channel     uuid.UUID           // TODO: Unverified, suspected to be channel UUID.
	PlayerLimit byte                // The maximum amount of players allowed to join the lobby.
	LobbyType   byte                // The type of lobby
	Settings    SessionSettings     // The JSON settings associated with the session.
	Entrants    []EntrantDescriptor // Information regarding entrants (e.g. including offline/local player ids, or AI bot platform ids).
}

func (s *BroadcasterStartSession) String() string {
	return fmt.Sprintf("BroadcasterStartSession(session_id=%s, player_limit=%d, lobby_type=%d, settings=%s, entrant_descriptors=%v)",
		s.MatchID, s.PlayerLimit, s.LobbyType, s.Settings.String(), s.Entrants)
}

func NewBroadcasterStartSession(sessionID uuid.UUID, channel uuid.UUID, playerLimit uint8, lobbyType uint8, appID string, mode Symbol, level Symbol, features []string, entrants []EvrId) *BroadcasterStartSession {
	descriptors := make([]EntrantDescriptor, len(entrants))
	for i, entrant := range entrants {
		descriptors[i] = *NewEntrantDescriptor(entrant)
	}

	return &BroadcasterStartSession{
		MatchID:     sessionID,
		Channel:     channel,
		PlayerLimit: byte(playerLimit),
		LobbyType:   byte(lobbyType),
		Settings:    NewSessionSettings(appID, mode, level, features),
		Entrants:    descriptors,
	}
}

type SessionSettings struct {
	AppID    string   `json:"appid"`
	Mode     int64    `json:"gametype"`
	Level    *int64   `json:"level"`
	Features []string `json:"features,omitempty"`
}

func NewSessionSettings(appID string, mode Symbol, level Symbol, features []string) SessionSettings {

	settings := SessionSettings{
		AppID:    appID,
		Mode:     int64(mode),
		Level:    nil,
		Features: features,
	}
	if level != 0 {
		l := int64(level)
		settings.Level = &l
	}
	return settings
}

func (s *SessionSettings) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type EntrantDescriptor struct {
	Unk0     uuid.UUID
	PlayerId EvrId
	Flags    uint64
}

func (m *EntrantDescriptor) String() string {
	return fmt.Sprintf("EREntrantDescriptor(unk0=%s, player_id=%s, flags=%d)", m.Unk0, m.PlayerId.String(), m.Flags)
}

func NewEntrantDescriptor(playerId EvrId) *EntrantDescriptor {
	return &EntrantDescriptor{
		Unk0:     uuid.Must(uuid.NewV4()),
		PlayerId: playerId,
		Flags:    0x0044BB8000,
	}
}

func RandomBotEntrantDescriptor() EntrantDescriptor {
	botuuid, _ := uuid.NewV4()
	return EntrantDescriptor{
		Unk0:     botuuid,
		PlayerId: EvrId{PlatformCode: BOT, AccountId: rand.Uint64()},
		Flags:    0x0044BB8000,
	}
}

func (m *BroadcasterStartSession) Stream(s *EasyStream) error {
	finalStructCount := byte(len(m.Entrants))
	pad1 := byte(0)
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.MatchID) },
		func() error { return s.StreamGuid(&m.Channel) },
		func() error { return s.StreamByte(&m.PlayerLimit) },
		func() error { return s.StreamNumber(binary.LittleEndian, &finalStructCount) },
		func() error { return s.StreamByte(&m.LobbyType) },
		func() error { return s.StreamByte(&pad1) },
		func() error { return s.StreamJson(&m.Settings, true, NoCompression) },
		func() error {
			if s.Mode == DecodeMode {
				m.Entrants = make([]EntrantDescriptor, finalStructCount)
			}
			for _, entrant := range m.Entrants {
				err := RunErrorFunctions([]func() error{
					func() error { return s.StreamGuid(&entrant.Unk0) },
					func() error { return s.StreamStruct(&entrant.PlayerId) },
					func() error { return s.StreamNumber(binary.LittleEndian, &entrant.Flags) },
				})
				if err != nil {
					return err
				}
			}
			return nil
		},
	})

}
