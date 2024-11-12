package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
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

		var ok bool
		var messageType string

		messageType, ok = strMap["message_type"].(string)
		if !ok {
			messageType = ""
		}

		if message, ok := strMap["message"].(string); ok {

			switch message {

			// Ignore customization interactions.
			case "Podium Interaction":
				fallthrough
			case "Customization Item Preview":
				fallthrough
			case "Customization Item Equip":
				fallthrough
			case "server library loaded":
				continue
			case "r15 net game error message":
				continue
			}

		}

		if category, ok := strMap["category"].(string); ok {
			ignoredCategories := []string{
				"iap",
				"rich_presence",
				"social",
			}
			for _, ignored := range ignoredCategories {
				if category == ignored {
					continue OuterLoop
				}
			}
		}

		if typ, ok := strMap["message_type"].(string); ok {
			if strings.HasPrefix(typ, "OVR_IAP") {
				// Ignore IAP logs.
				continue
			}
		}

		parsed, err := evr.RemoteLogMessageFromMessage(strMap, bytes)
		if err != nil {
			if errors.Is(err, evr.ErrUnknownRemoteLogMessageType) {
				logger.Warn("Unknown remote log message type", zap.String("messageType", messageType), zap.String("log", logString))
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

	// Parse the useful remote logs from the set.
	entries := parseRemoteLogMessageEntries(logger, request.Logs)

	// Add them to the journal first.
	if found := p.userRemoteLogJournalRegistry.AddEntries(session.id, entries); !found {
		// This is a new session, so we need to start a goroutine to remove/store the session's journal entries when the session is closed.
		go func() {
			<-session.Context().Done()
			messages := p.userRemoteLogJournalRegistry.RemoveSessionAll(session.id)

			data, err := json.Marshal(messages)
			if err != nil {
				logger.Error("Failed to marshal remote log messages", zap.Error(err))
				return
			}
			// Write the remoteLog to storage.
			ops := StorageOpWrites{
				{
					OwnerID: session.userID.String(),
					Object: &api.WriteStorageObject{
						Collection:      RemoteLogStorageCollection,
						Key:             RemoteLogStorageJournalKey,
						Value:           string(data),
						PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
						PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
						Version:         "",
					},
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
			defer cancel()
			_, _, err = StorageWriteObjects(ctx, logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops)
			if err != nil {
				logger.Error("Failed to write remote log journal.", zap.Error(err))
			}
			logger.Debug("Wrote remote log journal to storage.")
		}()
	}

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

		switch msg := e.Parsed.(type) {

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
						periodicity: periodicty,
					}

					if _, err := p.leaderboardRegistry.RecordWriteTabletStat(ctx, meta, userID, username, 1); err != nil {
						logger.Warn("Failed to submit leaderboard", zap.Error(err))
					}
				}
			}

			profile.SetEarlyQuitStatistics(*eq)

			err = p.profileRegistry.SaveAndCache(ctx, uuid.FromStringOrNil(userID), profile)
			if err != nil {
				logger.Error("Failed to save player's profile")
			}
			update, _ = updates.LoadOrStore(matchID.UUID, &MatchGameStateUpdate{})
			update.CurrentGameClock = time.Duration(msg.GameInfoGameTime) * time.Second

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

			sessionID := msg.SessionUUID()

			if msg.SessionUUID().IsNil() {
				logger.Error("VOIP loudness message has no session ID")
				continue
			}
			if msg.GameInfoGameTime == 0 {
				continue
			}
			update, _ = updates.LoadOrStore(sessionID, &MatchGameStateUpdate{})
			update.FromVOIPLoudness(msg)

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
			}
			profile, err := p.profileRegistry.Load(ctx, session.userID)
			if err != nil {
				return status.Errorf(codes.Internal, "Failed to load player's profile")
			}
			profile.SetEvrID(evrID)
			p.profileRegistry.UpdateEquippedItem(profile, category, name)

			err = p.profileRegistry.SaveAndCache(ctx, session.userID, profile)
			if err != nil {
				return status.Errorf(codes.Internal, "Failed to store player's profile")
			}

		case *evr.RemoteLogRepairMatrix:

		case *evr.RemoteLogServerConnectionFailed:

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

	u.CurrentGameClock = time.Duration(goal.GameInfoGameTime) * time.Second
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

func (u *MatchGameStateUpdate) FromVOIPLoudness(goal *evr.RemoteLogVOIPLoudness) {
	u.CurrentGameClock = time.Duration(goal.GameInfoGameTime)
}
