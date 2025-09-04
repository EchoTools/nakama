package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/nevr-common/v3/rtapi"

	evr "github.com/echotools/nakama/v3/protocol"
	nkrtapi "github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/heroiclabs/nakama/v3/social"
)

var dg *discordgo.Session

var unrequireMessage = evr.NewSTcpConnectionUnrequireEvent()

var GlobalAppBot = atomic.NewPointer[DiscordAppBot](nil)

type Pipeline struct {
	sync.RWMutex
	ctx context.Context

	node string

	internalIP net.IP

	logger *zap.Logger
	db     *sql.DB

	config          server.Config
	storageIndex    server.StorageIndex
	matchRegistry   server.MatchRegistry
	sessionRegistry server.SessionRegistry
	sessionCache    server.SessionCache
	statusRegistry  server.StatusRegistry
	partyRegistry   server.PartyRegistry
	tracker         server.Tracker
	router          server.MessageRouter
	streamManager   server.StreamManager
	metrics         server.Metrics

	version string

	nk            runtime.NakamaModule
	nevr          *RuntimeGoNEVRModule
	runtimeLogger runtime.Logger

	discordCache *DiscordIntegrator
	appBot       *DiscordAppBot

	userRemoteLogJournalRegistry *UserLogJouralRegistry
	guildGroupRegistry           *GuildGroupRegistry
	ipInfoCache                  *IPInfoCache

	placeholderEmail string
	linkDeviceURL    string
}

func NewEvrPipeline(logger *zap.Logger, startupLogger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, protojsonMarshaler *protojson.MarshalOptions, protojsonUnmarshaler *protojson.UnmarshalOptions, config server.Config, version string, socialClient *social.Client, storageIndex server.StorageIndex, leaderboardScheduler server.LeaderboardScheduler, leaderboardCache server.LeaderboardCache, leaderboardRankCache server.LeaderboardRankCache, sessionRegistry server.SessionRegistry, sessionCache server.SessionCache, statusRegistry server.StatusRegistry, matchRegistry server.MatchRegistry, partyRegistry server.PartyRegistry, matchmaker *atomic.Pointer[server.LocalMatchmaker], tracker server.Tracker, router server.MessageRouter, streamManager server.StreamManager, metrics server.Metrics, discordIntegrator *DiscordIntegrator, appBot *DiscordAppBot, userRemoteLogJournalRegistry *UserLogJouralRegistry, guildGroupRegistry *GuildGroupRegistry, ipInfoCache *IPInfoCache) *Pipeline {

	nevr := &RuntimeGoNEVRModule{
		RuntimeGoNakamaModule: nk.(*server.RuntimeGoNakamaModule),
		zapLogger:             logger,
		db:                    db,
		metrics:               metrics,
		storageIndex:          storageIndex,
	}

	evrPipeline := &Pipeline{
		node:    config.GetName(),
		version: version,

		logger:          logger,
		db:              db,
		config:          config,
		storageIndex:    storageIndex,
		matchRegistry:   matchRegistry,
		sessionRegistry: sessionRegistry,
		sessionCache:    sessionCache,
		statusRegistry:  statusRegistry,
		partyRegistry:   partyRegistry,
		tracker:         tracker,
		router:          router,
		streamManager:   streamManager,
		metrics:         metrics,

		nk:            nevr,
		nevr:          nevr,
		runtimeLogger: server.NewRuntimeGoLogger(logger),

		discordCache: discordIntegrator,
		appBot:       appBot,

		userRemoteLogJournalRegistry: userRemoteLogJournalRegistry,
		guildGroupRegistry:           guildGroupRegistry,
		ipInfoCache:                  ipInfoCache,

		placeholderEmail: config.GetRuntime().Environment[EnvVarPlaceholderEmailDomain],
		linkDeviceURL:    config.GetRuntime().Environment["LINK_DEVICE_URL"],
	}

	return evrPipeline
}

func (p *Pipeline) Stop() {}

func (p *Pipeline) ProcessRequest(logger *zap.Logger, session *sessionEVR, in evr.Message) bool {

	// Handle legacy messages

	var envelope *rtapi.Envelope

	switch msg := in.(type) {
	// EVR Encapsulated Protobuf message (NEVR protocol)

	case *evr.NEVRProtobufJSONMessageV1:
		envelope = &rtapi.Envelope{}
		err := protojson.Unmarshal(msg.Payload, envelope)
		if err != nil {
			logger.Error("Failed to unmarshal protobuf message", zap.String("format", "json"), zap.Error(err), zap.String("data", string(msg.Payload)))
			return false
		}

	case *evr.NEVRProtobufMessageV1:
		envelope = &rtapi.Envelope{}
		err := proto.Unmarshal(msg.Payload, envelope)
		if err != nil {
			logger.Error("Failed to unmarshal protobuf message", zap.String("format", "binary"), zap.Error(err), zap.Binary("data", msg.Payload))
			return false
		}

	}

	if envelope == nil {
		// TODO: Extract this to a separate file/function
		// Handle legacy messages that are not encapsulated in NEVR protobuf messages
		switch msg := in.(type) {
		case *evr.GameServerRegistrationRequest: // Legacy message
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_GameServerRegistration{
					GameServerRegistration: &rtapi.GameServerRegistrationMessage{
						ServerId:          msg.ServerID,
						InternalIpAddress: msg.InternalIP.String(),
						Port:              uint32(msg.Port),
						Region:            uint64(msg.Region),
						VersionLock:       uint64(msg.VersionLock),
					},
				},
			}
		case *evr.GameServerJoinAttempt: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			entrantIDStrs := make([]string, 0, len(msg.EntrantIDs))
			for _, id := range msg.EntrantIDs {
				entrantIDStrs = append(entrantIDStrs, id.String())
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbyEntraConnected{
					LobbyEntraConnected: &rtapi.LobbyEntrantsConnectedMessage{
						LobbySessionId: matchID.UUID.String(),
						EntrantIds:     entrantIDStrs,
					},
				},
			}
		case *evr.GameServerPlayerRemoved: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbyEntrantRemove{
					LobbyEntrantRemove: &rtapi.LobbyEntrantRemovedMessage{
						LobbySessionId: matchID.UUID.String(),
						EntrantId:      msg.EntrantID.String(),
					},
				},
			}
		case *evr.BroadcasterPlayerSessionsLocked: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbySessionEvent{
					LobbySessionEvent: &rtapi.LobbySessionEventMessage{
						LobbySessionId: matchID.UUID.String(),
						Code:           int32(rtapi.LobbySessionEventMessage_LOCKED),
					},
				},
			}
		case *evr.BroadcasterPlayerSessionsUnlocked: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbySessionEvent{
					LobbySessionEvent: &rtapi.LobbySessionEventMessage{
						LobbySessionId: matchID.UUID.String(),
						Code:           int32(rtapi.LobbySessionEventMessage_UNLOCKED),
					},
				},
			}
		case *evr.BroadcasterSessionEnded: // Legacy message
			matchID, _, err := GameServerBySessionID(p.nk, session.ID())
			if err != nil {
				logger.Error("Failed to get broadcaster's match by session ID", zap.Error(err))
				return true
			}
			envelope = &rtapi.Envelope{
				Message: &rtapi.Envelope_LobbySessionEvent{
					LobbySessionEvent: &rtapi.LobbySessionEventMessage{
						LobbySessionId: matchID.UUID.String(),
						Code:           int32(rtapi.LobbySessionEventMessage_ENDED),
					},
				},
			}
		}
	}
	if envelope != nil {
		// This is a legacy message that has been converted to a protobuf message
		if err := p.ProcessProtobufRequest(logger, session, envelope); err != nil {
			logger.Error("Failed to process protobuf message", zap.Any("envelope", envelope), zap.Error(err))
			return false
		}
		return true
	}

	// TODO: Remove ctx from the function call. It should be part of the session.
	var pipelineFn func(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error

	switch in.(type) {

	// Login/Profile Service
	case *evr.RemoteLogSet:
		pipelineFn = p.remoteLogSetv3
	case *evr.LoginRequest:
		params, _ := LoadParams(session.Context())
		// Set the game settings based on the service settings
		gameSettings := evr.NewDefaultGameSettings()
		if params.enableAllRemoteLogs {
			gameSettings.RemoteLogSocial = true
			gameSettings.RemoteLogWarnings = true
			gameSettings.RemoteLogErrors = true
			gameSettings.RemoteLogRichPresence = true
			gameSettings.RemoteLogMetrics = true
		}

		if err := session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewLoginSuccess(session.id, session.xpid),
				unrequireMessage,
				gameSettings,
			},
		}); err != nil {
			logger.Error("Failed to send login success message", zap.Error(err))
			return false
		}
		return true
	case *evr.DocumentRequest:
		pipelineFn = p.documentRequest
	case *evr.LoggedInUserProfileRequest:
		pipelineFn = p.loggedInUserProfileRequest
	case *evr.ChannelInfoRequest:
		pipelineFn = p.channelInfoRequest
	case *evr.UpdateClientProfile:
		pipelineFn = p.updateClientProfileRequest
	case *evr.OtherUserProfileRequest:
		pipelineFn = p.otherUserProfileRequest
	case *evr.UserServerProfileUpdateRequest: // Broadcaster only via it's login connection
		pipelineFn = p.userServerProfileUpdateRequest
	case *evr.GenericMessage:
		pipelineFn = p.genericMessage

	// server.Matchmaker service
	case *evr.LobbyFindSessionRequest:
		pipelineFn = p.lobbySessionRequest
	case *evr.LobbyCreateSessionRequest:
		pipelineFn = p.lobbySessionRequest
	case *evr.LobbyJoinSessionRequest:
		pipelineFn = p.lobbySessionRequest
	case *evr.LobbyMatchmakerStatusRequest:
		pipelineFn = p.lobbyMatchmakerStatusRequest
	case *evr.LobbyPingResponse:
		pipelineFn = p.lobbyPingResponse
	case *evr.LobbyPlayerSessionsRequest:
		pipelineFn = p.lobbyPlayerSessionsRequest
	case *evr.LobbyPendingSessionCancel:
		pipelineFn = p.lobbyPendingSessionCancel

	default:
		pipelineFn = func(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
			logger.Warn("Received unhandled message", zap.Any("message", in))
			return nil
		}
	}

	if err := pipelineFn(session.Context(), logger, session, in); err != nil {
		// Unwrap the error
		logger.Error("server.Pipeline error", zap.Error(err))
		// TODO: Handle errors and close the connection
	}
	// Keep the connection open, otherwise the client will display "service unavailable"
	return true
}

// Process outgoing protobuf envelopes and translate them to Evr messages
func (p *Pipeline) ProcessOutgoing(logger *zap.Logger, session *sessionEVR, in *nkrtapi.Envelope) ([]evr.Message, error) {

	switch in.Message.(type) {

	case *nkrtapi.Envelope_StreamData:
		// EVR binary protocol data
		payload := []byte(in.GetStreamData().GetData())
		if bytes.HasPrefix(payload, evr.MessageMarker) {
			return nil, session.SendBytes(payload, true)
		}

	case *nkrtapi.Envelope_MatchData:
		// EVR binary protocol data
		if in.GetMatchData().GetOpCode() == OpCodeEVRPacketData {
			return nil, session.SendBytes(in.GetMatchData().GetData(), true)
		}
	}

	if err := p.updatePartyLeaderFromMessage(session, in); err != nil {
		logger.Error("Failed to update party leader from message", zap.Error(err))
		return nil, err
	}
	params, ok := LoadParams(session.Context())
	if !ok {
		logger.Error("Failed to get lobby parameters")
		return nil, nil
	}

	switch in.Message.(type) {
	case *nkrtapi.Envelope_Party, *nkrtapi.Envelope_PartyCreate, *nkrtapi.Envelope_PartyJoin,
		*nkrtapi.Envelope_PartyLeave, *nkrtapi.Envelope_PartyPromote, *nkrtapi.Envelope_PartyLeader,
		*nkrtapi.Envelope_PartyAccept, *nkrtapi.Envelope_PartyRemove, *nkrtapi.Envelope_PartyClose,
		*nkrtapi.Envelope_PartyJoinRequestList, *nkrtapi.Envelope_PartyJoinRequest,
		*nkrtapi.Envelope_PartyMatchmakerAdd, *nkrtapi.Envelope_PartyMatchmakerRemove,
		*nkrtapi.Envelope_PartyMatchmakerTicket, *nkrtapi.Envelope_PartyData, *nkrtapi.Envelope_PartyDataSend,
		*nkrtapi.Envelope_PartyPresenceEvent, *nkrtapi.Envelope_PartyUpdate:
		return nil, p.processPartyMessage(logger, session, in)
	}

	// DM the user on discord
	if !strings.HasPrefix(session.Username(), "broadcaster:") && params.relayOutgoing {
		content := ""
		switch msg := in.Message.(type) {
		case *nkrtapi.Envelope_StreamData:

			discordMessage := struct {
				Stream  *nkrtapi.Stream       `json:"stream"`
				Sender  *nkrtapi.UserPresence `json:"sender"`
				Content json.RawMessage       `json:"content"`
			}{
				msg.StreamData.Stream,
				msg.StreamData.Sender,
				json.RawMessage(msg.StreamData.Data),
			}

			if msg.StreamData.Stream.Mode == int32(server.SessionFormatJson) {
				data, err := json.MarshalIndent(discordMessage, "", "  ")
				if err != nil {
					logger.Error("Failed to marshal stream data", zap.Error(err))
				} else {
					content = fmt.Sprintf("```json\n%s\n```", data)
				}
			}

		case *nkrtapi.Envelope_Error:

			// Json the message
			data, err := json.MarshalIndent(in.GetError(), "", "  ")
			if err != nil {
				logger.Error("Failed to marshal error", zap.Error(err))
			} else {
				content = string("```json\n" + string(data) + "\n```")
			}

		case *nkrtapi.Envelope_StatusPresenceEvent, *nkrtapi.Envelope_MatchPresenceEvent, *nkrtapi.Envelope_StreamPresenceEvent:

			// Json the message
			data, _ := json.MarshalIndent(in.GetMessage(), "", "  ")
			content = string("```json\n" + string(data) + "\n```")

		default:
			if data, err := json.MarshalIndent(in.GetMessage(), "", "  "); err != nil {
				logger.Error("Failed to marshal message", zap.Error(err))
			} else if len(data) > 2000 {
				content = "Message too long to display"
			} else if len(data) > 0 {
				content = string("```json\n" + string(data) + "\n```")
			}
		}

		if content != "" {
			if dg := p.discordCache.dg; dg == nil {
				// No discord bot
			} else if discordID, err := GetDiscordIDByUserID(session.Context(), p.db, session.UserID().String()); err != nil {
				logger.Warn("Failed to get discord ID", zap.Error(err))
			} else if channel, err := dg.UserChannelCreate(discordID); err != nil {
				logger.Warn("Failed to create DM channel", zap.Error(err))
			} else {

				// Limit the entire size of the message to 4k bytes
				if len(content) > 4000 {
					content = content[:4000]
				}

				// If the message is over 1800 bytes, then send it in chunks. just split it into 1800 byte chunks
				if len(content) > 1800 {
					for i := 0; i < len(content); i += 1800 {
						max := min(i+1800, len(content))
						if _, err = dg.ChannelMessageSend(channel.ID, content[i:max]); err != nil {
							logger.Warn("Failed to send message to user", zap.Error(err))
						}
					}
				} else {
					if _, err = dg.ChannelMessageSend(channel.ID, content); err != nil {
						logger.Warn("Failed to send message to user", zap.Error(err))
					}
				}
			}
		}
	}
	return nil, nil
}

func (p *Pipeline) ProcessProtobufRequest(logger *zap.Logger, session server.Session, in *rtapi.Envelope) error {
	if in.Message == nil {
		return fmt.Errorf("received empty protobuf message")
	}

	var pipelineFn func(logger *zap.Logger, session *sessionEVR, in *rtapi.Envelope) error

	// Process the request based on its type
	switch in.Message.(type) {
	case *rtapi.Envelope_GameServerRegistration:
		pipelineFn = p.gameserverRegistrationRequest
	case *rtapi.Envelope_LobbyEntraConnected:
		pipelineFn = p.lobbyEntrantConnected
	case *rtapi.Envelope_LobbySessionEvent:
		pipelineFn = p.lobbySessionEvent
	case *rtapi.Envelope_LobbyEntrantRemove:
		pipelineFn = p.lobbyEntrantRemoved
	default:
		// For all other messages, use the generic protobuf handler
		return fmt.Errorf("unhandled protobuf message type: %T", in.Message)
	}

	if err := pipelineFn(logger, session.(*sessionEVR), in); err != nil {
		logger.Error("Failed to process protobuf message", zap.Any("envelope", in), zap.Error(err))
		return err
	}
	return nil
}
