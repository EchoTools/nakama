package server

import (
	"encoding/json"
	"fmt"

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

func JoinMatchmakingStream(logger *zap.Logger, s *sessionWS, params *LobbySessionParameters) (PresenceStream, error) {

	label := MatchmakingGroupLabel{
		Mode:        params.Mode.String(),
		GroupID:     params.GroupID.String(),
		VersionLock: params.VersionLock.String(),
	}

	labelStr := label.String()
	_ = labelStr

	groupStream := PresenceStream{Mode: StreamModeMatchmaking, Subject: params.GroupID, Label: label.String()}
	logger.Debug("Joining lobby group matchmaking stream", zap.Any("stream", groupStream))

	data, err := json.Marshal(params)
	if err != nil {
		return PresenceStream{}, fmt.Errorf("failed to marshal lobby group matchmaking stream data: %w", err)
	}

	presenceMeta := PresenceMeta{
		Status: string(data),
	}

	// Leave any existing lobby group stream.
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})

	ctx := s.Context()

	if success := s.tracker.TrackMulti(ctx, s.id, []*TrackerOp{
		{Stream: groupStream, Meta: presenceMeta},
	}, s.userID); !success {

		return PresenceStream{}, fmt.Errorf("failed to track lobby group matchmaking stream")
	} else {
		logger.Debug("Tracked lobby group matchmaking stream", zap.Any("stream", groupStream), zap.Any("meta", presenceMeta))
	}

	// Track the groupID as well
	return groupStream, nil
}
func LeaveMatchmakingStream(logger *zap.Logger, s *sessionWS) error {
	logger.Debug("Leaving lobby group matchmaking stream")
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	return nil
}
