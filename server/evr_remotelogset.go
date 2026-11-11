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
	filteredLogs := make([]string, 0, len(logs))
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

// maxRemoteLogPayload is the byte cap on one set's entries; it is the OOM guard
// from 12d90d0ab, whose measured cause was queued JSON copies of whole payloads,
// i.e. bytes. There is deliberately no cap on entry count: memory follows bytes,
// and the wire decoder already refuses more than evr.MaxStringTableEntries
// (1024) entries. A 50-entry cap here cut an 8-player post-match set (72
// entries) before POST_MATCH_MATCH_TYPE_STATS reached most of its players.
const maxRemoteLogPayload = 256 * 1024 // 256KB total payload limit

// capRemoteLogs applies the payload gates to a set's raw entries and returns
// the entries to keep. It must run before filtering.
func capRemoteLogs(logs []string) []string {
	totalSize := 0
	for i, log := range logs {
		totalSize += len(log)
		if totalSize > maxRemoteLogPayload {
			return logs[:i]
		}
	}
	return logs
}

// applyRemoteLogCaps caps a set's entries and, when anything was dropped,
// says so: one Warn naming the session, the entries kept and the entries dropped.
func applyRemoteLogCaps(logger *zap.Logger, sessionID string, logs []string) []string {
	kept := capRemoteLogs(logs)
	if dropped := len(logs) - len(kept); dropped > 0 {
		logger.Warn("Remote log set truncated by payload cap",
			zap.String("sid", sessionID),
			zap.Int("kept", len(kept)),
			zap.Int("dropped", dropped))
	}
	return kept
}

func (p *EvrPipeline) processRemoteLogSets(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, request *evr.RemoteLogSet) error {
	request.Logs = applyRemoteLogCaps(logger, session.id.String(), request.Logs)

	// Filter once, use everywhere
	filteredLogs := filterRemoteLogs(request.Logs)
	request.Logs = filteredLogs

	if !session.userID.IsNil() {
		p.userRemoteLogJournalRegistry.Add(session.id, session.userID, filteredLogs)
	}
	if dispatcher := globalEventDispatcher.Load(); dispatcher != nil {
		dispatcher.Dispatch(ctx, &EventRemoteLogSet{
			Node:         p.node,
			UserID:       session.UserID().String(),
			SessionID:    session.ID().String(),
			XPID:         evrID,
			Username:     session.Username(),
			RemoteLogSet: request,
		})
		return nil
	}
	// Fallback to legacy JSON path if dispatcher not yet initialized
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
	GameStatus       GameStatus       `json:"game_status"`
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
