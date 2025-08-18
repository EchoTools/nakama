package server

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/echotools/nakama/v3/server/evr"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func EVRSessionConsume(s *sessionWS) {
	// Fire an event for session start.
	if fn := s.runtime.EventSessionStart(); fn != nil {
		fn(s.userID.String(), s.username.Load(), s.vars, s.expiry, s.id.String(), s.clientIP, s.clientPort, s.lang, time.Now().UTC().Unix())
	}

	s.conn.SetReadLimit(s.config.GetSocket().MaxMessageSizeBytes)
	if err := s.conn.SetReadDeadline(time.Now().Add(s.pongWaitDuration)); err != nil {
		s.logger.Warn("Failed to set initial read deadline", zap.Error(err))
		go s.Close("failed to set initial read deadline", runtime.PresenceReasonDisconnect)
		return
	}
	s.conn.SetPongHandler(func(string) error {
		s.maybeResetPingTimer()
		return nil
	})

	// Start a routine to process outbound messages.
	go s.processOutgoing()

	var reason string
	var data []byte

	isDebug := s.logger.Core().Enabled(zap.DebugLevel)

IncomingLoop:
	for {

		messageType, data, err := s.conn.ReadMessage()
		if err != nil {
			// Ignore "normal" WebSocket errors.
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				// Ignore underlying connection being shut down while read is waiting for data.
				var opErr *net.OpError
				if !errors.As(err, &opErr) || opErr.Error() != net.ErrClosed.Error() {
					if s.format != SessionFormatEVR {
						// EchoVR does not cleanly close connections.
						s.logger.Debug("Error reading message from client", zap.Error(err))
					}
					reason = err.Error()
				}
			}
			break
		}
		start := time.Now()
		if messageType != s.wsMessageType {
			// Expected text but received binary, or expected binary but received text.
			// Disconnect client if it attempts to use this kind of mixed protocol mode.
			s.logger.Debug("Received unexpected WebSocket message type", zap.Int("expected", s.wsMessageType), zap.Int("actual", messageType))
			reason = "received unexpected WebSocket message type"
			break
		}

		s.receivedMessageCounter--
		if s.receivedMessageCounter <= 0 {
			s.receivedMessageCounter = s.config.GetSocket().PingBackoffThreshold
			if !s.maybeResetPingTimer() {
				// Problems resetting the ping timer indicate an error so we need to close the loop.
				reason = "error updating ping timer"
				break
			}
		}

		if s.format == SessionFormatEVR {
			// EchoVR messages do not map directly onto nakama messages.

			requests, err := evr.ParsePacket(data)
			if err != nil {
				if errors.Is(err, evr.ErrSymbolNotFound) {
					s.logger.Debug("Received unknown message", zap.Error(err))
					continue
				}
				// If the payload is malformed the client is incompatible or misbehaving, either way disconnect it now.
				s.logger.Warn("Received malformed payload", zap.Binary("data", data), zap.Error(err))
				reason = "received malformed payload"
				break
			}
			// Send to the Evr pipeline for routing/processing.

			for _, request := range requests {
				logger := s.logger.With(zap.String("request_type", fmt.Sprintf("%T", request)))
				if isDebug { // remove extra heavy reflection processing
					logger = logger.With(zap.String("request", fmt.Sprintf("%s", request)))
					logger.Debug("Received message")
				}
				if request == nil {
					continue
				}

				if !s.evrPipeline.ProcessRequestEVR(logger, s, request) {
					reason = "error processing message"
					break IncomingLoop
				}
			}
		} else {
			request := &rtapi.Envelope{}
			switch s.format {
			case SessionFormatProtobuf:
				err = proto.Unmarshal(data, request)
			case SessionFormatJson:
				fallthrough
			default:
				err = s.protojsonUnmarshaler.Unmarshal(data, request)
			}
			if err != nil {
				// If the payload is malformed the client is incompatible or misbehaving, either way disconnect it now.
				s.logger.Warn("Received malformed payload", zap.Binary("data", data))
				reason = "received malformed payload"
				break
			}

			switch request.Cid {
			case "":
				if !s.pipeline.ProcessRequest(s.logger, s, request) {
					reason = "error processing message"
					break IncomingLoop
				}
			default:
				requestLogger := s.logger.With(zap.String("cid", request.Cid))
				if !s.pipeline.ProcessRequest(requestLogger, s, request) {
					reason = "error processing message"
					break IncomingLoop
				}
			}
		}
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), false)
		s.metrics.CustomTimer("socket_incoming_message_processing_time", nil, time.Millisecond*time.Since(start))
	}

	if reason != "" {
		// Update incoming message metrics.
		s.metrics.Message(int64(len(data)), true)
	}

	s.Close(reason, runtime.PresenceReasonDisconnect)
}
