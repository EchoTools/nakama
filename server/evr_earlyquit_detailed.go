package server

import (
	"context"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEarlyQuitHistory = "EarlyQuitHistory"
	StorageKeyEarlyQuitHistory        = "history"
)

// QuitType represents the type of quit
type QuitType string

const (
	QuitTypeEarly   QuitType = "early"   // Left during active gameplay
	QuitTypePregame QuitType = "pregame" // Left before game started
)

// CompletionRecord represents a completed match (no early quit)
type CompletionRecord struct {
	MatchID        MatchID   `json:"match_id"`
	CompletionTime time.Time `json:"completion_time"`
}

// QuitRecord represents a single early quit event with full context
type QuitRecord struct {
	// Event info
	MatchID    MatchID   `json:"match_id"`
	QuitTime   time.Time `json:"quit_time"`
	QuitType   QuitType  `json:"quit_type"`
	Forgiven   bool      `json:"forgiven,omitempty"`    // True if quit was forgiven due to full logout
	ForgivenAt time.Time `json:"forgiven_at,omitempty"` // When the quit was forgiven

	// Player info at time of quit
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	Team        TeamIndex `json:"team"`
	SessionID   string    `json:"session_id,omitempty"`

	// Join state
	JoinTime             time.Time     `json:"join_time"`
	ClockRemainingAtJoin time.Duration `json:"clock_remaining_at_join_ns"`
	GameDurationAtJoin   time.Duration `json:"game_duration_at_join_ns"`
	ScoresAtJoin         [2]int        `json:"scores_at_join"`
	TeamSizesAtJoin      [2]int        `json:"team_sizes_at_join"`

	// Quit state
	ClockRemainingAtQuit time.Duration `json:"clock_remaining_at_quit_ns"`
	GameDurationAtQuit   time.Duration `json:"game_duration_at_quit_ns"`
	ScoresAtQuit         [2]int        `json:"scores_at_quit"`
	TeamSizesAtQuit      [2]int        `json:"team_sizes_at_quit"`
	DisconnectsAtQuit    int           `json:"disconnects_at_quit"`

	// Match context
	Mode      string `json:"mode"`
	Level     string `json:"level,omitempty"`
	LobbyType string `json:"lobby_type"`
	GroupID   string `json:"group_id,omitempty"`

	// Additional context
	PartyID         string   `json:"party_id,omitempty"`
	PartySize       int      `json:"party_size"`
	PingBand        PingBand `json:"ping_band"`
	WasLosingAtQuit bool     `json:"was_losing_at_quit"`

	// Hazard tracking
	TeamHazardEvents       int  `json:"team_hazard_events"`
	TeamConsecutiveHazards int  `json:"team_consecutive_hazards"`
	IsTeamWipeMember       bool `json:"is_team_wipe_member,omitempty"`
}

// EarlyQuitHistory tracks detailed quit history for a player
type EarlyQuitHistory struct {
	UserID      string             `json:"user_id"`
	Records     []QuitRecord       `json:"records"`
	Completions []CompletionRecord `json:"completions"` // Track completed matches with timestamps

	version string
}

func NewEarlyQuitHistory(userID string) *EarlyQuitHistory {
	return &EarlyQuitHistory{
		UserID:      userID,
		Records:     make([]QuitRecord, 0),
		Completions: make([]CompletionRecord, 0),
	}
}

func (h *EarlyQuitHistory) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionEarlyQuitHistory,
		Key:             StorageKeyEarlyQuitHistory,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         h.version,
	}
}

func (h *EarlyQuitHistory) SetStorageMeta(meta StorableMetadata) {
	h.version = meta.Version
}

func (h *EarlyQuitHistory) GetStorageVersion() string {
	return h.version
}

// AddQuitRecord adds a new quit record
func (h *EarlyQuitHistory) AddQuitRecord(record QuitRecord) {
	h.Records = append(h.Records, record)
}

// AddCompletion adds a new completion record
func (h *EarlyQuitHistory) AddCompletion(matchID MatchID, completionTime time.Time) {
	h.Completions = append(h.Completions, CompletionRecord{
		MatchID:        matchID,
		CompletionTime: completionTime,
	})
}

// ForgiveQuit marks a quit as forgiven (player logged out completely)
func (h *EarlyQuitHistory) ForgiveQuit(matchID MatchID) bool {
	// Find the most recent unforgiven quit for this match
	for i := len(h.Records) - 1; i >= 0; i-- {
		if h.Records[i].MatchID.UUID == matchID.UUID && !h.Records[i].Forgiven {
			h.Records[i].Forgiven = true
			h.Records[i].ForgivenAt = time.Now().UTC()
			return true
		}
	}
	return false
}

// GetUnforgivenQuitsSince returns unforgiven quits since the given time
func (h *EarlyQuitHistory) GetUnforgivenQuitsSince(since time.Time) []QuitRecord {
	var quits []QuitRecord
	for _, record := range h.Records {
		if !record.Forgiven && record.QuitTime.After(since) {
			quits = append(quits, record)
		}
	}
	return quits
}

// GetRecentQuits returns all quits (forgiven or not) within the given duration
func (h *EarlyQuitHistory) GetRecentQuits(duration time.Duration) []QuitRecord {
	since := time.Now().Add(-duration)
	var quits []QuitRecord
	for _, record := range h.Records {
		if record.QuitTime.After(since) {
			quits = append(quits, record)
		}
	}
	return quits
}

// GetRecentCompletions returns completions within the given duration
func (h *EarlyQuitHistory) GetRecentCompletions(duration time.Duration) []CompletionRecord {
	since := time.Now().Add(-duration)
	var completions []CompletionRecord
	for _, record := range h.Completions {
		if record.CompletionTime.After(since) {
			completions = append(completions, record)
		}
	}
	return completions
}

// CountUnforgivenQuits returns the count of unforgiven quits
func (h *EarlyQuitHistory) CountUnforgivenQuits() int {
	count := 0
	for _, record := range h.Records {
		if !record.Forgiven {
			count++
		}
	}
	return count
}

// CountQuitsByType returns counts of quits by type (optionally only unforgiven)
func (h *EarlyQuitHistory) CountQuitsByType(unforgivenOnly bool) (early, pregame int) {
	for _, record := range h.Records {
		if unforgivenOnly && record.Forgiven {
			continue
		}
		switch record.QuitType {
		case QuitTypeEarly:
			early++
		case QuitTypePregame:
			pregame++
		}
	}
	return
}

// GetQuitRate returns the quit rate based on unforgiven quits
// Returns quits / (quits + completed matches from EarlyQuitConfig)
func (h *EarlyQuitHistory) GetQuitRate(completedMatches int32) float64 {
	quits := h.CountUnforgivenQuits()
	total := quits + int(completedMatches)
	if total == 0 {
		return 0.0
	}
	return float64(quits) / float64(total)
}

// PruneOldRecords removes records older than the given duration
// Returns the number of records removed (quits, completions)
func (h *EarlyQuitHistory) PruneOldRecords(maxAge time.Duration) (int, int) {
	cutoff := time.Now().Add(-maxAge)
	originalQuitLen := len(h.Records)
	originalCompletionLen := len(h.Completions)

	// Keep only quit records newer than cutoff
	keptQuits := make([]QuitRecord, 0, originalQuitLen)
	for _, record := range h.Records {
		if record.QuitTime.After(cutoff) {
			keptQuits = append(keptQuits, record)
		}
	}

	// Keep only completion records newer than cutoff
	keptCompletions := make([]CompletionRecord, 0, originalCompletionLen)
	for _, record := range h.Completions {
		if record.CompletionTime.After(cutoff) {
			keptCompletions = append(keptCompletions, record)
		}
	}

	h.Records = keptQuits
	h.Completions = keptCompletions
	return originalQuitLen - len(keptQuits), originalCompletionLen - len(keptCompletions)
}

// CreateQuitRecordFromParticipation creates a QuitRecord from PlayerParticipation and match state
func CreateQuitRecordFromParticipation(state *MatchLabel, participation *PlayerParticipation) QuitRecord {
	if state == nil || participation == nil {
		return QuitRecord{}
	}

	// Determine quit type based on game duration
	quitType := QuitTypeEarly
	if participation.GameDurationAtLeave < 30*time.Second {
		quitType = QuitTypePregame
	}

	// Determine if player was losing at quit
	wasLosing := false
	if participation.Team == BlueTeam {
		wasLosing = participation.ScoresAtLeave[0] < participation.ScoresAtLeave[1]
	} else if participation.Team == OrangeTeam {
		wasLosing = participation.ScoresAtLeave[1] < participation.ScoresAtLeave[0]
	}

	return QuitRecord{
		MatchID:  state.ID,
		QuitTime: participation.LeaveTime,
		QuitType: quitType,
		Forgiven: false,

		Username:    participation.Username,
		DisplayName: participation.DisplayName,
		Team:        participation.Team,
		SessionID:   participation.SessionID,

		JoinTime:             participation.JoinTime,
		ClockRemainingAtJoin: participation.ClockRemainingAtJoin,
		GameDurationAtJoin:   participation.GameDurationAtJoin,
		ScoresAtJoin:         participation.ScoresAtJoin,
		TeamSizesAtJoin:      participation.TeamSizesAtJoin,

		ClockRemainingAtQuit: participation.ClockRemainingAtLeave,
		GameDurationAtQuit:   participation.GameDurationAtLeave,
		ScoresAtQuit:         participation.ScoresAtLeave,
		TeamSizesAtQuit:      participation.TeamSizesAtLeave,
		DisconnectsAtQuit:    participation.DisconnectsAtLeave,

		Mode:      state.Mode.String(),
		Level:     state.Level.String(),
		LobbyType: state.LobbyType.String(),
		GroupID:   state.GetGroupID().String(),

		PartyID:         participation.PartyID,
		PartySize:       participation.PartySize,
		PingBand:        participation.PingBand,
		WasLosingAtQuit: wasLosing,

		TeamHazardEvents:       participation.TeamHazardEvents,
		TeamConsecutiveHazards: participation.TeamConsecutiveHazards,
		IsTeamWipeMember:       participation.IsTeamWipeMember,
	}
}

// TrackMatchCompletion tracks a match completion in the player's early quit history
// This is a helper function to reduce code duplication
func TrackMatchCompletion(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID string, matchID MatchID, completionTime time.Time) error {
	history := NewEarlyQuitHistory(userID)
	if err := StorableRead(ctx, nk, userID, history, false); err != nil {
		// Align behavior with quit tracking: log but proceed with a new history so completions are still tracked.
		logger.WithField("error", err).Warn("Failed to load early quit history for completion tracking, proceeding with new history")
	}

	history.AddCompletion(matchID, completionTime)
	if err := StorableWrite(ctx, nk, userID, history); err != nil {
		logger.WithField("error", err).Warn("Failed to write completion to early quit history")
		return err
	}

	return nil
}
