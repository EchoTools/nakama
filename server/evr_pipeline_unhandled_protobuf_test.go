package server

import (
	"context"
	"testing"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// A protobuf envelope whose Message type has no case in ProcessProtobufRequest
// must not drop the game server's connection: the legacy path already logs and
// keeps the session, and the protobuf path must match.
func TestProcessRequestEVRUnhandledProtobufKeepsSession(t *testing.T) {
	p := &EvrPipeline{}
	ws := &sessionWS{id: uuid.Must(uuid.NewV4()), userID: uuid.Must(uuid.NewV4()), ctx: context.Background()}

	// Error has no case in the ProcessProtobufRequest switch.
	envelope := &rtapi.Envelope{Message: &rtapi.Envelope_Error{Error: &rtapi.Error{}}}

	if err := p.ProcessProtobufRequest(zap.NewNop(), ws, envelope); err != nil {
		t.Errorf("ProcessProtobufRequest returned %v for an unrecognized message type; want nil", err)
	}

	payload, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ProcessRequestEVR(zap.NewNop(), ws, &evr.NEVRProtobufMessageV1{Payload: payload}) {
		t.Fatal("ProcessRequestEVR returned false for an unrecognized protobuf message; the session read loop would break")
	}
}
