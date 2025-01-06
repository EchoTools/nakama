package server

import (
	"errors"
	"regexp"

	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

var (
	ErrInvalidMatchTokenFormat = errors.New("invalid match token format")
	ErrInvalidMatchUUID        = errors.New("invalid match ID")
	ErrInvalidMatchNode        = errors.New("invalid match node")
	MatchUUIDPattern           = regexp.MustCompile("[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
)

// MatchID represents a unique identifier for a match, consisting of a uuid.UUID and a node name.
type MatchID struct {
	UUID uuid.UUID
	Node string
}

var NilMatchID = MatchID{}

// Equals returns true if the match ID is equal to the other match ID.
func (t MatchID) Equals(other MatchID) bool {
	return t.UUID == other.UUID && t.Node == other.Node
}

// IsNil returns true if the match ID is nil.
func (t MatchID) IsNil() bool {
	return NilMatchID == t
}

// NewMatchID creates a new match ID.
func NewMatchID(id uuid.UUID, node string) (t MatchID, err error) {
	switch {
	case id == uuid.Nil:
		err = errors.Join(runtime.ErrMatchIdInvalid, ErrInvalidMatchUUID)
	case node == "":
		err = errors.Join(runtime.ErrMatchIdInvalid, ErrInvalidMatchNode)
	default:
		t.UUID = id
		t.Node = node
	}
	return
}

// String returns the string representation of the match ID (UUID + node).
func (t MatchID) String() string {
	if t.IsNil() {
		return ""
	}
	return t.UUID.String() + "." + t.Node
}

// IsValid returns true if the match ID is valid (has a node and a non-nil UUID)
func (t MatchID) IsValid() bool {
	return t.UUID != uuid.Nil && t.Node != ""
}

// MarshalText returns the text representation of the match ID.
func (t MatchID) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

// UnmarshalText sets the match ID to the value represented by the text.
func (t *MatchID) UnmarshalText(data []byte) error {
	id, err := MatchIDFromString(string(data))
	if err != nil {
		return err
	}
	*t = id
	return nil
}

// MatchIDFromString creates a match ID from a string (splitting the UUID and node).
func MatchIDFromString(s string) (t MatchID, err error) {
	if len(s) == 0 {
		return t, nil
	}
	if len(s) < 38 || s[36] != '.' {
		return t, runtime.ErrMatchIdInvalid
	}

	components := strings.SplitN(s, ".", 2)
	t.UUID = uuid.FromStringOrNil(components[0])
	t.Node = components[1]

	if !t.IsValid() {
		return t, runtime.ErrMatchIdInvalid
	}
	return
}

// MatchIDFromStringOrNil creates a match ID from a string, returning a nil match ID if the string is empty.
func MatchIDFromStringOrNil(s string) (t MatchID) {
	if s == "" {
		return NilMatchID
	}
	t, err := MatchIDFromString(s)
	if err != nil {
		return NilMatchID
	}
	return
}
