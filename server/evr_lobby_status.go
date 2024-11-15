package server

import (
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/rtapi"
	"go.uber.org/zap"
)

type MatchmakingStreamOpCode uint8

const (
	MatchmakingStreamOpCodeParameters MatchmakingStreamOpCode = iota
)

type MatchmakingStreamData struct {
	Parameters *LobbySessionParameters `json:"parameters,omitempty"`
}

func (d MatchmakingStreamData) String() string {
	b, _ := json.Marshal(d)
	return string(b)
}

type GuildLobbyLabel struct {
	GroupID string `json:"group_id"`
}

func JoinMatchmakingStream(logger *zap.Logger, s *sessionWS, params *LobbySessionParameters) error {

	groupStream := params.MatchmakingStream()

	presenceMeta := params.PresenceMeta()

	// Leave any existing lobby group stream.
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, groupStream)

	ctx := s.Context()

	if success := s.tracker.Update(ctx, s.id, groupStream, s.userID, presenceMeta); !success {
		return fmt.Errorf("failed to track lobby group matchmaking stream")
	}

	s.pipeline.router.SendToStream(logger, groupStream, &rtapi.Envelope{
		Message: &rtapi.Envelope_StreamData{
			StreamData: &rtapi.StreamData{
				Stream: &rtapi.Stream{
					Mode:    int32(SessionFormatJson),
					Subject: groupStream.Subject.String(),
				},
				Sender: &rtapi.UserPresence{
					UserId:    s.UserID().String(),
					SessionId: s.ID().String(),
					Username:  s.Username(),
				},
				Data: MatchmakingStreamData{Parameters: params}.String(),
			},
		},
	}, true)

	return nil
}

func LeaveMatchmakingStream(logger *zap.Logger, s *sessionWS) error {
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	return nil
}
