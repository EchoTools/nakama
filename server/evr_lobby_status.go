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
	DiscordID         string                  `json:"discord_id,omitempty"`
	Parameters        *LobbySessionParameters `json:"parameters,omitempty"`
	BackfillQuery     string                  `json:"backfill_query,omitempty"`
	MatchmakingQuery  string                  `json:"matchmaking_query,omitempty"`
	StringParameters  map[string]string       `json:"string_parameters,omitempty"`
	NumericParameters map[string]float64      `json:"numeric_parameters,omitempty"`
}

func (d MatchmakingStreamData) String() string {
	b, _ := json.Marshal(d)
	return string(b)
}

type GuildLobbyLabel struct {
	GroupID string `json:"group_id"`
}

func JoinMatchmakingStream(logger *zap.Logger, s *sessionWS, lobbyParams *LobbySessionParameters) error {

	stream := lobbyParams.MatchmakingStream()

	presenceMeta := lobbyParams.PresenceMeta()

	// Leave any existing lobby group stream.
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, stream)

	ctx := s.Context()

	if success := s.tracker.Update(ctx, s.id, stream, s.userID, presenceMeta); !success {
		return fmt.Errorf("failed to track lobby group matchmaking stream")
	}

	sessionParams, found := LoadParams(s.ctx)
	if !found {
		return fmt.Errorf("failed to load lobby session parameters")
	}

	ticketConfig, ok := DefaultMatchmakerTicketConfigs[lobbyParams.Mode]
	if !ok {
		ticketConfig = MatchmakingTicketParameters{
			IncludeRankRange:        true,
			IncludeEarlyQuitPenalty: true,
		}
	}

	query, stringProps, numericProps := lobbyParams.MatchmakingParameters(&ticketConfig)
	s.pipeline.router.SendToStream(logger, stream, &rtapi.Envelope{
		Message: &rtapi.Envelope_StreamData{
			StreamData: &rtapi.StreamData{
				Stream: &rtapi.Stream{
					Mode:    int32(SessionFormatJson),
					Subject: stream.Subject.String(),
				},
				Sender: &rtapi.UserPresence{
					UserId:    s.UserID().String(),
					SessionId: s.ID().String(),
					Username:  s.Username(),
				},
				Data: MatchmakingStreamData{
					DiscordID:         sessionParams.discordID,
					Parameters:        lobbyParams,
					BackfillQuery:     lobbyParams.BackfillSearchQuery(true, true),
					MatchmakingQuery:  query,
					StringParameters:  stringProps,
					NumericParameters: numericProps,
				}.String(),
			},
		},
	}, true)

	return nil
}

func LeaveMatchmakingStream(logger *zap.Logger, s *sessionWS) error {
	s.tracker.UntrackLocalByModes(s.id, map[uint8]struct{}{StreamModeMatchmaking: {}}, PresenceStream{})
	return nil
}
