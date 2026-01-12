package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
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

func (p *EvrPipeline) processRemoteLogSets(ctx context.Context, _ *zap.Logger, session *sessionWS, evrID evr.EvrId, request *evr.RemoteLogSet) error {
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
