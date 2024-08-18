package server

import (
	"context"
	"encoding/json"
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
}

func parseRemoteLogMessageEntries(logger *zap.Logger, logs []string) []GenericRemoteLog {
	entries := make([]GenericRemoteLog, 0, len(logs))
OuterLoop:
	for _, str := range logs {
		// Unmarshal the top-level to check the message type.

		entry := map[string]interface{}{}

		data := []byte(str)
		if err := json.Unmarshal(data, &entry); err != nil {
			if logger.Core().Enabled(zap.DebugLevel) {
				logger.Debug("Non-JSON log entry", zap.String("entry", str))
				continue
			}
		}

		if message, ok := entry["message"]; ok {
			if message == "CUSTOMIZATION_METRICS_PAYLOAD" {
				entry["message_type"] = "customization_metrics_payload"
			}
		}

		if _, ok := entry["message_type"]; !ok {
			//logger.Debug("Remote log entry has no message type", zap.String("entry", str))
			continue
		}

		messageType, ok := entry["message_type"].(string)
		if !ok {
			logger.Debug("Remote log entry has invalid message type", zap.String("entry", str))
			continue
		}

		messageType = strings.ToLower(messageType)

		if strings.HasPrefix("ovr_iap", messageType) {
			// Ignore IAP logs.
			continue
		}

		if category, ok := entry["category"].(string); ok {
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
		entries = append(entries, GenericRemoteLog{
			MessageType: messageType,
			Message:     str,
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

	updates := MapOf[uuid.UUID, *MatchGameStateUpdate]{}

	for _, e := range entries {
		var update *MatchGameStateUpdate

		// Unmarshal the top-level to check the message type.

		switch strings.ToLower(e.MessageType) {
		case "user_disconnect":
			msg := &evr.RemoteLogUserDisconnected{}
			if err := json.Unmarshal([]byte(e.Message), msg); err != nil {
				logger.Error("Failed to unmarshal user disconnect", zap.Error(err))
			}
			if msg.GameInfoIsPrivate || !msg.GameInfoIsArena {
				// Don't process disconnects for private games, or non-arena games.
				continue
			}

			// Do not process disconnects for games that have not started.
			if msg.GameInfoGameTime == 0 {
				continue
			}

			userID, err := GetUserIDByEvrID(ctx, p.db, msg.PlayerInfoUserid)
			if err != nil || userID == "" {
				logger.Error("Failed to get user ID by evr ID", zap.Error(err))
			}

			profile, err := p.profileRegistry.Load(ctx, uuid.FromStringOrNil(userID))
			if err != nil {
				logger.Error("Failed to load player's profile")
			}

			eq := profile.GetEarlyQuitStatistics()
			eq.IncrementEarlyQuits()
			profile.SetEarlyQuitStatistics(eq)
			stats := profile.Server.Statistics
			if stats != nil {
				eq.ApplyEarlyQuitPenalty(logger, stats, evr.ModeArenaPublic, 0.01)
			}

			err = p.profileRegistry.SaveAndCache(ctx, uuid.FromStringOrNil(userID), profile)
			if err != nil {
				logger.Error("Failed to save player's profile")
			}
			update, _ = updates.LoadOrStore(msg.SessionID(), &MatchGameStateUpdate{})
			update.CurrentRoundClock = msg.GameInfoGameTime

		case "goal":
			// This is a goal message.
			goal := evr.RemoteLogGoal{}
			if err := json.Unmarshal([]byte(e.Message), &goal); err != nil {
				logger.Error("Failed to unmarshal goal", zap.Error(err))
				continue
			}

			sessionID := goal.SessionID()

			if goal.SessionID() == uuid.Nil {
				logger.Error("Goal message has no session ID")
				continue
			}

			update, _ = updates.LoadOrStore(sessionID, &MatchGameStateUpdate{})

			update.FromGoal(goal)

		case "ghost_user":
			// This is a ghost user message.
			ghostUser := &evr.RemoteLogGhostUser{}
			if err := json.Unmarshal([]byte(e.Message), ghostUser); err != nil {
				logger.Error("Failed to unmarshal ghost user", zap.Error(err))
				continue
			}
			_ = ghostUser
		case "voip_loudness":
			voipVolume := evr.RemoteLogVOIPLoudness{}
			if err := json.Unmarshal([]byte(e.Message), &voipVolume); err != nil {
				logger.Error("Failed to unmarshal voip volume", zap.Error(err))
				continue
			}

			sessionID := voipVolume.SessionID()

			if voipVolume.SessionID() == uuid.Nil {
				logger.Error("Goal message has no session ID")
				continue
			}
			if voipVolume.GameInfoGameTime == 0 {
				continue
			}
			update, _ = updates.LoadOrStore(sessionID, &MatchGameStateUpdate{})
			update.FromVOIPLoudness(voipVolume)

		case "game_settings":
			fallthrough
		case "session_started":
			fallthrough
		case "customization item preview":
			fallthrough
		case "customization item equip":
			fallthrough
		case "podium interaction":
			fallthrough
		case "interaction_event":
			fallthrough
		case "customization_metrics_payload":
			// Update the server profile with the equipped cosmetic item.
			c := &evr.RemoteLogCustomizationMetricsPayload{}
			if err := json.Unmarshal([]byte(e.Message), &c); err != nil {
				logger.Error("Failed to unmarshal customization metrics", zap.Error(err))
				continue
			}

			if c.EventType != "item_equipped" {
				continue
			}
			category, name, err := c.GetEquippedCustomization()
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
	GameState
	PauseDuration time.Duration `json:"pause_duration,omitempty"`
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

func (u *MatchGameStateUpdate) FromGoal(goal evr.RemoteLogGoal) {
	u.CurrentRoundClock = goal.GameInfoGameTime
	u.PauseDuration = time.Duration(AfterGoalDuration+RespawnDuration+CatapultDuration) * time.Second
	if u.Goals == nil {
		u.Goals = make([]LastGoal, 0, 1)
	}
	u.Goals = append(u.Goals, LastGoal{
		GoalTime:              goal.GameInfoGameTime,
		GoalType:              goal.GoalType,
		Displayname:           goal.PlayerInfoDisplayname,
		Teamid:                goal.PlayerInfoTeamid,
		EvrID:                 goal.PlayerInfoUserid,
		PrevPlayerDisplayName: goal.PrevPlayerDisplayname,
		PrevPlayerTeamID:      goal.PrevPlayerTeamid,
		PrevPlayerEvrID:       goal.PrevPlayerUserid,
		WasHeadbutt:           goal.WasHeadbutt,
	})
}

func (u *MatchGameStateUpdate) FromVOIPLoudness(goal evr.RemoteLogVOIPLoudness) {
	u.CurrentRoundClock = float64(goal.GameInfoGameTime)
}
