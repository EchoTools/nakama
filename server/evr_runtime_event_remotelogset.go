package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var _ = Event(&EventRemoteLogSet{})

type EventRemoteLogSet struct {
	Node         string            `json:"node"`
	UserID       string            `json:"user_id"`
	SessionID    string            `json:"session_id"`
	XPID         evr.EvrId         `json:"xp_id"`
	Username     string            `json:"username"`
	RemoteLogSet *evr.RemoteLogSet `json:"remote_log_set"`
}

func NewEventPostMatchRemoteLog(userID, sessionID string, xpid evr.EvrId, username string, node string, logSet *evr.RemoteLogSet) *EventRemoteLogSet {
	return &EventRemoteLogSet{
		Node:         node,
		UserID:       userID,
		SessionID:    sessionID,
		XPID:         xpid,
		Username:     username,
		RemoteLogSet: logSet,
	}
}

func (s *EventRemoteLogSet) unmarshalLogs(logger runtime.Logger, logStrs []string) ([]evr.RemoteLog, error) {
	entries := make([]evr.RemoteLog, 0, len(logStrs))
	for _, logStr := range logStrs {
		// Parse the useful remote logs from the set.
		parsed, err := evr.UnmarshalRemoteLog([]byte(logStr))
		if err != nil {
			logger := logger.WithFields(map[string]any{
				"log":   logStr,
				"error": err,
			})
			if errors.Is(err, evr.ErrUnknownRemoteLogMessageType) {
				logger.Warn("Unknown remote log message type")
			} else if !errors.Is(err, evr.ErrRemoteLogIsNotJSON) {
				logger.Warn("Failed to parse remote log message")
			}
			continue
		}
		entries = append(entries, parsed)
	}
	return entries, nil
}

func (s *EventRemoteLogSet) Process(ctx context.Context, logger runtime.Logger, dispatcher *EventDispatcher) error {
	var (
		db              = dispatcher.db
		nk              = dispatcher.nk
		sessionRegistry = dispatcher.sessionRegistry
		matchRegistry   = dispatcher.matchRegistry
		statisticsQueue = dispatcher.statisticsQueue
	)
	logger = logger.WithFields(map[string]any{
		"uid":      s.UserID,
		"username": s.Username,
		"evrid":    s.XPID.String(),
	})
	entries, err := s.unmarshalLogs(logger, s.RemoteLogSet.Logs)
	if err != nil {
		logger.WithField("error", err).Warn("Failed to unmarshal remote logs")
		return fmt.Errorf("failed to unmarshal remote logs: %w", err)
	} else {
		logger.WithField("logs", entries).Debug("Unmarshalled remote logs")
	}

	// Send the remote logs to the match data event.
	if matchDatas := make(map[MatchID][]evr.RemoteLog, len(entries)); len(entries) > 0 {
		for _, e := range entries {
			if m, ok := e.(evr.SessionIdentifierRemoteLog); ok {
				if m.SessionUUID().IsNil() {
					continue
				}
				matchID, err := NewMatchID(m.SessionUUID(), s.Node)
				if err != nil {
					continue
				}
				if _, ok := matchDatas[matchID]; !ok {
					matchDatas[matchID] = make([]evr.RemoteLog, 0, len(entries))
				}
				matchDatas[matchID] = append(matchDatas[matchID], e)
			}
		}

		for matchID, matchData := range matchDatas {
			entry := MatchDataRemoteLogSet{
				UserID:    s.UserID,
				SessionID: matchID.String(),
				Logs:      matchData,
			}
			if err := MatchDataEvent(ctx, nk, matchID, entry); err != nil {
				logger.WithField("error", err).Warn("Failed to process match data event")
			}
		}
	}

	// Collect the updates to the match's game metadata (e.g. game clock)
	updates := MapOf[uuid.UUID, *MatchGameStateUpdate]{}

	for _, e := range entries {
		logger := logger.WithField("message_type", fmt.Sprintf("%T", e))

		var update *MatchGameStateUpdate
		if msg, ok := e.(evr.GameTimer); ok {
			matchID, err := NewMatchID(msg.SessionUUID(), s.Node)
			if err != nil {
				logger.WithFields(map[string]any{
					"error": err,
					"msg":   msg,
				}).Warn("Failed to parse match ID")
				continue
			} else {
				update, _ = updates.LoadOrStore(matchID.UUID, &MatchGameStateUpdate{})
				update.CurrentGameClock = time.Duration(msg.GameTime()) * time.Second
			}
		}

		switch msg := e.(type) {

		case *evr.RemoteLogDisconnectedDueToTimeout:
			logger.WithFields(map[string]any{
				"username": s.Username,
				"evrid":    s.XPID.String(),
				"msg":      msg,
			}).Warn("Disconnected due to timeout")

		case *evr.RemoteLogUserDisconnected:

			if !msg.GameInfoIsArena || msg.GameInfoIsPrivate {
				// Don't process disconnects for private games, or non-arena games.
				continue
			}

			// Do not process disconnects for games that have not started.
			if msg.GameInfoGameTime == 0 {
				continue
			}
			/*
				matchID, err := NewMatchID(msg.SessionUUID(), p.node)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to create match ID")
					continue
				}

				label, err := MatchLabelByID(ctx, nk, matchID)
				if err != nil || label == nil {
					logger.WithField("error", err).Warn("Failed to get match label")
					continue
				}

				userID, err := GetUserIDByDeviceID(ctx, db, msg.PlayerEvrID)
				if err != nil || userID == "" {
				logger.WithField("error", err).Debug("Failed to get user ID by evr ID")
					continue
				}
			*/

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

		case *evr.RemoteLogPauseSettings:
			if !s.XPID.IsValid() {
				continue
			}

			userID, err := GetUserIDByDeviceID(ctx, db, s.XPID.String())
			if err != nil {
				logger.WithField("error", err).Debug("Failed to get user ID by evr ID")
				continue
			}
			metadata, err := EVRProfileLoad(ctx, nk, userID)
			if err != nil {
				logger.WithField("error", err).Warn("Failed to load account metadata")
				continue
			}

			metadata.GamePauseSettings = &msg.Settings
			if err := EVRProfileUpdate(ctx, nk, userID, metadata); err != nil {
				logger.WithField("error", err).Warn("Failed to set account metadata")
			}

		case *evr.RemoteLogCustomizationMetricsPayload:

			if msg.EventType != "item_equipped" {
				continue
			}

			category, name, err := msg.GetEquippedCustomization()
			if err != nil {
				logger.WithField("error", err).Warn("Failed to get equipped customization")
				continue
			}
			if category == "" || name == "" {
				logger.Error("Equipped customization is empty")
				continue
			}

			session := sessionRegistry.Get(uuid.FromStringOrNil(s.SessionID))
			if session == nil {
				logger.Error("Failed to get session")
				continue
			}

			params, ok := LoadParams(session.Context())
			if !ok {
				logger.Error("Failed to load params")
				continue
			}

			profile, err := EVRProfileLoad(ctx, nk, session.UserID().String())
			if err != nil {
				logger.WithField("error", err).Warn("Failed to load account metadata")
				continue
			}

			// Update the equipped item in the profile.
			if profile.LoadoutCosmetics.Loadout, err = LoadoutEquipItem(profile.LoadoutCosmetics.Loadout, category, name); err != nil {
				return fmt.Errorf("failed to update equipped item: %w", err)
			}

			if err := EVRProfileUpdate(ctx, nk, session.UserID().String(), profile); err != nil {
				logger.WithField("error", err).Warn("Failed to set account metadata")
			}

			// swap the metadata in the session parameters

			params.profile = profile

			StoreParams(session.Context(), &params)

			modes := []evr.Symbol{
				evr.ModeArenaPublic,
				evr.ModeCombatPublic,
			}

			groupID := params.profile.GetActiveGroupID().String()
			zapLogger := RuntimeLoggerToZapLogger(logger)
			serverProfile, err := UserServerProfileFromParameters(ctx, zapLogger, db, nk, params, groupID, modes, 0)
			if err != nil {
				return fmt.Errorf("failed to get server profile: %w", err)
			}

			if gg, ok := params.guildGroups[groupID]; ok {
				if gg.IsEnforcer(session.UserID().String()) && params.isGoldNameTag.Load() && serverProfile.DeveloperFeatures == nil {
					// Give the user a gold name if they are enabled as a moderator in the guild, and want it.
					serverProfile.DeveloperFeatures = &evr.DeveloperFeatures{}
				}
			}

			/*
				if _, err := p.profileCache.Store(session.ID(), *serverProfile); err != nil {
					return fmt.Errorf("failed to cache profile: %w", err)
				}
			*/

		case *evr.RemoteLogRepairMatrix:

		case *evr.RemoteLogServerConnectionFailed:
			session := sessionRegistry.Get(uuid.FromStringOrNil(s.SessionID))
			if session == nil {
				logger.Error("Failed to get session")
				continue
			}
			params, ok := LoadParams(session.Context())
			if !ok {
				logger.Error("Failed to load params")
				continue
			}

			msgData, err := json.MarshalIndent(msg, "", "  ")
			if err != nil {
				logger.WithField("error", err).Warn("Failed to marshal remote log message")
			}

			matchID, err := NewMatchID(msg.SessionUUID(), s.Node)
			if err != nil {
				logger.WithField("error", err).Warn("Failed to create match ID")
				continue
			}
			// Get the match label
			label, err := MatchLabelByID(ctx, nk, matchID)
			if err != nil || label == nil {
				logger.WithField("error", err).Warn("Failed to get match label")
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
				ServerID:         label.GameServer.SessionID.String(),
				MatchOperator:    label.GameServer.OperatorID.String(),
				MatchEndpoint:    label.GameServer.Endpoint.String(),
				ClientIsPCVR:     params.IsPCVR(),
				ClientUserID:     session.UserID().String(),
				ClientUsername:   session.Username(),
				ClientDiscordID:  params.DiscordID(),
				ClientEvrID:      params.xpID,
				RemoteLogMessage: string(msgData),
			}
			// Check if the match's group wants audit messages
			contentData, err := json.MarshalIndent(messageContent, "", "  ")
			if err != nil {
				logger.WithField("error", err).Warn("Failed to marshal message content")
			}

			globalAppBot.Load().LogUserErrorMessage(ctx, label.GetGroupID().String(), fmt.Sprintf("```json\n%s\n```", string(contentData)), false)

			logger.WithFields(map[string]any{
				"username": session.Username(),
				"mid":      msg.SessionUUID().String(),
				"evrid":    s.XPID.String(),
				"msg":      msg,
			}).Warn("Server connection failed")

			acct, err := nk.AccountGetId(ctx, label.GameServer.OperatorID.String())
			if err != nil {
				logger.WithField("error", err).Warn("Failed to get account")
				continue
			}

			tags := map[string]string{
				"operator_id":       label.GameServer.OperatorID.String(),
				"operator_username": acct.User.Username,
				"ext_ip":            label.GameServer.Endpoint.GetExternalIP(),
				"port":              strconv.Itoa(int(label.GameServer.Endpoint.Port)),
				"mode":              label.Mode.String(),
				"is_pcvr":           strconv.FormatBool(params.IsPCVR()),
			}

			nk.MetricsCounterAdd("remotelog_error_server_connection_failed_count", tags, 1)

		case *evr.RemoteLogPostMatchMatchStats:
			update, _ = updates.LoadOrStore(msg.SessionUUID(), &MatchGameStateUpdate{})
			update.MatchOver = true

			// Increment the completed matches for the player
			if err := s.incrementCompletedMatches(ctx, logger, nk, sessionRegistry, s.UserID, s.SessionID); err != nil {
				logger.WithField("error", err).Warn("Failed to increment completed matches")
				continue
			}

		case *evr.RemoteLogPostMatchTypeStats:
			// Process the post match stats into the player's statistics
			if err := s.processPostMatchTypeStats(ctx, logger, db, nk, sessionRegistry, statisticsQueue, msg); err != nil {
				logger.WithFields(map[string]any{
					"error": err,
					"msg":   msg,
				}).Warn("Failed to process post match type stats")

				continue
			}
		case *evr.RemoteLogLoadStats:

			if msg.LoadTime > 45 {

				var gg *GuildGroup
				metadata := make(map[string]any)
				msgJSON, _ := json.MarshalIndent(msg, "", "  ")
				content := fmt.Sprintf("High load time detected: %ds\n```json\n%s\n```", int(msg.LoadTime), string(msgJSON))

				matchID, presence, _ := GetMatchIDBySessionID(nk, uuid.FromStringOrNil(s.SessionID))

				if matchID.IsValid() {
					// Get the match label
					label, _ := MatchLabelByID(ctx, nk, matchID)
					if label != nil {
						discordID, _ := GetDiscordIDByUserID(ctx, db, presence.GetUserId())

						metadata["user_id"] = presence.GetUserId()
						metadata["match_id"] = matchID.String()
						metadata["username"] = presence.GetUsername()
						metadata["session_id"] = presence.GetSessionId()
						metadata["discord_id"] = discordID
						presenceJSON, _ := json.MarshalIndent(presence, "", "  ")
						content = content + fmt.Sprintf("\nPresence:\n```json\n%s\n```", string(presenceJSON))
						gg, _ = dispatcher.guildGroup(ctx, label.GetGroupID().String())
					}
				}
				if gg != nil {
					AuditLogSend(dg, gg.ErrorChannelID, content)
				}
				AuditLogSend(dg, ServiceSettings().GlobalErrorChannelID, content)
			}
			continue
		default:
		}
	}

	updates.Range(func(key uuid.UUID, value *MatchGameStateUpdate) bool {
		matchRegistry.SendData(key, s.Node, uuid.FromStringOrNil(s.UserID), uuid.FromStringOrNil(s.SessionID), s.Username, s.Node, OpCodeMatchGameStateUpdate, value.Bytes(), false, time.Now().Unix())
		return true
	})

	return nil
}

func (s *EventRemoteLogSet) incrementCompletedMatches(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, sessionRegistry SessionRegistry, userID, sessionID string) error {
	// Decrease the early quitter count for the player
	eqconfig := NewEarlyQuitConfig()
	if err := StorageRead(ctx, nk, userID, eqconfig, true); err != nil {
		logger.WithField("error", err).Warn("Failed to load early quitter config")
	} else {
		eqconfig.IncrementCompletedMatches()
		if err := StorageWrite(ctx, nk, userID, eqconfig); err != nil {
			logger.WithField("error", err).Warn("Failed to store early quitter config")
		}
	}
	if playerSession := sessionRegistry.Get(uuid.FromStringOrNil(sessionID)); playerSession != nil {
		if params, ok := LoadParams(playerSession.Context()); ok {
			params.earlyQuitConfig.Store(eqconfig)
		}
	}
	return nil
}

func (s *EventRemoteLogSet) processPostMatchTypeStats(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, sessionRegistry SessionRegistry, statisticsQueue *StatisticsQueue, msg *evr.RemoteLogPostMatchTypeStats) error {
	var err error
	matchID, err := NewMatchID(msg.SessionUUID(), s.Node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}
	// Get the match label
	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil || label == nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}
	xpid, err := evr.ParseEvrId(msg.XPID)
	if err != nil {
		return fmt.Errorf("failed to parse evr ID: %w", err)
	}
	// Get the player's information
	playerInfo := label.GetPlayerByEvrID(*xpid)
	if playerInfo == nil {
		// If the player isn't in the match, do not update the stats
		return fmt.Errorf("player not in match: %s", msg.XPID)
	}

	// If the player isn't in the match, or isn't a player, do not update the stats
	if playerInfo.Team != BlueTeam && playerInfo.Team != OrangeTeam {
		return fmt.Errorf("non-player profile update request: %s", msg.XPID)
	}
	logger = logger.WithFields(map[string]any{
		"player_uid":  playerInfo.UserID,
		"player_sid":  playerInfo.SessionID,
		"player_xpid": playerInfo.EvrID.String(),
	})

	groupIDStr := label.GetGroupID().String()
	validModes := []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}

	serviceSettings := ServiceSettings()
	if serviceSettings.UseSkillBasedMatchmaking() && slices.Contains(validModes, label.Mode) {

		// Determine winning team
		blueWins := playerInfo.Team == BlueTeam && msg.IsWinner()
		ratings := CalculateNewPlayerRatings(label.Players, blueWins)
		if rating, ok := ratings[playerInfo.SessionID]; ok {
			if err := MatchmakingRatingStore(ctx, nk, playerInfo.UserID, playerInfo.DiscordID, playerInfo.DisplayName, groupIDStr, label.Mode, rating); err != nil {
				logger.WithField("error", err).Warn("Failed to record rating to leaderboard")
			}
		} else {
			logger.WithField("target_sid", playerInfo.SessionID).Warn("No rating found for player in matchmaking ratings")
		}

		zapLogger := RuntimeLoggerToZapLogger(logger)
		// Calculate a new rank percentile
		if rankPercentile, err := CalculateSmoothedPlayerRankPercentile(ctx, zapLogger, db, nk, playerInfo.UserID, groupIDStr, label.Mode); err != nil {
			logger.WithField("error", err).Warn("Failed to calculate new player rank percentile")
			// Store the rank percentile in the leaderboards.
		} else if err := MatchmakingRankPercentileStore(ctx, nk, playerInfo.UserID, playerInfo.DisplayName, groupIDStr, label.Mode, rankPercentile); err != nil {
			logger.WithField("error", err).Warn("Failed to record rank percentile to leaderboard")
		}
	}

	// Update the player's statistics, if the service settings allow it
	if serviceSettings.DisableStatisticsUpdates {
		return nil
	}

	statEntries, err := typeStatsToScoreMap(playerInfo.UserID, playerInfo.DisplayName, groupIDStr, label.Mode, msg.Stats)
	if err != nil {
		return fmt.Errorf("failed to convert type stats to score map: %w", err)
	}
	return statisticsQueue.Add(statEntries)
}

func typeStatsToScoreMap(userID, displayName, groupID string, mode evr.Symbol, stats evr.MatchTypeStats) ([]*StatisticsQueueEntry, error) {
	// Modify the update based on the previous stats
	updateElem := reflect.ValueOf(stats)
	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}

	statsBaseType := reflect.ValueOf(stats).Type()

	nameOperatorMap := make(map[string]LeaderboardOperator, statsBaseType.NumField())
	// Create a map of stat names to their corresponding operator
	for i := range statsBaseType.NumField() {
		jsonTag := statsBaseType.Field(i).Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]
		opTag := statsBaseType.Field(i).Tag.Get("op")
		op := OperatorSet
		switch opTag {
		case "avg":
			op = OperatorSet
		case "add":
			op = OperatorIncrement
		case "max":
			op = OperatorBest
		case "rep":
			op = OperatorSet
		}
		nameOperatorMap[statName] = op
	}

	// construct the entries
	entries := make([]*StatisticsQueueEntry, 0, len(resetSchedules)*updateElem.NumField())

	for i := range updateElem.NumField() {
		updateField := updateElem.Field(i)

		for _, r := range resetSchedules {
			if updateField.IsZero() {
				continue
			}
			// Extract the JSON and operator tags from the struct field
			jsonTag := updateElem.Type().Field(i).Tag.Get("json")
			statName := strings.SplitN(jsonTag, ",", 2)[0]

			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      nameOperatorMap[statName],
				ResetSchedule: r,
			}

			var statValue float64 = 0

			if updateField.CanInt() {
				statValue = float64(updateField.Int())
			} else if updateField.CanUint() {
				statValue = float64(updateField.Uint())
			} else if updateField.CanFloat() {
				statValue = float64(updateField.Float())
			}

			// Skip stats that are not set or negative
			if statValue <= 0 {
				continue
			}

			score, err := Float64ToScore(statValue)
			if err != nil {
				return nil, fmt.Errorf("failed to convert float64 to int64 pair: %w", err)
			}

			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    0,
				Metadata:    nil,
			})
		}
	}

	return entries, nil
}
