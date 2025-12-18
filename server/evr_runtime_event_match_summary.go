package server

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	matchSummaryCollectionName     = "match_summaries"
	matchDisconnectsCollectionName = "match_disconnects_view"
)

var _ = Event(&EventMatchSummary{})

// PlayerParticipation tracks a player's full participation in a match,
// including join/leave times and state at various points.
type PlayerParticipation struct {
	// Core player info (snapshot at join time)
	UserID      string    `json:"user_id" bson:"user_id"`
	Username    string    `json:"username,omitempty" bson:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty" bson:"display_name,omitempty"`
	DiscordID   string    `json:"discord_id,omitempty" bson:"discord_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty" bson:"session_id,omitempty"`
	PartyID     string    `json:"party_id,omitempty" bson:"party_id,omitempty"`
	Team        TeamIndex `json:"team" bson:"team"`

	// Party and connection info
	PartySize  int      `json:"party_size" bson:"party_size"`
	PingBand   PingBand `json:"ping_band" bson:"ping_band"`
	PingMillis int      `json:"ping_ms,omitempty" bson:"ping_ms,omitempty"`

	// Join state
	JoinTime             time.Time     `json:"join_time" bson:"join_time"`
	ClockRemainingAtJoin time.Duration `json:"clock_remaining_at_join_ns" bson:"clock_remaining_at_join_ns"`
	GameDurationAtJoin   time.Duration `json:"game_duration_at_join_ns" bson:"game_duration_at_join_ns"`
	ScoresAtJoin         [2]int        `json:"scores_at_join" bson:"scores_at_join"`
	TeamSizesAtJoin      [2]int        `json:"team_sizes_at_join" bson:"team_sizes_at_join"`
	DisconnectsAtJoin    int           `json:"disconnects_at_join" bson:"disconnects_at_join"`
	DisadvantageAtJoin   int           `json:"disadvantage_at_join" bson:"disadvantage_at_join"`

	// Leave state (zero values if still present at match end)
	LeaveTime             time.Time     `json:"leave_time,omitempty" bson:"leave_time,omitempty"`
	ClockRemainingAtLeave time.Duration `json:"clock_remaining_at_leave_ns,omitempty" bson:"clock_remaining_at_leave_ns,omitempty"`
	GameDurationAtLeave   time.Duration `json:"game_duration_at_leave_ns,omitempty" bson:"game_duration_at_leave_ns,omitempty"`
	ScoresAtLeave         [2]int        `json:"scores_at_leave,omitempty" bson:"scores_at_leave,omitempty"`
	TeamSizesAtLeave      [2]int        `json:"team_sizes_at_leave,omitempty" bson:"team_sizes_at_leave,omitempty"`
	DisconnectsAtLeave    int           `json:"disconnects_at_leave,omitempty" bson:"disconnects_at_leave,omitempty"`

	// Computed during match
	IntegrityDuration    time.Duration `json:"integrity_duration_ns" bson:"integrity_duration_ns"`
	DisadvantageDuration time.Duration `json:"disadvantage_duration_ns" bson:"disadvantage_duration_ns"`

	// Hazard tracking
	TeamHazardEvents       int  `json:"team_hazard_events" bson:"team_hazard_events"`
	TeamConsecutiveHazards int  `json:"team_consecutive_hazards" bson:"team_consecutive_hazards"`
	IsTeamWipeMember       bool `json:"is_team_wipe_member,omitempty" bson:"is_team_wipe_member,omitempty"`

	// Final status
	WasPresentAtEnd bool `json:"was_present_at_end" bson:"was_present_at_end"`
	IsAbandoner     bool `json:"is_abandoner,omitempty" bson:"is_abandoner,omitempty"`
}

// MatchSummaryState captures the full final match state.
type MatchSummaryState struct {
	MatchID     string             `json:"match_id" bson:"_id"`
	Node        string             `json:"node" bson:"node"`
	Mode        string             `json:"mode" bson:"mode"`
	Level       string             `json:"level" bson:"level"`
	LobbyType   string             `json:"lobby_type" bson:"lobby_type"`
	GroupID     string             `json:"group_id,omitempty" bson:"group_id,omitempty"`
	OperatorID  string             `json:"operator_id,omitempty" bson:"operator_id,omitempty"`
	TeamSize    int                `json:"team_size,omitempty" bson:"team_size,omitempty"`
	PlayerLimit int                `json:"player_limit,omitempty" bson:"player_limit,omitempty"`
	MaxSize     int                `json:"max_size,omitempty" bson:"max_size,omitempty"`
	StartTime   time.Time          `json:"start_time,omitempty" bson:"start_time,omitempty"`
	EndTime     time.Time          `json:"end_time,omitempty" bson:"end_time,omitempty"`
	CreatedAt   time.Time          `json:"created_at,omitempty" bson:"created_at,omitempty"`
	FinalScores [2]int             `json:"final_scores" bson:"final_scores"`
	MatchOver   bool               `json:"match_over" bson:"match_over"`
	Scoreboard  *SessionScoreboard `json:"scoreboard,omitempty" bson:"scoreboard,omitempty"`

	// All players who participated at any point during the match
	Participants []PlayerParticipation `json:"participants" bson:"participants"`

	// Goal history for the match
	Goals []MatchGoalRecord `json:"goals,omitempty" bson:"goals,omitempty"`
}

// MatchGoalRecord stores goal information for replay/analysis.
type MatchGoalRecord struct {
	GoalTime float64 `json:"goal_time" bson:"goal_time"`
	TeamID   int     `json:"team_id" bson:"team_id"`
	GoalType string  `json:"goal_type" bson:"goal_type"`
	ScorerID string  `json:"scorer_id,omitempty" bson:"scorer_id,omitempty"`
}

// EventMatchSummary is emitted once at the end of a match with the full match state.
type EventMatchSummary struct {
	Match     MatchSummaryState `json:"match" bson:"match"`
	CreatedAt time.Time         `json:"created_at" bson:"created_at"`
}

// Process persists the match summary and disconnect view into MongoDB.
func (e *EventMatchSummary) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	if dispatcher.mongo == nil {
		logger.Warn("mongodb client not configured; skipping match summary insert")
		return nil
	}

	db := dispatcher.mongo.Database(matchDataDatabaseName)

	// Store the match summary
	summaryCollection := db.Collection(matchSummaryCollectionName)
	opts := options.Update().SetUpsert(true)
	filter := bson.M{"_id": e.Match.MatchID}
	update := bson.M{"$set": e.Match}
	if _, err := summaryCollection.UpdateOne(ctx, filter, update, opts); err != nil {
		return fmt.Errorf("failed to upsert match summary: %w", err)
	}

	// Generate and store the disconnect view for easy querying
	disconnects := GenerateDisconnectRecords(&e.Match)
	if len(disconnects) > 0 {
		disconnectCollection := db.Collection(matchDisconnectsCollectionName)

		// Delete existing disconnect records for this match before inserting new ones
		// This ensures we don't have duplicate key errors if the same match is processed multiple times
		filter := bson.M{"match_id": e.Match.MatchID}
		if _, err := disconnectCollection.DeleteMany(ctx, filter); err != nil {
			logger.WithField("error", err).Warn("failed to delete existing disconnect records")
		}

		docs := make([]interface{}, len(disconnects))
		for i, d := range disconnects {
			docs[i] = d
		}
		if _, err := disconnectCollection.InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("failed to insert disconnect records: %w", err)
		}
	}

	return nil
}

// DisconnectRecord is a view generated from the match state for easy querying.
// It's not stored in the match summary, but generated on demand or stored in a separate collection.
type DisconnectRecord struct {
	// Indexable fields
	MatchID string `json:"match_id" bson:"match_id"`
	UserID  string `json:"user_id" bson:"user_id"`

	// Player info
	Username    string    `json:"username,omitempty" bson:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty" bson:"display_name,omitempty"`
	Team        TeamIndex `json:"team" bson:"team"`
	PartyID     string    `json:"party_id,omitempty" bson:"party_id,omitempty"`
	PartySize   int       `json:"party_size" bson:"party_size"`
	SessionID   string    `json:"session_id,omitempty" bson:"session_id,omitempty"`

	// Timing
	PingBand   PingBand  `json:"ping_band" bson:"ping_band"`
	PingMillis int       `json:"ping_ms,omitempty" bson:"ping_ms,omitempty"`
	JoinTime   time.Time `json:"join_time" bson:"join_time"`
	LeaveTime  time.Time `json:"leave_time" bson:"leave_time"`

	// Status
	IsAbandoner     bool `json:"is_abandoner" bson:"is_abandoner"`
	WasPresentAtEnd bool `json:"was_present_at_end" bson:"was_present_at_end"`

	// Scores (from player's perspective: positive = winning)
	ScoresAtJoin      [2]int `json:"scores_at_join" bson:"scores_at_join"`
	ScoresAtLeave     [2]int `json:"scores_at_leave" bson:"scores_at_leave"`
	ScoreDeltaAtJoin  int    `json:"score_delta_at_join" bson:"score_delta_at_join"`
	ScoreDeltaAtLeave int    `json:"score_delta_at_leave" bson:"score_delta_at_leave"`
	WasLosingAtLeave  bool   `json:"was_losing_at_leave" bson:"was_losing_at_leave"`

	// Team state
	TeamSizesAtJoin         [2]int `json:"team_sizes_at_join" bson:"team_sizes_at_join"`
	TeamSizesAtLeave        [2]int `json:"team_sizes_at_leave" bson:"team_sizes_at_leave"`
	TeamDisadvantageAtJoin  int    `json:"team_disadvantage_at_join" bson:"team_disadvantage_at_join"`
	TeamDisadvantageAtLeave int    `json:"team_disadvantage_at_leave" bson:"team_disadvantage_at_leave"`

	// Disconnect tracking
	DisconnectsAtJoin  int `json:"disconnects_at_join" bson:"disconnects_at_join"`
	DisconnectsAtLeave int `json:"disconnects_at_leave" bson:"disconnects_at_leave"`

	// Hazard events
	TeamHazardEvents       int  `json:"team_hazard_events" bson:"team_hazard_events"`
	TeamConsecutiveHazards int  `json:"team_consecutive_hazards" bson:"team_consecutive_hazards"`
	IsTeamWipeMember       bool `json:"is_team_wipe_member" bson:"is_team_wipe_member"`

	// Duration tracking
	GameDurationAtJoin   time.Duration `json:"game_duration_at_join_ns" bson:"game_duration_at_join_ns"`
	GameDurationAtLeave  time.Duration `json:"game_duration_at_leave_ns" bson:"game_duration_at_leave_ns"`
	IntegrityDuration    time.Duration `json:"integrity_duration_ns" bson:"integrity_duration_ns"`
	DisadvantageDuration time.Duration `json:"disadvantage_duration_ns" bson:"disadvantage_duration_ns"`

	// Match context
	Mode      string    `json:"mode" bson:"mode"`
	Level     string    `json:"level" bson:"level"`
	LobbyType string    `json:"lobby_type" bson:"lobby_type"`
	GroupID   string    `json:"group_id,omitempty" bson:"group_id,omitempty"`
	MatchEnd  time.Time `json:"match_end" bson:"match_end"`
}

// GenerateDisconnectRecords creates disconnect records from the match state.
// This is a view function - the disconnect info is deterministic from the match state.
func GenerateDisconnectRecords(match *MatchSummaryState) []DisconnectRecord {
	if match == nil || len(match.Participants) == 0 {
		return nil
	}

	records := make([]DisconnectRecord, 0, len(match.Participants))

	for _, p := range match.Participants {
		// Calculate score deltas from player's team perspective
		scoreDeltaAtJoin := p.ScoresAtJoin[0] - p.ScoresAtJoin[1]
		scoreDeltaAtLeave := p.ScoresAtLeave[0] - p.ScoresAtLeave[1]
		teamDisadvantageAtJoin := p.TeamSizesAtJoin[0] - p.TeamSizesAtJoin[1]
		teamDisadvantageAtLeave := p.TeamSizesAtLeave[0] - p.TeamSizesAtLeave[1]
		wasLosingAtLeave := (p.Team == BlueTeam && p.ScoresAtLeave[0] < p.ScoresAtLeave[1]) ||
			(p.Team == OrangeTeam && p.ScoresAtLeave[1] < p.ScoresAtLeave[0])

		if p.Team == OrangeTeam {
			scoreDeltaAtJoin = -scoreDeltaAtJoin
			scoreDeltaAtLeave = -scoreDeltaAtLeave
			teamDisadvantageAtJoin = -teamDisadvantageAtJoin
			teamDisadvantageAtLeave = -teamDisadvantageAtLeave
		}

		leaveTime := p.LeaveTime
		if leaveTime.IsZero() {
			leaveTime = match.EndTime
		}

		record := DisconnectRecord{
			MatchID:                 match.MatchID,
			UserID:                  p.UserID,
			Username:                p.Username,
			DisplayName:             p.DisplayName,
			Team:                    p.Team,
			PartyID:                 p.PartyID,
			PartySize:               p.PartySize,
			SessionID:               p.SessionID,
			PingBand:                p.PingBand,
			PingMillis:              p.PingMillis,
			JoinTime:                p.JoinTime,
			LeaveTime:               leaveTime,
			IsAbandoner:             p.IsAbandoner,
			WasPresentAtEnd:         p.WasPresentAtEnd,
			ScoresAtJoin:            p.ScoresAtJoin,
			ScoresAtLeave:           p.ScoresAtLeave,
			ScoreDeltaAtJoin:        scoreDeltaAtJoin,
			ScoreDeltaAtLeave:       scoreDeltaAtLeave,
			WasLosingAtLeave:        wasLosingAtLeave,
			TeamSizesAtJoin:         p.TeamSizesAtJoin,
			TeamSizesAtLeave:        p.TeamSizesAtLeave,
			TeamDisadvantageAtJoin:  teamDisadvantageAtJoin,
			TeamDisadvantageAtLeave: teamDisadvantageAtLeave,
			DisconnectsAtJoin:       p.DisconnectsAtJoin,
			DisconnectsAtLeave:      p.DisconnectsAtLeave,
			TeamHazardEvents:        p.TeamHazardEvents,
			TeamConsecutiveHazards:  p.TeamConsecutiveHazards,
			IsTeamWipeMember:        p.IsTeamWipeMember,
			GameDurationAtJoin:      p.GameDurationAtJoin,
			GameDurationAtLeave:     p.GameDurationAtLeave,
			IntegrityDuration:       p.IntegrityDuration,
			DisadvantageDuration:    p.DisadvantageDuration,
			Mode:                    match.Mode,
			Level:                   match.Level,
			LobbyType:               match.LobbyType,
			GroupID:                 match.GroupID,
			MatchEnd:                match.EndTime,
		}
		records = append(records, record)
	}

	// Sort by leave time
	sort.Slice(records, func(i, j int) bool {
		return records[i].LeaveTime.Before(records[j].LeaveTime)
	})

	return records
}

// EnsureMatchSummaryIndexes creates the necessary indexes for efficient querying.
func EnsureMatchSummaryIndexes(ctx context.Context, db *mongo.Database) error {
	// Indexes for match_summaries collection
	summaryCollection := db.Collection(matchSummaryCollectionName)
	summaryIndexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "group_id", Value: 1}}},
		{Keys: bson.D{{Key: "mode", Value: 1}}},
		{Keys: bson.D{{Key: "end_time", Value: -1}}},
		{Keys: bson.D{{Key: "participants.user_id", Value: 1}}},
	}
	if _, err := summaryCollection.Indexes().CreateMany(ctx, summaryIndexes); err != nil {
		return fmt.Errorf("failed to create match summary indexes: %w", err)
	}

	// Indexes for match_disconnects_view collection
	disconnectCollection := db.Collection(matchDisconnectsCollectionName)
	disconnectIndexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "match_id", Value: 1}}},
		{Keys: bson.D{{Key: "user_id", Value: 1}}},
		{Keys: bson.D{{Key: "match_id", Value: 1}, {Key: "user_id", Value: 1}}},
		{Keys: bson.D{{Key: "leave_time", Value: -1}}},
		{Keys: bson.D{{Key: "is_abandoner", Value: 1}}},
		{Keys: bson.D{{Key: "group_id", Value: 1}}},
	}
	if _, err := disconnectCollection.Indexes().CreateMany(ctx, disconnectIndexes); err != nil {
		return fmt.Errorf("failed to create disconnect view indexes: %w", err)
	}

	return nil
}
