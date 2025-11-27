package server

import (
	"context"
	"encoding/json"
	"fmt"

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

	/*
		// FIXME: This generates too much data, disabled for now.

			entry, err := NewEventEnvelopeMatchData(matchID, data)
			if err != nil {
				return fmt.Errorf("failed to create match data event: %w", err)
			}

			evt, err := entry.MarshalEvent()
			if err != nil {
				return fmt.Errorf("failed to marshal match data event: %w", err)
			}

			if err := nk.Event(ctx, evt); err != nil {
				return fmt.Errorf("failed to add match data event: %w", err)
			}
	*/

	return nil
}
