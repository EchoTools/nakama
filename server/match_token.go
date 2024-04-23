package server

import (
	"errors"
	"fmt"

	"strings"

	"github.com/gofrs/uuid/v5"
)

var (
	ErrInvalidMatchTokenFormat = errors.New("invalid match token format")
	ErrInvalidMatchID          = errors.New("invalid match ID")
	ErrInvalidMatchNode        = errors.New("invalid match node")
	ErrInvalidMatchToken       = errors.New("invalid match token")
)

// MatchToken represents a unique identifier for a match, consisting of a uuid.UUID and a node name.
type MatchToken string

func (MatchToken) Nil() MatchToken {
	return MatchToken("")
}

func NewMatchToken(id uuid.UUID, node string) (MatchToken, error) {
	if id == uuid.Nil || node == "" {
		return "", ErrInvalidMatchToken
	}
	return MatchToken(fmt.Sprintf("%s.%s", id, node)), nil
}

func (t MatchToken) String() string {
	return string(t)
}

func (t MatchToken) ID() uuid.UUID {
	parts := strings.Split(string(t), ".")
	id, _ := uuid.FromString(parts[0])
	return id
}

func (t MatchToken) Node() string {
	parts := strings.Split(string(t), ".")
	return parts[1]
}

func (t MatchToken) IsValid() bool {
	if t == "" {
		return false
	}
	return MatchTokenFromStringOrNil(t.String()) == t
}

func (t *MatchToken) UnmarshalText(data []byte) error {
	token, err := MatchTokenFromString(string(data))
	if err != nil {
		return err
	}
	*t = token
	return nil
}

func MatchTokenFromString(s string) (t MatchToken, err error) {
	if s == "" {
		return
	}

	parts := strings.SplitN(s, ".", 2)
	switch {
	case len(parts) != 2:
		err = ErrInvalidMatchTokenFormat
	case parts[0] == "" || uuid.FromStringOrNil(parts[0]) == uuid.Nil:
		err = ErrInvalidMatchID
	case parts[1] == "":
		err = ErrInvalidMatchNode
	}
	if err == nil {
		t = MatchToken(s)
	}
	return
}

// FromStringOrNil returns a UUID parsed from the input string. Same behavior as FromString(), but returns uuid.Nil instead of an error.

// FromStringOrNil returns a UUID parsed from the input string.
// Same behavior as FromString(), but returns uuid.Nil instead of an error.
func MatchTokenFromStringOrNil(s string) MatchToken {
	t, err := MatchTokenFromString(s)
	if err != nil {
		t = ""
	}
	return t
}
