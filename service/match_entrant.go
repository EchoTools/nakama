package service

import (
	"encoding/json"
	"fmt"
)

type EntrantMetadata struct {
	Presence     *LobbyPresence
	Reservations []*LobbyPresence
}

func NewJoinMetadata(p *LobbyPresence) *EntrantMetadata {
	return &EntrantMetadata{Presence: p}
}

func (m EntrantMetadata) Presences() []*LobbyPresence {
	return append([]*LobbyPresence{m.Presence}, m.Reservations...)
}

func (m EntrantMetadata) ToMatchMetadata() map[string]string {

	bytes, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}

	return map[string]string{
		"entrants": string(bytes),
	}
}

func (m *EntrantMetadata) FromMatchMetadata(md map[string]string) error {
	if v, ok := md["entrants"]; ok {
		return json.Unmarshal([]byte(v), m)
	}
	return fmt.Errorf("`entrants` key not found")
}
