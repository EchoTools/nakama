package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// MatchmakerState captures the complete state of a matchmaking cycle for replay
type MatchmakerState struct {
	Timestamp         time.Time          `json:"timestamp"`
	Mode              string             `json:"mode"`
	GroupID           string             `json:"group_id"`
	Candidates        []CandidateGroup   `json:"candidates"`
	Matches           []CandidateGroup   `json:"matches"`
	FilterCounts      map[string]int     `json:"filter_counts"`
	ProcessingTimeMs  int64              `json:"processing_time_ms"`
	TotalPlayers      int                `json:"total_players"`
	TotalTickets      int                `json:"total_tickets"`
	MatchedPlayers    int                `json:"matched_players"`
	UnmatchedPlayers  int                `json:"unmatched_players"`
	MatchCount        int                `json:"match_count"`
	PredictionDetails []PredictionDetail `json:"prediction_details,omitempty"`
}

// CandidateGroup represents a group of matchmaker entries (potential match)
type CandidateGroup struct {
	Entries []MatchmakerEntrySnapshot `json:"entries"`
}

// MatchmakerEntrySnapshot captures all relevant data from a matchmaker entry
type MatchmakerEntrySnapshot struct {
	Ticket            string             `json:"ticket"`
	Presence          PresenceSnapshot   `json:"presence"`
	Properties        map[string]any     `json:"properties"`
	NumericProperties map[string]float64 `json:"numeric_properties"`
	StringProperties  map[string]string  `json:"string_properties"`
	PartyID           string             `json:"party_id"`
}

// PresenceSnapshot captures presence data
type PresenceSnapshot struct {
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
	Username  string `json:"username"`
}

// PredictionDetail captures details about a predicted match for debugging
type PredictionDetail struct {
	CandidateIndex        int             `json:"candidate_index"`
	Size                  int8            `json:"size"`
	DivisionCount         int8            `json:"division_count"`
	OldestTicketTimestamp int64           `json:"oldest_ticket_timestamp"`
	OldestTicketAge       float64         `json:"oldest_ticket_age_seconds"`
	DrawProb              float32         `json:"draw_prob"`
	Selected              bool            `json:"selected"`
	RejectionReason       string          `json:"rejection_reason,omitempty"`
	Tickets               []TicketSummary `json:"tickets"`
}

// TicketSummary provides a summary of a ticket in a prediction
type TicketSummary struct {
	Ticket          string  `json:"ticket"`
	PlayerCount     int     `json:"player_count"`
	RatingMu        float64 `json:"rating_mu"`
	RatingSigma     float64 `json:"rating_sigma"`
	WaitTimeSeconds float64 `json:"wait_time_seconds"`
	Divisions       string  `json:"divisions"`
}

// captureMatchmakerEntry converts a runtime.MatchmakerEntry to a snapshot
func captureMatchmakerEntry(entry runtime.MatchmakerEntry) MatchmakerEntrySnapshot {
	snapshot := MatchmakerEntrySnapshot{
		Ticket:            entry.GetTicket(),
		Properties:        entry.GetProperties(),
		NumericProperties: make(map[string]float64),
		StringProperties:  make(map[string]string),
		PartyID:           entry.GetPartyId(),
	}

	if presence := entry.GetPresence(); presence != nil {
		snapshot.Presence = PresenceSnapshot{
			UserID:    presence.GetUserId(),
			SessionID: presence.GetSessionId(),
			Username:  presence.GetUsername(),
		}
	}

	// Extract numeric properties separately for easier analysis
	for k, v := range entry.GetProperties() {
		switch val := v.(type) {
		case float64:
			snapshot.NumericProperties[k] = val
		case string:
			snapshot.StringProperties[k] = val
		}
	}

	return snapshot
}

// CaptureMatchmakerState captures the complete state of a matchmaking cycle
func (m *SkillBasedMatchmaker) CaptureMatchmakerState(
	ctx context.Context,
	mode string,
	groupID string,
	candidates [][]runtime.MatchmakerEntry,
	matches [][]runtime.MatchmakerEntry,
	filterCounts map[string]int,
	processingTime time.Duration,
	predictions []PredictedMatch,
) *MatchmakerState {
	state := &MatchmakerState{
		Timestamp:        time.Now().UTC(),
		Mode:             mode,
		GroupID:          groupID,
		Candidates:       make([]CandidateGroup, 0, len(candidates)),
		Matches:          make([]CandidateGroup, 0, len(matches)),
		FilterCounts:     filterCounts,
		ProcessingTimeMs: processingTime.Milliseconds(),
	}

	// Capture candidates
	playerSet := make(map[string]struct{})
	ticketSet := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		group := CandidateGroup{
			Entries: make([]MatchmakerEntrySnapshot, 0, len(candidate)),
		}
		for _, entry := range candidate {
			group.Entries = append(group.Entries, captureMatchmakerEntry(entry))
			ticketSet[entry.GetTicket()] = struct{}{}
			playerSet[entry.GetPresence().GetUserId()] = struct{}{}
		}
		state.Candidates = append(state.Candidates, group)
	}

	// Capture matches
	matchedPlayerSet := make(map[string]struct{})
	for _, match := range matches {
		group := CandidateGroup{
			Entries: make([]MatchmakerEntrySnapshot, 0, len(match)),
		}
		for _, entry := range match {
			group.Entries = append(group.Entries, captureMatchmakerEntry(entry))
			matchedPlayerSet[entry.GetPresence().GetUserId()] = struct{}{}
		}
		state.Matches = append(state.Matches, group)
	}

	state.TotalPlayers = len(playerSet)
	state.TotalTickets = len(ticketSet)
	state.MatchedPlayers = len(matchedPlayerSet)
	state.UnmatchedPlayers = len(playerSet) - len(matchedPlayerSet)
	state.MatchCount = len(matches)

	// Capture prediction details if available
	if len(predictions) > 0 {
		now := time.Now().UTC().Unix()
		matchedTickets := make(map[string]bool)
		for _, match := range matches {
			for _, entry := range match {
				matchedTickets[entry.GetTicket()] = true
			}
		}

		state.PredictionDetails = make([]PredictionDetail, 0, len(predictions))
		for i, pred := range predictions {
			detail := PredictionDetail{
				CandidateIndex:        i,
				Size:                  pred.Size,
				DivisionCount:         pred.DivisionCount,
				OldestTicketTimestamp: pred.OldestTicketTimestamp,
				OldestTicketAge:       float64(now - pred.OldestTicketTimestamp),
				DrawProb:              pred.DrawProb,
				Tickets:               make([]TicketSummary, 0),
			}

			// Check if any ticket from this prediction was selected
			selected := false
			ticketMap := make(map[string][]runtime.MatchmakerEntry)
			for _, entry := range pred.Candidate {
				ticket := entry.GetTicket()
				ticketMap[ticket] = append(ticketMap[ticket], entry)
				if matchedTickets[ticket] {
					selected = true
				}
			}
			detail.Selected = selected

			// Add ticket summaries
			for ticket, entries := range ticketMap {
				if len(entries) == 0 {
					continue
				}
				props := entries[0].GetProperties()
				mu, _ := props["rating_mu"].(float64)
				sigma, _ := props["rating_sigma"].(float64)
				submissionTime, _ := props["submission_time"].(float64)
				divisions, _ := props["divisions"].(string)

				waitTime := float64(0)
				if submissionTime > 0 {
					waitTime = float64(now) - submissionTime
				}

				summary := TicketSummary{
					Ticket:          ticket,
					PlayerCount:     len(entries),
					RatingMu:        mu,
					RatingSigma:     sigma,
					WaitTimeSeconds: waitTime,
					Divisions:       divisions,
				}
				detail.Tickets = append(detail.Tickets, summary)
			}

			state.PredictionDetails = append(state.PredictionDetails, detail)
		}
	}

	return state
}

// SaveMatchmakerState saves the matchmaker state to a JSON file
func SaveMatchmakerState(state *MatchmakerState, directory string) (string, error) {
	if directory == "" {
		directory = "/tmp/matchmaker_replay"
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// Generate filename with timestamp
	filename := fmt.Sprintf("matchmaker_state_%s_%s.json",
		state.Timestamp.Format("20060102_150405"),
		state.Mode,
	)
	filepath := filepath.Join(directory, filename)

	// Marshal to JSON with indentation
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filepath, nil
}

// LoadMatchmakerState loads a matchmaker state from a JSON file
func LoadMatchmakerState(filepath string) (*MatchmakerState, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var state MatchmakerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return &state, nil
}
