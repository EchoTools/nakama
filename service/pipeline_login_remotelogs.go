package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"go.uber.org/zap"
)

var remoteLogFilters = func() []string {
	filters := map[string][]string{
		"message": {
			"Podium Interaction",
			"Customization Item Preview",
			"Customization Item Equip",
			"Confirmation Panel Press",
			"server library loaded",
			"r15 net game error message",
			"cst_usage_metrics",
			"purchasing item",
			"Tutorial progress",
		},
		"category": {
			"iap",
			"rich_presence",
			"social",
		},
		"message_type": {
			"OVR_IAP",
		},
	}

	filterStrings := make([]string, 0, len(filters))
	for key, values := range filters {
		for _, value := range values {
			filterStrings = append(filterStrings, fmt.Sprintf(`"%s":"%s"`, key, value))
		}
	}

	return filterStrings
}()

func filterRemoteLogs(logs []string) []string {
	filteredLogs := logs[:0]
	for _, log := range logs {
		shouldFilter := false
		for _, filter := range remoteLogFilters {
			if strings.Contains(log, filter) {
				shouldFilter = true
				break
			}
		}
		if !shouldFilter {
			filteredLogs = append(filteredLogs, log)
		}
	}
	return filteredLogs
}

func (p *Pipeline) remoteLogSetv3(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.RemoteLogSet)

	go func() {
		if err := p.processRemoteLogSets(ctx, logger, session, request.EvrID, request); err != nil {
			logger.Error("Failed to process remote log set", zap.Error(err))
		}
	}()

	return nil
}

func (p *Pipeline) processRemoteLogSets(ctx context.Context, logger *zap.Logger, session *sessionEVR, evrID evr.XPID, request *evr.RemoteLogSet) error {
	if !session.userID.IsNil() {
		p.userRemoteLogJournalRegistry.Add(session.id, session.userID, filterRemoteLogs(request.Logs))
	}
	return SendEvent(ctx, p.nk, &EventRemoteLogSet{
		Node:         p.node,
		UserID:       session.UserID().String(),
		SessionID:    session.ID().String(),
		XPID:         evrID,
		Username:     session.Username(),
		RemoteLogSet: request,
	})
}

type MatchGameStateUpdate struct {
	CurrentGameClock time.Duration    `json:"current_game_clock,omitempty"`
	PauseDuration    time.Duration    `json:"pause_duration,omitempty"`
	Goals            []*evr.MatchGoal `json:"goals,omitempty"`
	MatchOver        bool             `json:"match_over,omitempty"`
}

func (u *MatchGameStateUpdate) String() string {
	b, err := json.Marshal(u)
	if err != nil {
		return ""
	}
	return string(b)
}

func (u *MatchGameStateUpdate) Bytes() []byte {
	b, err := json.Marshal(u)
	if err != nil {
		return nil
	}
	return b
}

func (u *MatchGameStateUpdate) FromGoal(goal *evr.RemoteLogGoal) {

	pauseDuration := 0 * time.Second

	if goal.GameInfoIsArena && !goal.GameInfoIsPrivate {
		// If the game is an arena game, and not private, then pause the clock after the goal.
		pauseDuration = AfterGoalDuration + RespawnDuration + CatapultDuration
	}

	u.PauseDuration = pauseDuration

	if u.Goals == nil {
		u.Goals = make([]*evr.MatchGoal, 0, 1)
	}

	playerInfoXPID, err := evr.ParseEvrId(goal.PlaterInfoXPID)
	if err != nil {
		return // Invalid XPID, skip this goal
	}
	prevPlayerXPID, err := evr.ParseEvrId(goal.PrevPlayerXPID)
	if err != nil {
		return // Invalid previous player XPID, skip this goal
	}
	u.Goals = append(u.Goals, &evr.MatchGoal{
		GoalTime:              goal.GameInfoGameTime,
		GoalType:              goal.GoalType,
		DisplayName:           goal.PlayerInfoDisplayName,
		TeamID:                int64(goal.PlayerInfoTeamID),
		XPID:                  *playerInfoXPID,
		PrevPlayerDisplayName: goal.PrevPlayerDisplayname,
		PrevPlayerTeamID:      int64(goal.PrevPlayerTeamID),
		PrevPlayerXPID:        *prevPlayerXPID,
		WasHeadbutt:           goal.WasHeadbutt,
		PointsValue:           GoalTypeToPoints(goal.GoalType),
	})
}
