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
	MessageType string `json:"message"`
}

func (p *EvrPipeline) processRemoteLogSets(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, request *evr.RemoteLogSet) error {
	logger.Debug("Processing remote log set", zap.Any("request", request))
	updates := MapOf[uuid.UUID, *MatchGameStateUpdate]{}
	for _, str := range request.Logs {
		var update *MatchGameStateUpdate

		// Unmarshal the top-level to check the message type.

		entry := map[string]interface{}{}

		data := []byte(str)
		if err := json.Unmarshal(data, &entry); err != nil {
			if logger.Core().Enabled(zap.DebugLevel) {
				logger.Debug("Non-JSON log entry", zap.String("entry", str))
			}
		}
		if _, ok := entry["message"]; !ok {
			logger.Error("Remote log entry has no message type", zap.String("entry", str))
			return nil
		}

		messageType, ok := entry["message"].(string)
		if !ok {
			logger.Error("Remote log entry has invalid message type", zap.String("entry", str))
			return nil
		}
		messageType = strings.ToLower(messageType)

		logger.Debug("Processing remote log set", zap.Any("logs", entry))
		switch strings.ToLower(messageType) {
		case "goal":
			// This is a goal message.
			goal := evr.RemoteLogGoal{}
			if err := json.Unmarshal(data, &goal); err != nil {
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
			if err := json.Unmarshal(data, ghostUser); err != nil {
				logger.Error("Failed to unmarshal ghost user", zap.Error(err))
				continue
			}
			_ = ghostUser

		case "game_settings":
			gameSettings := &evr.RemoteLogGameSettings{}
			if err := json.Unmarshal(data, gameSettings); err != nil {
				logger.Error("Failed to unmarshal game settings", zap.Error(err))
				continue
			}

			// Store the game settings (this isn't really used)
			ops := StorageOpWrites{
				{
					OwnerID: session.userID.String(),
					Object: &api.WriteStorageObject{
						Collection:      RemoteLogStorageCollection,
						Key:             GamePlayerSettingsStorageKey,
						Value:           string(data),
						PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
						PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
						Version:         "",
					},
				},
			}
			if _, _, err := StorageWriteObjects(ctx, logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops); err != nil {
				logger.Error("Failed to write game settings", zap.Error(err))
				continue
			}

		case "session_started":
			// TODO let the match know the server loaded the session?
			sessionStarted := &evr.RemoteLogSessionStarted{}
			if err := json.Unmarshal(data, sessionStarted); err != nil {
				logger.Error("Failed to unmarshal session started", zap.Error(err))
				continue
			}
		case "customization item preview":
			fallthrough
		case "customization item equip":
			fallthrough
		case "podium interaction":
			fallthrough
		case "interaction_event":
			// Avoid spamming the logs with interaction events.
			if !logger.Core().Enabled(zap.DebugLevel) {
				continue
			}
			event := &evr.RemoteLogInteractionEvent{}
			if err := json.Unmarshal(data, &event); err != nil {
				logger.Error("Failed to unmarshal interaction event", zap.Error(err))
				continue
			}
		case "customization_metrics_payload":
			// Update the server profile with the equipped cosmetic item.
			c := &evr.RemoteLogCustomizationMetricsPayload{}
			if err := json.Unmarshal(data, &c); err != nil {
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

			err = p.profileRegistry.Save(ctx, session.userID, profile)
			if err != nil {
				return status.Errorf(codes.Internal, "Failed to store player's profile")
			}

		default:
			if logger.Core().Enabled(zap.DebugLevel) {
				// Write the remoteLog to storage.
				ops := StorageOpWrites{
					{
						OwnerID: session.userID.String(),
						Object: &api.WriteStorageObject{
							Collection:      RemoteLogStorageCollection,
							Key:             messageType,
							Value:           str,
							PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
							PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
							Version:         "",
						},
					},
				}
				_, _, err := StorageWriteObjects(ctx, logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops)
				if err != nil {
					logger.Error("Failed to write remote log", zap.Error(err))
				}
			} else {
				//logger.Debug("Received unknown remote log", zap.Any("entry", entry))
			}
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

func (u *MatchGameStateUpdate) Bytes() []byte {
	b, err := json.Marshal(u)
	if err != nil {
		return nil
	}
	return b
}

func (u *MatchGameStateUpdate) FromGoal(goal evr.RemoteLogGoal) {

	u.RoundClock = goal.GameInfoGameTime
	u.Paused = true
	u.PauseDuration = time.Duration(AfterGoalDuration+RespawnDuration+CatapultDuration) * time.Second
	if u.LastGoal == nil {
		u.LastGoal = &LastGoal{
			GoalTime:              goal.GameInfoGameTime,
			GoalType:              goal.GoalType,
			Displayname:           goal.PlayerInfoDisplayname,
			Teamid:                goal.PlayerInfoTeamid,
			EvrID:                 goal.PlayerInfoUserid,
			PrevPlayerDisplayName: goal.PrevPlayerDisplayname,
			PrevPlayerTeamID:      goal.PrevPlayerTeamid,
			PrevPlayerEvrID:       goal.PrevPlayerUserid,
			WasHeadbutt:           goal.WasHeadbutt,
		}
	}
}
