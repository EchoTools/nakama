package service

import (
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
)

type MatchDataJournalEntry struct {
	CreatedAt time.Time `json:"created_at"`
	Data      any       `json:"data"`
}

type MatchDataJournal struct {
	MatchID   string                   `json:"match_id"`
	Events    []*MatchDataJournalEntry `json:"events"`
	CreatedAt time.Time                `json:"created_at"`
	UpdatedAt time.Time                `json:"updated_at"`
}

func NewMatchDataJournal(matchID MatchID) *MatchDataJournal {
	return &MatchDataJournal{
		MatchID:   matchID.String(),
		Events:    make([]*MatchDataJournalEntry, 0),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}

type MatchDataStarted struct {
	State *MatchLabel `json:"state"`
}

type MatchDataPlayerJoin struct {
	State    *LobbySessionState `json:"state"`
	Presence *LobbyPresence     `json:"presence"`
}

type MatchDataPlayerLeave struct {
	Label    *MatchLabel    `json:"state"`
	Presence *LobbyPresence `json:"presence"`
	Reason   string         `json:"reason"`
}

type MatchDataRemoteLogSet struct {
	UserID    string          `json:"sender_user_id"`
	SessionID string          `json:"session_id"`
	Logs      []evr.RemoteLog `json:"logs"`
}
