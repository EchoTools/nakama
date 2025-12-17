package server

import (
	"context"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

var _ = Event(&EventMatchDisconnect{})

// PlayerDisconnectRecord captures the per-player disconnect payload we persist to MongoDB.
type PlayerDisconnectRecord struct {
	UserID                  string        `json:"user_id"`
	Username                string        `json:"username,omitempty"`
	DisplayName             string        `json:"display_name,omitempty"`
	Team                    TeamIndex     `json:"team"`
	PartyID                 string        `json:"party_id,omitempty"`
	PartySize               int           `json:"party_size"`
	PingBand                PingBand      `json:"ping_band"`
	PingMillis              int           `json:"ping_ms,omitempty"`
	JoinTime                time.Time     `json:"join_time"`
	LeaveTime               time.Time     `json:"leave_time"`
	IsAbandoner             bool          `json:"is_abandoner"`
	ScoresAtJoin            [2]int        `json:"scores_at_join"`
	ScoresAtLeave           [2]int        `json:"scores_at_leave"`
	TeamSizesAtJoin         [2]int        `json:"team_sizes_at_join"`
	TeamSizesAtLeave        [2]int        `json:"team_sizes_at_leave"`
	ScoreDeltaAtJoin        int           `json:"score_delta_at_join"`
	ScoreDeltaAtLeave       int           `json:"score_delta_at_leave"`
	TeamDisadvantageAtJoin  int           `json:"team_disadvantage_at_join"`
	TeamDisadvantageAtLeave int           `json:"team_disadvantage_at_leave"`
	WasLosingAtLeave        bool          `json:"was_losing_at_leave"`
	DisconnectsAtJoin       int           `json:"disconnects_at_join"`
	DisconnectsAtLeave      int           `json:"disconnects_at_leave"`
	TeamHazardEvents        int           `json:"team_hazard_events"`
	TeamConsecutiveHazards  int           `json:"team_consecutive_hazards"`
	IsTeamWipeMember        bool          `json:"is_team_wipe_member"`
	GameDurationAtJoin      time.Duration `json:"game_duration_at_join_ns"`
	GameDurationAtLeave     time.Duration `json:"game_duration_at_leave_ns"`
	IntegrityDuration       time.Duration `json:"integrity_duration_ns"`
	DisadvantageDuration    time.Duration `json:"disadvantage_duration_ns"`
	SessionID               string        `json:"session_id,omitempty"`
}

// MatchDisconnectSnapshot is a lightweight view of the final match state stored with the disconnect event.
type MatchDisconnectSnapshot struct {
	MatchID     string             `json:"match_id"`
	Node        string             `json:"node"`
	Mode        string             `json:"mode"`
	Level       string             `json:"level"`
	LobbyType   string             `json:"lobby_type"`
	GroupID     string             `json:"group_id,omitempty"`
	OperatorID  string             `json:"operator_id,omitempty"`
	TeamSize    int                `json:"team_size,omitempty"`
	PlayerLimit int                `json:"player_limit,omitempty"`
	MaxSize     int                `json:"max_size,omitempty"`
	PlayerCount int                `json:"player_count,omitempty"`
	StartTime   time.Time          `json:"start_time,omitempty"`
	CreatedAt   time.Time          `json:"created_at,omitempty"`
	FinalScores [2]int             `json:"final_scores"`
	MatchOver   bool               `json:"match_over"`
	Scoreboard  *SessionScoreboard `json:"scoreboard,omitempty"`
	Players     []PlayerInfo       `json:"players,omitempty"`
}

// EventMatchDisconnect is emitted when a match records disconnect information for players.
type EventMatchDisconnect struct {
	Match       MatchDisconnectSnapshot  `json:"match"`
	Disconnects []PlayerDisconnectRecord `json:"disconnects"`
	CreatedAt   time.Time                `json:"created_at"`
}

// Process persists the event into MongoDB.
func (e *EventMatchDisconnect) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	if dispatcher.mongo == nil {
		logger.Warn("mongodb client not configured; skipping match disconnect insert")
		return nil
	}

	collection := dispatcher.mongo.Database(matchDataDatabaseName).Collection(matchDisconnectCollectionName)
	if _, err := collection.InsertOne(ctx, e); err != nil {
		return fmt.Errorf("failed to insert match disconnect: %w", err)
	}
	return nil
}
