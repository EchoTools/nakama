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
	"github.com/intinig/go-openskill/types"
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

	// Collect the post-match messages for processing
	postMatchMessages := make(map[uuid.UUID][]evr.RemoteLog)

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
			if msg.GameInfoIsSocial {
				// Social matches do not have a game clock or pauses
				continue
			}
			sessionID := msg.SessionUUID()

			if sessionID.IsNil() {
				logger.Error("Goal message has no session ID")
				continue
			}

			update, _ = updates.LoadOrStore(sessionID, &MatchGameStateUpdate{})
			processMatchGoalIntoUpdate(msg, update)

		case *evr.RemoteLogGhostUser:
			// This is a ghost user message.

		case *evr.RemoteLogVOIPLoudness:
			// Process VOIP loudness data
			if err := s.processVOIPLoudness(ctx, logger, db, nk, statisticsQueue, msg); err != nil {
				logger.WithFields(map[string]any{
					"error": err,
					"msg":   msg,
				}).Debug("Failed to process VOIP loudness")
			}

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
			postMatchMessages[msg.SessionUUID()] = append(postMatchMessages[msg.SessionUUID()], msg)

		case *evr.RemoteLogPostMatchTypeStats:
			update, _ = updates.LoadOrStore(msg.SessionUUID(), &MatchGameStateUpdate{})
			update.MatchOver = true
			postMatchMessages[msg.SessionUUID()] = append(postMatchMessages[msg.SessionUUID()], msg)

		case *evr.RemoteLogPostMatchMatchTypeXPLevel:
			postMatchMessages[msg.SessionUUID()] = append(postMatchMessages[msg.SessionUUID()], msg)
		case *evr.RemoteLogLoadStats:

			if msg.ClientLoadTime > 45 {

				var gg *GuildGroup
				metadata := make(map[string]any)
				msgJSON, _ := json.MarshalIndent(msg, "", "  ")
				content := fmt.Sprintf("High load time detected: %ds\n```json\n%s\n```", int(msg.ClientLoadTime), string(msgJSON))

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

	for sessionUUID, msgs := range postMatchMessages {
		matchID, err := NewMatchID(sessionUUID, s.Node)
		if err != nil {
			logger.Warn("Failed to create match ID for post match processing")
			continue
		}
		if err := s.processPostMatchMessages(ctx, logger, db, nk, sessionRegistry, statisticsQueue, matchID, msgs); err != nil {
			logger.WithField("error", err).Warn("Failed to process post match messages")
		}
	}

	updates.Range(func(key uuid.UUID, value *MatchGameStateUpdate) bool {
		matchRegistry.SendData(key, s.Node, uuid.FromStringOrNil(s.UserID), uuid.FromStringOrNil(s.SessionID), s.Username, s.Node, OpCodeMatchGameStateUpdate, value.Bytes(), false, time.Now().Unix())
		return true
	})

	return nil
}

func (s *EventRemoteLogSet) incrementCompletedMatches(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, sessionRegistry SessionRegistry, userID, sessionID string) error {
	// Decrease the early quitter count for the player
	eqconfig := NewEarlyQuitConfig()
	if err := StorableRead(ctx, nk, userID, eqconfig, true); err != nil {
		logger.WithField("error", err).Warn("Failed to load early quitter config")
	} else {
		eqconfig.IncrementCompletedMatches()

		// Check for tier change after completing match
		serviceSettings := ServiceSettings()
		oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

		if err := StorableWrite(ctx, nk, userID, eqconfig); err != nil {
			logger.WithField("error", err).Warn("Failed to store early quitter config")
		} else {
			// Update session cache
			if playerSession := sessionRegistry.Get(uuid.FromStringOrNil(sessionID)); playerSession != nil {
				if params, ok := LoadParams(playerSession.Context()); ok {
					params.earlyQuitConfig.Store(eqconfig)
				}
			}

			// Send Discord DM if tier changed
			if tierChanged {
				discordID, err := GetDiscordIDByUserID(ctx, db, userID)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to get Discord ID for tier notification")
				} else if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
					var message string
					if oldTier < newTier {
						// Degraded to Tier 2+
						message = TierDegradedMessage
					} else {
						// Recovered to Tier 1
						message = TierRestoredMessage
					}
					if _, err := SendUserMessage(ctx, appBot.dg, discordID, message); err != nil {
						logger.WithField("error", err).Warn("Failed to send tier change DM")
					}
				}
			}
		}
	}
	return nil
}

func (s *EventRemoteLogSet) processPostMatchMessages(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, sessionRegistry SessionRegistry, statisticsQueue *StatisticsQueue, matchID MatchID, msgs []evr.RemoteLog) error {
	var statsByPlayer = make(map[evr.EvrId]evr.MatchTypeStats, 8)

	//var xpLevel *evr.RemoteLogPostMatchMatchTypeXPLevel

	for _, msg := range msgs {
		switch m := msg.(type) {
		case *evr.RemoteLogPostMatchMatchTypeXPLevel:
			continue
		case *evr.RemoteLogPostMatchMatchStats:
			continue
		case *evr.RemoteLogPostMatchTypeStats:

			xpid, err := evr.ParseEvrId(m.XPID)
			if err != nil {
				return fmt.Errorf("failed to parse evr ID: %w", err)
			}
			statsByPlayer[*xpid] = m.Stats

		}
	}

	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil || label == nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}
	if label.Mode != evr.ModeArenaPublic {
		return nil // Only process type stats for arena mode
	}

	groupIDStr := label.GetGroupID().String()

	logger = logger.WithFields(map[string]any{
		"mid": matchID.String(),
	})

	allStatEntries := make([]*StatisticsQueueEntry, 0)

	// Load individual player ratings from leaderboards for MMR calculation
	// This is necessary because label.Players contains matchmaking ratings (aggregate for parties)
	// rather than individual player ratings
	playersWithTeamRatings := make([]PlayerInfo, len(label.Players))
	playersWithPlayerRatings := make([]PlayerInfo, len(label.Players))
	copy(playersWithTeamRatings, label.Players)
	copy(playersWithPlayerRatings, label.Players)

	serviceSettings := ServiceSettings()
	if serviceSettings.UseSkillBasedMatchmaking() {
		for i, p := range playersWithTeamRatings {
			// Only load ratings for competitors (blue/orange team)
			if !p.IsCompetitor() {
				continue
			}

			// Load the player's individual team-based rating from leaderboards
			teamRating, err := MatchmakingRatingLoad(ctx, nk, p.UserID, groupIDStr, label.Mode)
			if err != nil {
				logger.WithFields(map[string]any{
					"error":   err,
					"user_id": p.UserID,
				}).Warn("Failed to load team rating, using default")
				teamRating = NewDefaultRating()
			}
			playersWithTeamRatings[i].RatingMu = teamRating.Mu
			playersWithTeamRatings[i].RatingSigma = teamRating.Sigma

			// Load the player's individual player-based rating from leaderboards
			playerRating, err := MatchmakingPlayerRatingLoad(ctx, nk, p.UserID, groupIDStr, label.Mode)
			if err != nil {
				logger.WithFields(map[string]any{
					"error":   err,
					"user_id": p.UserID,
				}).Warn("Failed to load player rating, using default")
				playerRating = NewDefaultRating()
			}
			playersWithPlayerRatings[i].RatingMu = playerRating.Mu
			playersWithPlayerRatings[i].RatingSigma = playerRating.Sigma
		}
	}

	// Determine winning team once for the entire match
	// Check the first player's stats to determine which team won
	var blueWins bool
	for _, playerInfo := range label.Players {
		if playerInfo.Team == BlueTeam || playerInfo.Team == OrangeTeam {
			if stats, ok := statsByPlayer[playerInfo.EvrID]; ok {
				blueWins = (playerInfo.Team == BlueTeam && stats.ArenaWins > 0) || (playerInfo.Team == OrangeTeam && stats.ArenaLosses > 0)
				break
			}
		}
	}

	// Calculate new ratings once for all players (before the loop to avoid O(n²) complexity)
	var teamRatings map[string]types.Rating
	var playerRatings map[string]types.Rating
	if serviceSettings.UseSkillBasedMatchmaking() {
		// Calculate new team-based ratings using individual player ratings loaded from leaderboards
		teamRatings = CalculateNewTeamRatings(playersWithTeamRatings, statsByPlayer, blueWins)

		// Calculate new individual player ratings using individual player ratings loaded from leaderboards
		playerRatings = CalculateNewIndividualRatings(playersWithPlayerRatings, statsByPlayer, blueWins)
	}

	for xpid, typeStats := range statsByPlayer {
		// Get the match label

		// Get the player's information
		playerInfo := label.GetPlayerByEvrID(xpid)
		if playerInfo == nil {
			// If the player isn't in the match, do not update the stats
			return fmt.Errorf("player not in match: %s", xpid.String())
		}

		// If the player isn't in the match, or isn't a player, do not update the stats
		if playerInfo.Team != BlueTeam && playerInfo.Team != OrangeTeam {
			return fmt.Errorf("non-competitor player cannot have stats updated: %s", xpid.String())
		}

		logger = logger.WithFields(map[string]any{
			"player_uid":  playerInfo.UserID,
			"player_sid":  playerInfo.SessionID,
			"player_xpid": playerInfo.EvrID.String(),
		})

		// Increment the completed matches for the player
		if err := s.incrementCompletedMatches(ctx, logger, nk, db, sessionRegistry, playerInfo.UserID, playerInfo.SessionID); err != nil {
			logger.WithField("error", err).Warn("Failed to increment completed matches")
		}

		if serviceSettings.UseSkillBasedMatchmaking() {

			// Use the pre-calculated team ratings for this player
			if rating, ok := teamRatings[playerInfo.SessionID]; ok {
				// Add team skill rating entries to the statistics queue
				muScore, muSubscore, err := Float64ToScore(rating.Mu)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to convert Team Mu rating to score")
				} else {
					// Write to new TeamSkillRating stat
					allStatEntries = append(allStatEntries, &StatisticsQueueEntry{
						BoardMeta: LeaderboardMeta{
							GroupID:       groupIDStr,
							Mode:          label.Mode,
							StatName:      TeamSkillRatingMuStatisticID,
							Operator:      OperatorSet,
							ResetSchedule: evr.ResetScheduleAllTime,
						},
						UserID:      playerInfo.UserID,
						DisplayName: playerInfo.DisplayName,
						Score:       muScore,
						Subscore:    muSubscore,
						Metadata:    map[string]string{"discord_id": playerInfo.DiscordID},
					})
				}

				sigmaScore, sigmaSubscore, err := Float64ToScore(rating.Sigma)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to convert Team Sigma rating to score")
				} else {
					// Write to new TeamSkillRating stat
					allStatEntries = append(allStatEntries, &StatisticsQueueEntry{
						BoardMeta: LeaderboardMeta{
							GroupID:       groupIDStr,
							Mode:          label.Mode,
							StatName:      TeamSkillRatingSigmaStatisticID,
							Operator:      OperatorSet,
							ResetSchedule: evr.ResetScheduleAllTime,
						},
						UserID:      playerInfo.UserID,
						DisplayName: playerInfo.DisplayName,
						Score:       sigmaScore,
						Subscore:    sigmaSubscore,
						Metadata:    map[string]string{"discord_id": playerInfo.DiscordID},
					})
				}
			} else {
				logger.WithField("target_sid", playerInfo.SessionID).Warn("No team rating found for player in matchmaking ratings")
			}

			// Use the pre-calculated individual player ratings for this player
			if rating, ok := playerRatings[playerInfo.SessionID]; ok {
				// Add player skill rating entries to the statistics queue
				muScore, muSubscore, err := Float64ToScore(rating.Mu)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to convert Player Mu rating to score")
				} else {
					allStatEntries = append(allStatEntries, &StatisticsQueueEntry{
						BoardMeta: LeaderboardMeta{
							GroupID:       groupIDStr,
							Mode:          label.Mode,
							StatName:      PlayerSkillRatingMuStatisticID,
							Operator:      OperatorSet,
							ResetSchedule: evr.ResetScheduleAllTime,
						},
						UserID:      playerInfo.UserID,
						DisplayName: playerInfo.DisplayName,
						Score:       muScore,
						Subscore:    muSubscore,
						Metadata:    map[string]string{"discord_id": playerInfo.DiscordID},
					})
				}

				sigmaScore, sigmaSubscore, err := Float64ToScore(rating.Sigma)
				if err != nil {
					logger.WithField("error", err).Warn("Failed to convert Player Sigma rating to score")
				} else {
					allStatEntries = append(allStatEntries, &StatisticsQueueEntry{
						BoardMeta: LeaderboardMeta{
							GroupID:       groupIDStr,
							Mode:          label.Mode,
							StatName:      PlayerSkillRatingSigmaStatisticID,
							Operator:      OperatorSet,
							ResetSchedule: evr.ResetScheduleAllTime,
						},
						UserID:      playerInfo.UserID,
						DisplayName: playerInfo.DisplayName,
						Score:       sigmaScore,
						Subscore:    sigmaSubscore,
						Metadata:    map[string]string{"discord_id": playerInfo.DiscordID},
					})
				}
			} else {
				logger.WithField("target_sid", playerInfo.SessionID).Warn("No player rating found for player in matchmaking ratings")
			}
		}

		// Update the player's statistics, if the service settings allow it
		if !serviceSettings.DisableStatisticsUpdates {
			statEntries, err := typeStatsToScoreMap(playerInfo.UserID, playerInfo.DisplayName, groupIDStr, label.Mode, typeStats)
			if err != nil {
				return fmt.Errorf("failed to convert type stats to score map: %w", err)
			}
			allStatEntries = append(allStatEntries, statEntries...)
		}
	}

	if len(allStatEntries) > 0 {
		return statisticsQueue.Add(allStatEntries)
	}

	return nil
}

func iterateMatchTypeStatsFields(stats evr.MatchTypeStats, fn func(statName string, op LeaderboardOperator, value float64)) {
	statsBaseType := reflect.TypeOf(stats)
	statsValue := reflect.ValueOf(stats)
	for i := 0; i < statsBaseType.NumField(); i++ {
		field := statsBaseType.Field(i)
		value := statsValue.Field(i)

		opTag := field.Tag.Get("op")
		opParts := strings.Split(opTag, ",")

		if value.IsZero() && slices.Contains(opParts, "omitzero") {
			continue
		}

		jsonTag := field.Tag.Get("json")
		statName := strings.SplitN(jsonTag, ",", 2)[0]

		op := OperatorSet
		switch opParts[0] {
		case "avg":
			op = OperatorSet
		case "add":
			op = OperatorIncrement
		case "max":
			op = OperatorBest
		case "rep":
			op = OperatorSet
		}

		var statValue float64 = 0
		if value.CanInt() {
			statValue = float64(value.Int())
		} else if value.CanUint() {
			statValue = float64(value.Uint())
		} else if value.CanFloat() {
			statValue = float64(value.Float())
		}

		fn(statName, op, statValue)
	}
}

func typeStatsToScoreMap(userID, displayName, groupID string, mode evr.Symbol, stats evr.MatchTypeStats) ([]*StatisticsQueueEntry, error) {
	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}

	entries := make([]*StatisticsQueueEntry, 0)
	var conversionErr error
	iterateMatchTypeStatsFields(stats, func(statName string, op LeaderboardOperator, value float64) {
		if conversionErr != nil {
			return
		}
		for _, r := range resetSchedules {
			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      op,
				ResetSchedule: r,
			}

			score, subscore, err := Float64ToScore(value)
			if err != nil {
				conversionErr = fmt.Errorf("failed to convert stat %s: %w", statName, err)
				return
			}
			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
				Metadata:    nil,
			})
		}
	})

	if conversionErr != nil {
		return nil, conversionErr
	}
	return entries, nil
}

func (s *EventRemoteLogSet) processVOIPLoudness(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, statisticsQueue *StatisticsQueue, msg *evr.RemoteLogVOIPLoudness) error {
	// Get the match ID
	matchID, err := NewMatchID(msg.SessionUUID(), s.Node)
	if err != nil {
		return fmt.Errorf("failed to create match ID: %w", err)
	}

	// Get the match label
	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil || label == nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	if label.Mode != evr.ModeArenaPublic && label.Mode != evr.ModeCombatPublic && label.Mode != evr.ModeSocialPublic {
		return nil // Only process VOIP loudness for arena, combat, and social modes
	}

	// Parse the player's EVR ID
	xpid, err := evr.ParseEvrId(msg.PlayerInfoUserid)
	if err != nil {
		return fmt.Errorf("failed to parse evr ID: %w", err)
	}

	// Get the player's information from the match
	playerInfo := label.GetPlayerByEvrID(*xpid)
	if playerInfo == nil {
		// If the player isn't in the match, don't process
		return fmt.Errorf("player %s not found in match %s", msg.PlayerInfoUserid, matchID.String())
	}

	// Only process for actual players (not spectators)
	if playerInfo.Team != BlueTeam && playerInfo.Team != OrangeTeam && playerInfo.Team != SocialLobbyParticipant {
		return fmt.Errorf("player %s is not on a playing team (team: %d, expected blue, orange, or social)",
			msg.PlayerInfoUserid, playerInfo.Team)
	}

	groupIDStr := label.GetGroupID().String()
	mode := label.Mode

	// Get the loudness value from the message
	loudnessDB := msg.VoiceLoudnessDB

	// Read the current leaderboard record to get existing metadata
	boardID := StatisticBoardID(groupIDStr, mode, PlayerLoudnessStatisticID, evr.ResetScheduleDaily)

	err = UpdateLeaderboardStat(ctx, nk, boardID, playerInfo.UserID, playerInfo.DisplayName, func(currentScore float64, currentMetadata map[string]any) (float64, map[string]any, error) {
		var minLoudness, maxLoudness float64
		var count int64

		// Decode existing metadata
		if currentMetadata != nil {
			if v, ok := currentMetadata["min_loudness"]; ok {
				if s, ok := v.(string); ok {
					minLoudness, _ = strconv.ParseFloat(s, 64)
				}
			}
			if v, ok := currentMetadata["max_loudness"]; ok {
				if s, ok := v.(string); ok {
					maxLoudness, _ = strconv.ParseFloat(s, 64)
				}
			}
			if v, ok := currentMetadata["count"]; ok {
				if s, ok := v.(string); ok {
					count, _ = strconv.ParseInt(s, 10, 64)
				}
			}
		}

		// Update values
		currentScore += loudnessDB
		count++

		// Update min/max
		if count == 1 {
			minLoudness = loudnessDB
			maxLoudness = loudnessDB
		} else {
			if loudnessDB < minLoudness {
				minLoudness = loudnessDB
			}
			if loudnessDB > maxLoudness {
				maxLoudness = loudnessDB
			}
		}

		// Create metadata with min, max, and count
		md := VOIPLoudnessRecordMetadata{
			MinLoudness: minLoudness,
			MaxLoudness: maxLoudness,
			Count:       count,
		}

		// Convert map[string]string to map[string]any
		mdMap := make(map[string]any)
		for k, v := range md.ToMap() {
			mdMap[k] = v
		}

		return currentScore, mdMap, nil
	})

	return err
}

func processMatchGoalIntoUpdate(msg *evr.RemoteLogGoal, update *MatchGameStateUpdate) {
	playerInfoXPID := evr.XPIDFromStringOrNil(msg.PlayerXPID)
	prevPlayerXPID := evr.XPIDFromStringOrNil(msg.PrevPlayerXPID)

	// Arena goals have a predictable pause duration

	update.CurrentGameClock = time.Duration(msg.GameInfoGameTime * float64(time.Second))

	if update.CurrentGameClock > 0 && msg.GameInfoIsArena && !msg.GameInfoIsPrivate {
		update.PauseDuration = AfterGoalDuration + RespawnDuration + CatapultDuration
	}
	update.Goals = append(update.Goals, &evr.MatchGoal{
		GoalTime:              msg.GameInfoGameTime,
		GoalType:              msg.GoalType,
		DisplayName:           msg.PlayerInfoDisplayName,
		TeamID:                int64(msg.PlayerInfoTeamID),
		XPID:                  playerInfoXPID,
		PrevPlayerDisplayName: msg.PrevPlayerDisplayname,
		PrevPlayerTeamID:      int64(msg.PrevPlayerTeamID),
		PrevPlayerXPID:        prevPlayerXPID,
		WasHeadbutt:           msg.WasHeadbutt,
		PointsValue:           GoalTypeToPoints(msg.GoalType),
	})
}

type VOIPLoudnessRecordMetadata struct {
	MinLoudness float64 `json:"min_loudness"`
	MaxLoudness float64 `json:"max_loudness"`
	Count       int64   `json:"count"`
}

func (m *VOIPLoudnessRecordMetadata) ToMap() map[string]string {
	return map[string]string{
		"min_loudness": fmt.Sprintf("%f", m.MinLoudness),
		"max_loudness": fmt.Sprintf("%f", m.MaxLoudness),
		"count":        fmt.Sprintf("%d", m.Count),
	}
}

func VOIPLoudnessRecordMetadataFromString(data string) (*VOIPLoudnessRecordMetadata, error) {
	var dataMap map[string]string
	if err := json.Unmarshal([]byte(data), &dataMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VOIP loudness metadata: %w", err)
	}

	minLoudness, err := strconv.ParseFloat(dataMap["min_loudness"], 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse min_loudness: %w", err)
	}

	maxLoudness, err := strconv.ParseFloat(dataMap["max_loudness"], 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse max_loudness: %w", err)
	}

	count, err := strconv.ParseInt(dataMap["count"], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse count: %w", err)
	}

	return &VOIPLoudnessRecordMetadata{
		MinLoudness: minLoudness,
		MaxLoudness: maxLoudness,
		Count:       count,
	}, nil
}
