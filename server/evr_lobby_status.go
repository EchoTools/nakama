package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
)

type GuildLobbyLabel struct {
	GroupID string `json:"group_id"`
}

type MatchmakingGroupLabel struct {
	Mode        string `json:"mode"`
	GroupID     string `json:"group_id"`
	VersionLock string `json:"version_lock"`
}

func (l MatchmakingGroupLabel) String() string {
	data, _ := json.Marshal(l)
	return string(data)
}

func MatchmakingStream(ctx context.Context, logger *zap.Logger, s *sessionWS, params *LobbySessionParameters) (PresenceStream, MatchmakingGroupLabel, error) {
	_, subject, err := GetLobbyGroupID(ctx, s.pipeline.db, s.userID.String())
	if err != nil {
		return PresenceStream{}, MatchmakingGroupLabel{}, fmt.Errorf("failed to get party group ID: %v", err)
	}
	if subject == uuid.Nil {
		subject = s.id
	}
	label := MatchmakingGroupLabel{
		Mode:        params.Mode.String(),
		GroupID:     params.GroupID.String(),
		VersionLock: params.VersionLock.String(),
	}
	stream := PresenceStream{Mode: StreamModeMatchmaking, Subject: subject, Label: label.String()}
	return stream, label, nil
}

func JoinMatchmakingStream(logger *zap.Logger, s *sessionWS, stream PresenceStream) error {

	logger.Debug("Joining lobby group matchmaking stream", zap.Any("stream", stream))
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{stream.Mode: {}}, stream)
	// Leave any existing lobby group stream.

	ctx := s.Context()

	if success, isNew := s.tracker.Track(ctx, s.id, stream, s.UserID(), PresenceMeta{}); !success {
		return fmt.Errorf("failed to track lobby group matchmaking stream")
	} else if isNew {
		logger.Debug("Tracked lobby group matchmaking stream")
	}
	return nil
}
func LeaveMatchmakingStream(logger *zap.Logger, s *sessionWS) error {
	logger.Debug("Leaving lobby group matchmaking stream")
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	return nil
}
