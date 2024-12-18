package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type GenericRemoteLog struct {
	MessageType string `json:"messageType"`
	Message     string `json:"message"`
	Parsed      any
}

func parseRemoteLogMessageEntries(logger *zap.Logger, logs []string) []*GenericRemoteLog {

	entries := make([]*GenericRemoteLog, 0, len(logs))

OuterLoop:
	for _, logString := range logs {
		// Unmarshal the top-level to check the message type.

		bytes := []byte(logString)

		strMap := map[string]interface{}{}

		if err := json.Unmarshal(bytes, &strMap); err != nil {
			logger.Debug("Non-JSON log entry", zap.String("entry", logString))
			continue
		}

		ignores := map[string][]string{
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

		for key, values := range ignores {
			if value, ok := strMap[key].(string); ok {
				for _, v := range values {
					if value == v {
						continue OuterLoop
					}
				}
			}
		}

		var messageType string
		if v, ok := strMap["message_type"].(string); ok {
			messageType = v
		}

		parsed, err := evr.RemoteLogMessageFromMessage(strMap, bytes)
		if err != nil {
			if errors.Is(err, evr.ErrUnknownRemoteLogMessageType) {
				logger.Warn("Unknown remote log message type", zap.String("message_type", messageType), zap.String("log", logString))
			} else {
				logger.Debug("Failed to parse remote log message", zap.Error(err))
			}
			parsed = evr.RemoteLogString(logString)
		}

		entries = append(entries, &GenericRemoteLog{
			MessageType: messageType,
			Message:     logString,
			Parsed:      parsed,
		})
	}

	return entries
}

func (p *EvrPipeline) processRemoteLogSets(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, request *evr.RemoteLogSet) error {

	// Add the raw logs to the journal.
	p.userRemoteLogJournalRegistry.AddEntries(session.id, request.Logs)

	// Parse the useful remote logs from the set.
	entries := parseRemoteLogMessageEntries(logger, request.Logs)

	// Collect the updates to the match's game metadata (e.g. game clock)
	updates := MapOf[uuid.UUID, *MatchGameStateUpdate]{}

	for _, e := range entries {
		var update *MatchGameStateUpdate

		/*
			// If this is a session remote log, add it to the match manager.
			if m, ok := e.Parsed.(SessionRemoteLog); ok {
				if p.matchLogManager != nil {
					if err := p.matchLogManager.AddLog(m); err != nil {
						logger.Warn("Failed to add log", zap.Error(err))
					}
				}
			}
		*/
		if msg, ok := e.Parsed.(evr.GameTimer); ok {
			matchID, err := NewMatchID(msg.SessionUUID(), p.node)
			if err != nil {
				logger.Warn("Failed to create match ID", zap.Error(err), zap.Any("msg", msg))
			} else {
				update, _ = updates.LoadOrStore(matchID.UUID, &MatchGameStateUpdate{})
				update.CurrentGameClock = time.Duration(msg.GameTime()) * time.Second
			}
		}
		logger := logger.With(zap.String("message_type", fmt.Sprintf("%T", e.Parsed)))
		switch msg := e.Parsed.(type) {

		case *evr.RemoteLogDisconnectedDueToTimeout:
			logger.Warn("Disconnected due to timeout", zap.String("username", session.Username()), zap.String("evr_id", evrID.String()), zap.Any("remote_log_message", msg))

		case *evr.RemoteLogUserDisconnected:

			if msg.GameInfoIsPrivate || !msg.GameInfoIsArena {
				// Don't process disconnects for private games, or non-arena games.
				continue
			}

			// Do not process disconnects for games that have not started.
			if msg.GameInfoGameTime == 0 {
				continue
			}

			matchID, err := NewMatchID(msg.SessionUUID(), p.node)
			if err != nil {
				logger.Error("Failed to create match ID", zap.Error(err))
				continue
			}

			label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
			if err != nil || label == nil {
				logger.Error("Failed to get match label", zap.Error(err))
				continue
			}

			userID, err := GetUserIDByEvrID(ctx, p.db, msg.PlayerEvrID)
			if err != nil || userID == "" {
				logger.Error("Failed to get user ID by evr ID", zap.Error(err))
				continue
			}

			profile, err := p.profileRegistry.Load(ctx, uuid.FromStringOrNil(userID))
			if err != nil {
				logger.Error("Failed to load player's profile")
				continue
			}

			var username string
			for _, player := range label.Players {
				if player.EvrID.String() == msg.PlayerEvrID {
					username = player.Username
					break
				}
			}

			eq := profile.GetEarlyQuitStatistics()
			eq.IncrementEarlyQuits()

			if stats := profile.Server.Statistics; stats != nil {
				eq.ApplyEarlyQuitPenalty(logger, userID, label, stats, 0.01)

				for _, periodicty := range []string{"alltime", "daily", "weekly"} {
					meta := LeaderboardMeta{
						mode:        evr.ModeArenaPublic,
						name:        "EarlyQuits",
						operator:    "add",
						periodicity: periodicty,
					}

					if _, err := p.leaderboardRegistry.LeaderboardTabletStatWrite(context.Background(), meta, userID, username, 1.0); err != nil {
						return fmt.Errorf("Leaderboard record write error: %v", err)
					}
				}
			}

			profile.SetEarlyQuitStatistics(*eq)

			err = p.profileRegistry.SaveAndCache(ctx, uuid.FromStringOrNil(userID), profile)
			if err != nil {
				logger.Error("Failed to save player's profile")
			}

		case *evr.RemoteLogGoal:

			sessionID := msg.SessionUUID()

			if sessionID.IsNil() {
				logger.Error("Goal message has no session ID")
				continue
			}

			update, _ = updates.LoadOrStore(sessionID, &MatchGameStateUpdate{})

			update.FromGoal(msg)

		case *evr.RemoteLogGhostUser:
			// This is a ghost user message.

		case *evr.RemoteLogVOIPLoudness:

		case *evr.RemoteLogSessionStarted:

		case *evr.RemoteLogGameSettings:

		case *evr.RemoteLogCustomizationMetricsPayload:

			if msg.EventType != "item_equipped" {
				continue
			}
			category, name, err := msg.GetEquippedCustomization()
			if err != nil {
				logger.Error("Failed to get equipped customization", zap.Error(err))
				continue
			}
			if category == "" || name == "" {
				logger.Error("Equipped customization is empty")
				continue
			}
			profile, err := p.profileRegistry.Load(ctx, session.userID)
			if err != nil {
				return fmt.Errorf("failed to load player's profile: %w", err)
			}
			profile.SetEvrID(evrID)
			if err := p.profileRegistry.UpdateEquippedItem(profile, category, name); err != nil {
				return fmt.Errorf("failed to update equipped item: %w", err)
			}

			err = p.profileRegistry.SaveAndCache(ctx, session.userID, profile)
			if err != nil {
				return fmt.Errorf("failed to save player's profile: %w", err)
			}

		case *evr.RemoteLogRepairMatrix:

		case *evr.RemoteLogServerConnectionFailed:

			params, ok := LoadParams(session.Context())
			if !ok {
				logger.Error("Failed to load params")
				continue
			}

			msgData, err := json.MarshalIndent(msg, "", "  ")
			if err != nil {
				logger.Error("Failed to marshal remote log message", zap.Error(err))
			}

			matchID, err := NewMatchID(msg.SessionUUID(), p.node)
			if err != nil {
				logger.Error("Failed to create match ID", zap.Error(err))
				continue
			}
			// Get the match label
			label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
			if err != nil || label == nil {
				logger.Error("Failed to get match label", zap.Error(err))
				continue
			}

			messageContent := struct {
				MatchID          MatchID    `json:"match_id"`
				MatchMode        evr.Symbol `json:"match_mode"`
				MatchStartedAt   time.Time  `json:"match_start_time"`
				ServerID         string     `json:"server_id"`
				MatchOperator    string     `json:"server_operator"`
				MatchEndpoint    string     `json:"server_endpoint"`
				ClientUserID     string     `json:"client_user_id"`
				ClientUsername   string     `json:"client_username"`
				ClientDiscordID  string     `json:"client_discord_id"`
				ClientEvrID      evr.EvrId  `json:"client_evr_id"`
				ClientIsPCVR     bool       `json:"client_is_pcvr"`
				RemoteLogMessage string     `json:"remote_log_message"`
			}{
				MatchID:          matchID,
				MatchMode:        label.Mode,
				MatchStartedAt:   label.StartTime,
				ServerID:         label.Broadcaster.SessionID,
				MatchOperator:    label.Broadcaster.OperatorID,
				MatchEndpoint:    label.Broadcaster.Endpoint.String(),
				ClientIsPCVR:     params.IsPCVR.Load(),
				ClientUserID:     session.userID.String(),
				ClientUsername:   session.Username(),
				ClientDiscordID:  params.DiscordID,
				ClientEvrID:      params.XPID,
				RemoteLogMessage: string(msgData),
			}
			// Check if the match's group wants audit messages
			contentData, err := json.MarshalIndent(messageContent, "", "  ")
			if err != nil {
				logger.Error("Failed to marshal message content", zap.Error(err))
			}
			p.appBot.LogAuditMessage(ctx, label.GetGroupID().String(), fmt.Sprintf("```json\n%s\n```", string(contentData)), false)

			logger.Warn("Server connection failed", zap.String("username", session.Username()), zap.String("match_id", msg.SessionUUID().String()), zap.String("evr_id", evrID.String()), zap.Any("remote_log_message", msg))

			acct, err := p.runtimeModule.AccountGetId(ctx, label.Broadcaster.OperatorID)
			if err != nil {
				logger.Error("Failed to get account", zap.Error(err))
				continue
			}

			tags := map[string]string{
				"operator_id":       label.Broadcaster.OperatorID,
				"operator_username": acct.User.Username,
				"ext_ip":            label.Broadcaster.Endpoint.GetExternalIP(),
				"port":              strconv.Itoa(int(label.Broadcaster.Endpoint.Port)),
				"mode":              label.Mode.String(),
				"is_pcvr":           strconv.FormatBool(params.IsPCVR.Load()),
			}

			p.runtimeModule.MetricsCounterAdd("remotelog_error_server_connection_failed_count", tags, 1)
		default:
		}
	}

	updates.Range(func(key uuid.UUID, value *MatchGameStateUpdate) bool {
		p.matchRegistry.SendData(key, p.node, session.userID, session.id, session.Username(), p.node, OpCodeMatchGameStateUpdate, value.Bytes(), false, time.Now().Unix())
		return true
	})

	return nil
}

type MatchGameStateUpdate struct {
	CurrentGameClock time.Duration `json:"current_game_clock,omitempty"`
	PauseDuration    time.Duration `json:"pause_duration,omitempty"`
	Goals            []*MatchGoal  `json:"goals,omitempty"`
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
		u.Goals = make([]*MatchGoal, 0, 1)
	}
	u.Goals = append(u.Goals, &MatchGoal{
		GoalTime:              goal.GameInfoGameTime,
		GoalType:              goal.GoalType,
		Displayname:           goal.PlayerInfoDisplayName,
		Teamid:                goal.PlayerInfoTeamID,
		EvrID:                 goal.PlayerInfoEvrID,
		PrevPlayerDisplayName: goal.PrevPlayerDisplayname,
		PrevPlayerTeamID:      goal.PrevPlayerTeamID,
		PrevPlayerEvrID:       goal.PrevPlayerEvrID,
		WasHeadbutt:           goal.WasHeadbutt,
	})
}
