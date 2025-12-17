package server

import (
	"context"
	"database/sql"
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
	if state == nil || state.GameState == nil || state.GameState.SessionScoreboard == nil {
		return nil
	}
	player := state.GetPlayerByUserID(userID)

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
		ClockRemainingAtJoin: state.GameState.SessionScoreboard.RemainingTime(),
		GameDurationAtJoin:   state.GameState.SessionScoreboard.Elapsed(),
		ScoresAtJoin:         [2]int{state.GameState.BlueScore, state.GameState.OrangeScore},
		DisadvantageAtJoin:   state.RoleCount(teamID) - state.RoleCount(otherTeamID),
		TeamSizesAtJoin:      [2]int{state.RoleCount(0), state.RoleCount(1)},
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
	if p == nil {
		return
	}
	if state == nil || state.GameState == nil || state.GameState.SessionScoreboard == nil {
		return
	}
	p.IsAbandoner = !state.GameState.MatchOver
	p.LeaveTime = time.Now()
	p.ClockRemainingAtLeave = state.GameState.SessionScoreboard.RemainingTime()
	p.GameDurationAtLeave = state.GameState.SessionScoreboard.Elapsed()
	p.ScoresAtLeave = [2]int{state.GameState.BlueScore, state.GameState.OrangeScore}
	p.TeamSizesAtLeave = [2]int{state.RoleCount(0), state.RoleCount(1)}

	totalLeaves := len(state.disconnectInfos) - state.GetPlayerCount()
	// Calculate how many players have left since this player joined
	p.DisconnectsAtLeave = totalLeaves - p.DisconnectsAtJoin

	for _, info := range state.disconnectInfos {
		if info == nil || info.PlayerInfo == nil {
			continue
		}
		if info.PlayerInfo.UserID != p.PlayerInfo.UserID && info.Team == p.Team && time.Since(info.LeaveTime) < 10*time.Second {
			p.IsTeamWipeMember = true
			break
		}
	}
	minTime := float64(time.Since(p.JoinTime.Add(-15*time.Second)) / time.Second)

	p.TeamHazardEvents, p.TeamConsecutiveHazards = calculateHazardEvents(state, int(p.Team), minTime)
}

func (p *PlayerDisconnectInfo) LogEarlyQuitMetrics(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, state *MatchLabel) {
	if state == nil {
		return
	}

	event := buildMatchDisconnectEvent(state)
	if event == nil {
		return
	}

	if err := SendEvent(ctx, nk, event); err != nil {
		logger.WithField("error", err).Error("failed to send match disconnect event")
	}

}

func (p *PlayerDisconnectInfo) toRecord(state *MatchLabel) PlayerDisconnectRecord {
	if p == nil || p.PlayerInfo == nil {
		return PlayerDisconnectRecord{}
	}

	scoreDeltaAtJoin := p.ScoresAtJoin[0] - p.ScoresAtJoin[1]
	scoreDeltaAtLeave := p.ScoresAtLeave[0] - p.ScoresAtLeave[1]
	teamDisadvantageAtJoin := p.TeamSizesAtJoin[0] - p.TeamSizesAtJoin[1]
	teamDisadvantageAtLeave := p.TeamSizesAtLeave[0] - p.TeamSizesAtLeave[1]
	wasLosingAtLeave := (p.Team == BlueTeam && p.ScoresAtLeave[0] < p.ScoresAtLeave[1]) || (p.Team == OrangeTeam && p.ScoresAtLeave[1] < p.ScoresAtLeave[0])

	if p.Team == OrangeTeam {
		scoreDeltaAtJoin = -scoreDeltaAtJoin
		scoreDeltaAtLeave = -scoreDeltaAtLeave
		teamDisadvantageAtJoin = -teamDisadvantageAtJoin
		teamDisadvantageAtLeave = -teamDisadvantageAtLeave
	}

	return PlayerDisconnectRecord{
		UserID:                  p.PlayerInfo.UserID,
		Username:                p.PlayerInfo.Username,
		DisplayName:             p.PlayerInfo.DisplayName,
		Team:                    p.Team,
		PartyID:                 p.PlayerInfo.PartyID,
		PartySize:               p.PartySize,
		PingBand:                p.PingBand,
		PingMillis:              p.PlayerInfo.PingMillis,
		JoinTime:                p.JoinTime.UTC(),
		LeaveTime:               p.LeaveTime.UTC(),
		IsAbandoner:             p.IsAbandoner,
		ScoresAtJoin:            p.ScoresAtJoin,
		ScoresAtLeave:           p.ScoresAtLeave,
		TeamSizesAtJoin:         p.TeamSizesAtJoin,
		TeamSizesAtLeave:        p.TeamSizesAtLeave,
		ScoreDeltaAtJoin:        scoreDeltaAtJoin,
		ScoreDeltaAtLeave:       scoreDeltaAtLeave,
		TeamDisadvantageAtJoin:  teamDisadvantageAtJoin,
		TeamDisadvantageAtLeave: teamDisadvantageAtLeave,
		WasLosingAtLeave:        wasLosingAtLeave,
		DisconnectsAtJoin:       p.DisconnectsAtJoin,
		DisconnectsAtLeave:      p.DisconnectsAtLeave,
		TeamHazardEvents:        p.TeamHazardEvents,
		TeamConsecutiveHazards:  p.TeamConsecutiveHazards,
		IsTeamWipeMember:        p.IsTeamWipeMember,
		GameDurationAtJoin:      p.GameDurationAtJoin,
		GameDurationAtLeave:     p.GameDurationAtLeave,
		IntegrityDuration:       p.IntegrityDuration,
		DisadvantageDuration:    p.DisadvantageDuration,
		SessionID:               p.PlayerInfo.SessionID,
	}
}

func buildMatchDisconnectEvent(state *MatchLabel) *EventMatchDisconnect {
	if state == nil || len(state.disconnectInfos) == 0 {
		return nil
	}

	disconnects := make([]PlayerDisconnectRecord, 0, len(state.disconnectInfos))
	for _, info := range state.disconnectInfos {
		if info == nil || info.PlayerInfo == nil {
			continue
		}
		disconnects = append(disconnects, info.toRecord(state))
	}

	if len(disconnects) == 0 {
		return nil
	}

	sort.Slice(disconnects, func(i, j int) bool {
		return disconnects[i].LeaveTime.Before(disconnects[j].LeaveTime)
	})

	snapshot := MatchDisconnectSnapshot{
		MatchID:     state.ID.String(),
		Node:        state.ID.Node,
		Mode:        state.Mode.String(),
		Level:       state.Level.String(),
		LobbyType:   state.LobbyType.String(),
		GroupID:     state.GetGroupID().String(),
		TeamSize:    state.TeamSize,
		PlayerLimit: state.PlayerLimit,
		MaxSize:     state.MaxSize,
		PlayerCount: state.PlayerCount,
		StartTime:   state.StartTime,
		CreatedAt:   state.CreatedAt,
		Players:     append([]PlayerInfo(nil), state.Players...),
	}

	if state.GameServer != nil {
		snapshot.OperatorID = state.GameServer.OperatorID.String()
	}

	if gs := state.GameState; gs != nil {
		snapshot.FinalScores = [2]int{gs.BlueScore, gs.OrangeScore}
		snapshot.MatchOver = gs.MatchOver
		if sb := gs.SessionScoreboard; sb != nil {
			snapshot.Scoreboard = sb.LatestAsNewScoreboard()
		}
	}

	return &EventMatchDisconnect{
		Match:       snapshot,
		Disconnects: disconnects,
		CreatedAt:   time.Now().UTC(),
	}
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
		if info == nil || info.PlayerInfo == nil {
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
