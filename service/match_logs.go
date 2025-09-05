package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type EventEnvelopeMatchData struct {
	MatchID MatchID         `json:"match_uuid"`
	Payload json.RawMessage `json:"data"`
}

func NewEventEnvelopeMatchData(matchID MatchID, payload any) (*EventEnvelopeMatchData, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal match data: %w", err)
	}
	return &EventEnvelopeMatchData{
		MatchID: matchID,
		Payload: data,
	}, nil
}

func (m *EventEnvelopeMatchData) MarshalEvent() (*api.Event, error) {
	return &api.Event{
		Name: EventMatchData,
		Properties: map[string]string{
			"match_id": m.MatchID.String(),
			"payload":  string(m.Payload),
		},
		External: true,
	}, nil
}

func MatchDataEvent(ctx context.Context, nk runtime.NakamaModule, matchID MatchID, data any) error {
	// Create a new match data journal entry
	entry := &MatchDataJournalEntry{
		CreatedAt: time.Now().UTC(),
		Data:      data,
	}

	// Create the event
	event := NewEventMatchDataJournal(matchID.String(), entry)
	
	// Send the event
	return SendEvent(ctx, nk, event)
}
