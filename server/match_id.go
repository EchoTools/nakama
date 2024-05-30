package server

import (
	"errors"

	"strings"

	"github.com/gofrs/uuid/v5"
)

var (
	ErrInvalidMatchTokenFormat = errors.New("invalid match token format")
	ErrInvalidMatchUUID        = errors.New("invalid match ID")
	ErrInvalidMatchNode        = errors.New("invalid match node")
	ErrInvalidMatchID          = errors.New("invalid match token")
)

// MatchID represents a unique identifier for a match, consisting of a uuid.UUID and a node name.
type MatchID struct {
	uuid uuid.UUID
	node string
}

// UUID returns the UUID of the match.
func (t MatchID) UUID() uuid.UUID {
	return t.uuid
}

// Node returns the node name of the match.
func (t MatchID) Node() string {
	return t.node
}

var NilMatchID = MatchID{}

// Equals returns true if the match ID is equal to the other match ID.
func (t MatchID) Equals(other MatchID) bool {
	return t.uuid == other.uuid && t.node == other.node
}

// IsNil returns true if the match ID is nil.
func (t MatchID) IsNil() bool {
	return NilMatchID == t
}

// NewMatchID creates a new match ID.
func NewMatchID(id uuid.UUID, node string) (t MatchID, err error) {
	switch {
	case id == uuid.Nil:
		err = ErrInvalidMatchUUID
	case node == "":
		err = ErrInvalidMatchNode
	default:
		t.uuid = id
		t.node = node
	}
	return
}

// String returns the string representation of the match ID (UUID + node).
func (t MatchID) String() string {
	if t.IsNil() {
		return ""
	}
	return t.uuid.String() + "." + t.node
}

// IsValid returns true if the match ID is valid (has a node and a non-nil UUID)
func (t MatchID) IsValid() bool {
	return t.uuid != uuid.Nil && t.node != ""
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
	if len(s) < 38 || s[36] != '.' {
		return t, ErrInvalidMatchID
	}

	components := strings.SplitN(s, ".", 2)
	t.uuid = uuid.FromStringOrNil(components[0])
	t.node = components[1]

	switch {
	case t.uuid == uuid.Nil:
		err = ErrInvalidMatchUUID
	case t.node == "":
		err = ErrInvalidMatchNode
	}
	return
}

// MatchIDFromStringOrNil creates a match ID from a string, returning a nil match ID if the string is empty.
func MatchIDFromStringOrNil(s string) (t MatchID) {
	if s == "" {
		return
	}
	t, err := MatchIDFromString(s)
	if err != nil {
		return NilMatchID
	}
	return
}
