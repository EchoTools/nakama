package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	evr "github.com/echotools/nakama/v3/protocol"
)

type LabelPart interface {
	// Query returns the label part as a query string (e.g. "+label.lobbytype:0")
	Query(o QueryOperator, b int) string
	// Label returns the label part as a label string (e.g. "lobbytype=0")
}

type QueryOperator rune // '', '+', '-'

const (
	Must    QueryOperator = '+'
	Should  QueryOperator = ' '
	MustNot QueryOperator = '-'
)

type Label struct {
	Op    rune   // '+', ' ', '-' denoting must, should, must not
	Name  string // label name
	Value string // label value
	boost int    // boost value (appended as ^N)
}

// Escaped returns the label as a query string (e.g. "+label.mode:social_2\.0^2")
func (l Label) Escaped() string {
	b := ""
	if l.boost != 0 {
		b = fmt.Sprintf("^%d", l.boost)
	}
	return fmt.Sprintf("%clabel.%s:%s%s", l.Op, l.Name, Query.QuoteStringValue(l.Value), b)
}

// Escaped returns the label as a query string (e.g. "+label.mode:social_2\.0^2")
func (l Label) Unescaped() string {
	b := ""
	if l.boost != 0 {
		b = fmt.Sprintf("^%d", l.boost)
	}
	return fmt.Sprintf("%clabel.%s:%s%s", l.Op, l.Name, l.Value, b)
}

// Escaped returns the label as a query string (e.g. "+label.mode:social_2\.0^2")
func (l Label) Property() string {
	b := ""
	if l.boost != 0 {
		b = fmt.Sprintf("^%d", l.boost)
	}
	return fmt.Sprintf("%cproperties.%s:%s%s", l.Op, l.Name, Query.QuoteStringValue(l.Value), b)
}

// Label returns the label as a label string (e.g. "lobbytype=0")

// The symbol representation of the label name (only used by a few labels)
func (l Label) Symbol() evr.Symbol {
	return evr.ToSymbol(string(l.Name))
}

var OpenLobby = LobbyState(true)
var LockedLobby = LobbyState(false)

const (
	PublicLobby     LobbyType = iota // An active public lobby
	PrivateLobby                     // An active private lobby
	UnassignedLobby                  // An unloaded lobby
)

// Lobby type determines if the lobby is public or private (or nothing).
type LobbyType uint8 // iota

func (l LobbyType) Query(o QueryOperator, b int) string {
	return Label{
		Op:    rune(o),
		Name:  "lobby_type",
		Value: l.String(),
		boost: b,
	}.Escaped()
}

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

func (l LobbyType) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.String())
}

func (l *LobbyType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch strings.ToLower(s) {
	default:
		i, err := strconv.Atoi(s)
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

// Lobby State determines if the lobby is open or locked.
type LobbyState bool

func (l LobbyState) Query(o QueryOperator, _b int) string {
	return Label{
		Op:    rune(o),
		Name:  "open",
		Value: l.String(),
		boost: _b,
	}.Escaped()
}

func (l LobbyState) String() string {
	if l {
		return "T"
	}
	return "F"
}

// The Team Index determines the team that the player is on.
type TeamIndex int16

const (
	AnyTeam TeamIndex = iota - 1
	BlueTeam
	OrangeTeam
	Spectator
	SocialLobbyParticipant
	Moderator // Moderator is invisible to other players and able to fly around.
)

func (t TeamIndex) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

func MarshalText(t TeamIndex) ([]byte, error) {
	return []byte(t.String()), nil
}

func (t *TeamIndex) UnmarshalText(b []byte) error {
	switch strings.ToLower(string(b)) {
	default:
		i, err := strconv.Atoi(string(b))
		if err != nil {
			return err
		}
		*t = TeamIndex(i)
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

func (t TeamIndex) String() string {

	switch t {
	default:
		return "unk"
	case AnyTeam:
		return "any"
	case OrangeTeam:
		return "orange"
	case BlueTeam:
		return "blue"
	case Spectator:
		return "spectator"
	case SocialLobbyParticipant:
		return "social"
	case Moderator:
		return "moderator"

	}
}

// The Match LabelGameMode specifies the type and class of match. (e.g. Echo Arena, Echo Combat, Social, etc.)
// The Match LabelGameMode is represented as a symbol in Evr messages.
type LabelGameMode evr.Symbol // Symbol

func (m LabelGameMode) Query(o QueryOperator, b int) string {
	return m.Label(o, b).Escaped()
}
func (m LabelGameMode) Label(o QueryOperator, b int) Label {
	return Label{
		Op:    rune(o),
		Name:  "mode",
		Value: evr.Symbol(m).Token().String(),
		boost: b,
	}
}

func (m LabelGameMode) String() string {
	if uint64(m) == uint64(0) {
		return ""
	}
	return string(evr.Symbol(m).Token())
}

// The Match Level is the map that the match is played on.
// The Match Level is represented as a symbol in Evr messages.
type Level evr.Symbol // Symbol

func (l Level) String() string {
	if uint64(l) == uint64(0) {
		return ""
	}
	return evr.Symbol(l).Token().String()
}

func (l Level) Query(o QueryOperator, b int) string {
	return Label{
		Op:    rune(o),
		Name:  "level",
		Value: evr.Symbol(l).Token().String(),
		boost: b,
	}.Escaped()
}

func (l Level) Label(o QueryOperator, b int) Label {
	return Label{
		Op:    rune(o),
		Name:  "level",
		Value: evr.Symbol(l).Token().String(),
		boost: b,
	}
}
func (l *MatchStatGroup) Query(o QueryOperator, b int) string {
	return fmt.Sprintf("%clabel.statgroup:%s^%d", o, Query.QuoteStringValue(string(*l)), b)
}

func (l *MatchLevelSelection) Query(o QueryOperator, b int) string {
	return fmt.Sprintf("%clabel.levelselect:%s^%d", o, Query.QuoteStringValue(string(*l)), b)
}

// The Match Region is the region the match is hosted in.
// The Match Region is represented as a symbol in Evr messages.
// The Match Region is stored as a string in the match registry.
// On Symbol Cache misses, it is stored as a hex string. (e.g. 0x0000000000000001)
type Region evr.Symbol

func (r Region) Query(o QueryOperator, b int) string {
	return Label{
		Op:    rune(o),
		Name:  "broadcaster.region_codes",
		Value: evr.Symbol(r).Token().String(),
		boost: b,
	}.Escaped()
}
