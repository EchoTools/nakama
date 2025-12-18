package server

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type PingBand string

const (
	PingBandGreat PingBand = "<40ms"
	PingBandGood  PingBand = "40-80ms"
	PingBandOkay  PingBand = "80-120ms"
	PingBandBad   PingBand = ">120ms"
)

func PingToBand(ping int) PingBand {
	switch {
	case ping < 40:
		return PingBandGreat
	case ping < 80:
		return PingBandGood
	case ping < 120:
		return PingBandOkay
	default:
		return PingBandBad
	}
}

// NewPlayerParticipation creates a new participation record when a player joins a match.
func NewPlayerParticipation(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, state *MatchLabel, userID string) *PlayerParticipation {
	if state == nil {
		return nil
	}
	player := state.GetPlayerByUserID(userID)
	if player == nil {
		return nil
	}

	node := ctx.Value(runtime.RUNTIME_CTX_MATCH_NODE).(string)
	// Count how many players are in the same party
	partySize := 1
	if player.PartyID != "" {
		count, err := nk.StreamCount(StreamModeParty, player.PartyID, "", node)
		if err != nil {
			logger.Warn("Failed to get party size for player %s: %v", userID, err)
		}
		partySize = max(1, count)
	}

	// Count how many players have joined in total and left
	totalLeaves := 0
	for _, p := range state.participations {
		if p == nil {
			continue
		}
		if !p.LeaveTime.IsZero() {
			totalLeaves++
		}
	}

	var teamID, otherTeamID = 1, 0
	if player.Team == BlueTeam {
		teamID = 0
		otherTeamID = 1
	}

	participation := &PlayerParticipation{
		UserID:             player.UserID,
		Username:           player.Username,
		DisplayName:        player.DisplayName,
		DiscordID:          player.DiscordID,
		SessionID:          player.SessionID,
		PartyID:            player.PartyID,
		Team:               player.Team,
		PartySize:          partySize,
		PingBand:           PingToBand(player.PingMillis),
		PingMillis:         player.PingMillis,
		JoinTime:           time.Now(),
		DisadvantageAtJoin: state.RoleCount(teamID) - state.RoleCount(otherTeamID),
		TeamSizesAtJoin:    [2]int{state.RoleCount(0), state.RoleCount(1)},
		DisconnectsAtJoin:  totalLeaves,
	}

	// Add game state info if available
	if state.GameState != nil {
		participation.ScoresAtJoin = [2]int{state.GameState.BlueScore, state.GameState.OrangeScore}
		if state.GameState.SessionScoreboard != nil {
			participation.ClockRemainingAtJoin = state.GameState.SessionScoreboard.RemainingTime()
			participation.GameDurationAtJoin = state.GameState.SessionScoreboard.Elapsed()
		}
	}

	return participation
}

// RecordLeaveEvent updates a participation record when a player leaves.
func (p *PlayerParticipation) RecordLeaveEvent(state *MatchLabel) {
	if p == nil {
		return
	}

	matchOver := false
	if state != nil && state.GameState != nil {
		matchOver = state.GameState.MatchOver
		p.ScoresAtLeave = [2]int{state.GameState.BlueScore, state.GameState.OrangeScore}
		if state.GameState.SessionScoreboard != nil {
			p.ClockRemainingAtLeave = state.GameState.SessionScoreboard.RemainingTime()
			p.GameDurationAtLeave = state.GameState.SessionScoreboard.Elapsed()
		}
	}

	p.IsAbandoner = !matchOver
	p.LeaveTime = time.Now()

	// If state is nil, we cannot safely access team sizes, participations, or hazard events.
	if state == nil {
		return
	}

	p.TeamSizesAtLeave = [2]int{state.RoleCount(0), state.RoleCount(1)}
	p.WasPresentAtEnd = matchOver

	// Count disconnects since join (excluding the current player who just left)
	totalLeaves := 0
	for _, info := range state.participations {
		if info == nil {
			continue
		}
		if !info.LeaveTime.IsZero() {
			totalLeaves++
		}
	}
	// Subtract 1 because the current player is already counted in totalLeaves
	// Use max to prevent negative values in edge cases
	p.DisconnectsAtLeave = max(0, totalLeaves-p.DisconnectsAtJoin-1)

	// Check if this is part of a team wipe (multiple players leaving around the same time)
	for _, info := range state.participations {
		if info == nil {
			continue
		}
		if info.UserID != p.UserID && info.Team == p.Team && !info.LeaveTime.IsZero() && time.Since(info.LeaveTime) < 10*time.Second {
			p.IsTeamWipeMember = true
			break
		}
	}

	minTime := float64(time.Since(p.JoinTime.Add(-15*time.Second)) / time.Second)
	p.TeamHazardEvents, p.TeamConsecutiveHazards = calculateHazardEvents(state, int(p.Team), minTime)
}

// FinalizeParticipation marks a player as present at match end if they haven't left.
func (p *PlayerParticipation) FinalizeParticipation(state *MatchLabel) {
	if p == nil {
		return
	}

	// If player never left, they were present at end
	if p.LeaveTime.IsZero() {
		p.WasPresentAtEnd = true
		p.LeaveTime = time.Now()

		if state != nil && state.GameState != nil {
			p.ScoresAtLeave = [2]int{state.GameState.BlueScore, state.GameState.OrangeScore}
			if state.GameState.SessionScoreboard != nil {
				p.ClockRemainingAtLeave = state.GameState.SessionScoreboard.RemainingTime()
				p.GameDurationAtLeave = state.GameState.SessionScoreboard.Elapsed()
			}
		}
		if state != nil {
			p.TeamSizesAtLeave = [2]int{state.RoleCount(0), state.RoleCount(1)}
		}
	}
}

// BuildMatchSummary creates the final match summary from the match state.
func BuildMatchSummary(state *MatchLabel) *EventMatchSummary {
	if state == nil || len(state.participations) == 0 {
		return nil
	}

	// Collect all participants
	participants := make([]PlayerParticipation, 0, len(state.participations))
	for _, p := range state.participations {
		if p == nil {
			continue
		}
		// Finalize any participants who are still present
		p.FinalizeParticipation(state)
		participants = append(participants, *p)
	}

	if len(participants) == 0 {
		return nil
	}

	// Sort by join time
	sort.Slice(participants, func(i, j int) bool {
		return participants[i].JoinTime.Before(participants[j].JoinTime)
	})

	// Build goal records
	goals := make([]MatchGoalRecord, 0, len(state.goals))
	for _, g := range state.goals {
		goals = append(goals, MatchGoalRecord{
			GoalTime: g.GoalTime,
			TeamID:   int(g.TeamID),
			GoalType: g.GoalType,
		})
	}

	summary := MatchSummaryState{
		MatchID:      state.ID.String(),
		Node:         state.ID.Node,
		Mode:         state.Mode.String(),
		Level:        state.Level.String(),
		LobbyType:    state.LobbyType.String(),
		GroupID:      state.GetGroupID().String(),
		TeamSize:     state.TeamSize,
		PlayerLimit:  state.PlayerLimit,
		MaxSize:      state.MaxSize,
		StartTime:    state.StartTime,
		EndTime:      time.Now().UTC(),
		CreatedAt:    state.CreatedAt,
		Participants: participants,
		Goals:        goals,
	}

	if state.GameServer != nil {
		summary.OperatorID = state.GameServer.OperatorID.String()
	}

	if gs := state.GameState; gs != nil {
		summary.FinalScores = [2]int{gs.BlueScore, gs.OrangeScore}
		summary.MatchOver = gs.MatchOver
		if sb := gs.SessionScoreboard; sb != nil {
			summary.Scoreboard = sb.LatestAsNewScoreboard()
		}
	}

	return &EventMatchSummary{
		Match:     summary,
		CreatedAt: time.Now().UTC(),
	}
}

// SendMatchSummary sends the match summary event.
func SendMatchSummary(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, state *MatchLabel) error {
	event := BuildMatchSummary(state)
	if event == nil {
		return nil
	}

	if err := SendEvent(ctx, nk, event); err != nil {
		logger.WithField("error", err).Error("failed to send match summary event")
		return err
	}
	return nil
}

func calculateHazardEvents(state *MatchLabel, teamID int, minGameTime float64) (total int, consecutive int) {
	// Calculate all the consecutive goals by one team, and player disconnects for a specific team
	type index struct {
		ts   float64
		team int
	}

	// The point delta is the points that the other team is ahead.
	losingByDelta := 0
	for _, goal := range state.goals {
		if goal.TeamID != int64(teamID) {
			losingByDelta += GoalTypeToPoints(goal.GoalType)
			continue
		}
		losingByDelta -= GoalTypeToPoints(goal.GoalType)
	}

	// If the player joined while their team was losing by 3 or more points, count that as a hazard event
	if losingByDelta >= 3 {
		total++
		consecutive++
	}

	// Create a combined list of goals and disconnects with timestamps
	items := make([]index, 0, len(state.goals)+len(state.participations))
	for _, goal := range state.goals {
		if goal.GoalTime < minGameTime {
			continue
		}
		items = append(items, index{ts: goal.GoalTime, team: int(goal.TeamID)})
	}
	for _, info := range state.participations {
		if info == nil {
			continue
		}
		if info.GameDurationAtLeave.Seconds() < minGameTime {
			continue
		}
		items = append(items, index{ts: float64(info.GameDurationAtLeave.Milliseconds()) / 1000, team: int(info.Team)})
	}

	// Sort by timestamp
	sort.Slice(items, func(i, j int) bool {
		return items[i].ts < items[j].ts
	})

	// Count total and consecutive events against this team.
	for _, item := range items {
		if item.team == teamID {
			total++
			consecutive++

		} else {
			consecutive = 0
		}
	}
	return total, consecutive
}

// UpdateParticipationDurations updates the integrity and disadvantage durations for all participants.
// This should be called periodically during the match.
func UpdateParticipationDurations(state *MatchLabel, delta time.Duration) {
	if state == nil {
		return
	}

	// Calculate current team state
	var integrityTime, disadvantageBlueTime, disadvantageOrangeTime time.Duration

	if state.OpenPlayerSlots() == 0 {
		integrityTime = delta
	} else if state.RoleCount(evr.TeamBlue) < state.RoleCount(evr.TeamOrange) {
		disadvantageBlueTime = delta
	} else if state.RoleCount(evr.TeamOrange) < state.RoleCount(evr.TeamBlue) {
		disadvantageOrangeTime = delta
	}

	for _, p := range state.participations {
		if p == nil {
			continue
		}
		// Only update for players still in the match
		if !p.LeaveTime.IsZero() {
			continue
		}
		p.IntegrityDuration += integrityTime

		if p.Team == BlueTeam {
			p.DisadvantageDuration += disadvantageBlueTime
		}
		if p.Team == OrangeTeam {
			p.DisadvantageDuration += disadvantageOrangeTime
		}
	}
}
