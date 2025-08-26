package service

import (
	"strconv"
	"strings"
)

const (
	PublicLobby     LobbyType = iota // An active public lobby
	PrivateLobby                     // An active private lobby
	UnassignedLobby                  // An unloaded lobby
)

// Lobby type determines if the lobby is public or private (or nothing).
type LobbyType uint8 // iota

func (l LobbyType) String() string {
	switch l {
	case PublicLobby:
		return "public"
	case PrivateLobby:
		return "private"
	case UnassignedLobby:
		return "unassigned"
	default:
		return "unk"
	}
}

func (l LobbyType) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

func (l *LobbyType) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	default:
		i, err := strconv.Atoi(string(b))
		if err != nil {
			return err
		}
		*l = LobbyType(i)
	case "public":
		*l = PublicLobby
	case "private":
		*l = PrivateLobby
	case "unassigned":
		*l = UnassignedLobby
	}
	return nil
}

// The Team Index determines the team that the player is on.
type RoleIndex int16

const (
	AnyTeam RoleIndex = iota - 1
	BlueTeam
	OrangeTeam
	Spectator
	SocialLobbyParticipant
	Moderator // Moderator is invisible to other players and able to fly around.
)

func (t RoleIndex) MarshalText() ([]byte, error) {
	switch t {
	case AnyTeam:
		return []byte("any"), nil
	case BlueTeam:
		return []byte("blue"), nil
	case OrangeTeam:
		return []byte("orange"), nil
	case Spectator:
		return []byte("spectator"), nil
	case SocialLobbyParticipant:
		return []byte("social"), nil
	case Moderator:
		return []byte("moderator"), nil
	default:
		return []byte("unk"), nil
	}
}

func (t *RoleIndex) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	default:
		i, err := strconv.Atoi(string(b))
		if err != nil {
			return err
		}
		*t = RoleIndex(i)
	case "":
		*t = AnyTeam
	case "any":
		*t = AnyTeam
	case "orange":
		*t = OrangeTeam
	case "blue":
		*t = BlueTeam
	case "spectator":
		*t = Spectator
	case "social":
		*t = SocialLobbyParticipant
	case "moderator":
		*t = Moderator
	}
	return nil
}

func (t RoleIndex) String() string {
	s, _ := t.MarshalText()
	return string(s)
}
