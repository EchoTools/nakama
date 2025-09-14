package server

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
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

func NewPlayerDisconnectInfo(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, state *MatchLabel, userID string) *PlayerDisconnectInfo {
	if state == nil || state.GameState == nil || state.GameState.RoundClock == nil {
		return nil
	}
	player := state.GetPlayerByUserID(userID)

	// Count how many players are in the same party
	partySize := 1
	if player.PartyID != "" {
		count, err := nk.StreamCount(StreamModeParty, player.PartyID, "", "")
		if err != nil {
			logger.Warn("Failed to get party size for player %s: %v", userID, err)
		}
		partySize = max(1, count)
	}

	// Count how many players have joined in total (i.e. they have a disconnect record)
	totalJoins := len(state.disconnectInfos)
	totalLeaves := totalJoins - state.GetPlayerCount()
	var teamID, otherTeamID = 1, 0
	if player.Team == BlueTeam {
		teamID = 0
		otherTeamID = 1
	}

	return &PlayerDisconnectInfo{
		PlayerInfo:           player,
		Team:                 player.Team,
		PartySize:            partySize,
		PingBand:             PingToBand(player.PingMillis),
		JoinTime:             time.Now(),
		ClockRemainingAtJoin: state.GameState.RoundClock.RemainingTime(),
		GameDurationAtJoin:   state.GameState.RoundClock.Elapsed(),
		ScoresAtJoin:         [2]int{state.GameState.BlueScore, state.GameState.OrangeScore},
		DisadvantageAtJoin:   state.RoleCount(teamID) - state.RoleCount(otherTeamID),
		DisconnectsAtJoin:    totalLeaves,
	}
}

type PlayerDisconnectInfo struct {
	PlayerInfo *PlayerInfo
	// The team of the player
	Team TeamIndex
	// The size of the party the player was in at the time of disconnection
	PartySize int
	// The player's ping band at the time of disconnection
	PingBand PingBand
	// The time the player connected
	JoinTime time.Time
	// The time the player disconnected
	LeaveTime time.Time
	// Is Abandoner (true) or Leaver (false)
	IsAbandoner bool
	// The time on the scoreboard clock when the player joined
	ClockRemainingAtJoin time.Duration
	// The time on the scoreboard clock when the player left
	ClockRemainingAtLeave time.Duration
	// The time in seconds from the start of the match when the player joined
	GameDurationAtJoin time.Duration
	// The time in seconds from the start of the match when the player left
	GameDurationAtLeave time.Duration
	// Time a team plays short-handed by ≥1 (DM) or ≥2 (DM2+) players.
	DisadvantageDuration time.Duration
	// Proportion of match time where both teams at full strength
	IntegrityDuration time.Duration
	// The game score at the time of join
	ScoresAtJoin [2]int
	// The game score at the time of disconnection
	ScoresAtLeave [2]int
	// The number of players unequal to the opposing team at the time of disconnection
	DisadvantageAtJoin int
	// The Team Sizes at the join time
	TeamSizesAtJoin [2]int
	// The number of players on the opposing team at the time of disconnection
	TeamSizesAtLeave [2]int
	// The count of the number of player disconnects on each team at the time of join
	DisconnectsAtJoin int
	// The count of the number of player disconnects on each team at the time of leave
	DisconnectsAtLeave int
	// The number of hazard events on the player's team at the time of disconnection
	TeamHazardEvents int
	// The number of consecutive hazard events on the player's team at the time of disconnection
	TeamConsecutiveHazards int
	// Was a member of group that left at similar time
	IsTeamWipeMember bool
}

func (p *PlayerDisconnectInfo) LeaveEvent(state *MatchLabel) {
	if state == nil || state.GameState == nil || state.GameState.RoundClock == nil {
		return
	}
	p.IsAbandoner = !state.GameState.MatchOver
	p.LeaveTime = time.Now()
	p.ClockRemainingAtLeave = state.GameState.RoundClock.RemainingTime()
	p.GameDurationAtLeave = state.GameState.RoundClock.Elapsed()
	p.ScoresAtLeave = [2]int{state.GameState.BlueScore, state.GameState.OrangeScore}
	p.TeamSizesAtLeave = [2]int{state.RoleCount(0), state.RoleCount(1)}

	totalLeaves := len(state.disconnectInfos) - state.GetPlayerCount()
	// Calculate how many players have left since this player joined
	p.DisconnectsAtLeave = totalLeaves - p.DisconnectsAtJoin

	for _, info := range state.disconnectInfos {
		if info.PlayerInfo.UserID != p.PlayerInfo.UserID && info.Team == p.PlayerInfo.Team && time.Since(info.LeaveTime) < 10*time.Second {
			p.IsTeamWipeMember = true
			break
		}
	}
	minTime := float64(time.Since(p.JoinTime.Add(-15*time.Second)) / time.Second)

	p.TeamHazardEvents, p.TeamConsecutiveHazards = calculateHazardEvents(state, int(p.Team), minTime)
}

func (p *PlayerDisconnectInfo) LogEarlyQuitMetrics(nk runtime.NakamaModule, state *MatchLabel) {
	if state == nil || state.GameState == nil || state.GameState.RoundClock == nil {
		return
	}

	// Check if the player left at the same time as another player on their team
	wasLosingAtLeave := (p.Team == BlueTeam && p.ScoresAtLeave[1] > p.ScoresAtLeave[0])
	scoreDeltaAtJoin := p.ScoresAtJoin[0] - p.ScoresAtJoin[1]
	scoreDeltaAtLeave := p.ScoresAtLeave[0] - p.ScoresAtLeave[1]
	teamDisadvantageAtJoin := p.TeamSizesAtJoin[0] - p.TeamSizesAtJoin[1]
	teamDisadvantageAtLeave := p.TeamSizesAtLeave[0] - p.TeamSizesAtLeave[1]
	if p.Team == OrangeTeam {
		teamDisadvantageAtJoin = -teamDisadvantageAtJoin
		teamDisadvantageAtLeave = -teamDisadvantageAtLeave
		scoreDeltaAtJoin = -scoreDeltaAtJoin
		scoreDeltaAtLeave = -scoreDeltaAtLeave
	}

	// Log metrics
	tags := map[string]string{
		"is_abandon":                 fmt.Sprintf("%t", p.IsAbandoner),
		"team":                       p.Team.String(),
		"was_losing_at_leave":        fmt.Sprintf("%t", wasLosingAtLeave),
		"is_team_wipe_member":        fmt.Sprintf("%t", p.IsTeamWipeMember),
		"score_delta_at_join":        fmt.Sprintf("%d", scoreDeltaAtJoin),
		"score_delta_at_leave":       fmt.Sprintf("%d", scoreDeltaAtLeave),
		"team_disadvantage_at_join":  fmt.Sprintf("%d", teamDisadvantageAtJoin),
		"team_disadvantage_at_leave": fmt.Sprintf("%d", teamDisadvantageAtLeave),
		"total_hazard_events":        fmt.Sprintf("%d", p.TeamHazardEvents),
		"consecutive_hazard_events":  fmt.Sprintf("%d", p.TeamConsecutiveHazards),
		"party_size":                 fmt.Sprintf("%d", p.PartySize),
		"ping_band":                  string(p.PingBand),
		"disconnects_at_join":        fmt.Sprintf("%d", p.DisconnectsAtJoin),
		"disconnects_at_leave":       fmt.Sprintf("%d", p.DisconnectsAtLeave),
		"integrity_duration":         fmt.Sprintf("%v", p.IntegrityDuration),
	}

	nk.MetricsCounterAdd("match_abandon_total", tags, 1)
	nk.MetricsTimerRecord("match_abandon_game_duration_at_join_seconds", tags, p.GameDurationAtJoin)
	nk.MetricsTimerRecord("match_abandon_game_duration_at_leave_seconds", tags, p.GameDurationAtLeave)
	nk.MetricsTimerRecord("match_abandon_session_duration_seconds", tags, p.LeaveTime.Sub(p.JoinTime))
	nk.MetricsTimerRecord("match_abandon_integrity_duration_seconds", tags, p.IntegrityDuration)
	nk.MetricsTimerRecord("match_abandon_disadvantage_duration_seconds", tags, p.DisadvantageDuration)

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
	items := make([]index, 0, len(state.goals)+len(state.disconnectInfos))
	for _, goal := range state.goals {
		if goal.GoalTime < minGameTime {
			continue
		}
		items = append(items, index{ts: goal.GoalTime, team: int(goal.TeamID)})
	}
	for _, info := range state.disconnectInfos {
		if info.GameDurationAtLeave < time.Duration(minGameTime/float64(time.Second)) {
			continue
		}
		items = append(items, index{ts: float64(info.GameDurationAtLeave) / float64(time.Second), team: int(info.Team)})
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
