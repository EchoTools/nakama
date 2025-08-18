package server

import (
	"encoding/json"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type MatchEntrant struct {
	ServerSessionID uuid.UUID
	ClientSessionID uuid.UUID
	SessionMeta     *evr.LobbySessionSuccess
}

func NewMatchEntrant(serverSessionID uuid.UUID, clientSessionID uuid.UUID, matchID MatchID, mode evr.Symbol, groupID uuid.UUID, endpoint evr.Endpoint, role int) *MatchEntrant {
	msg := evr.NewLobbySessionSuccess(evr.ModeArenaPublic, matchID.UUID, groupID, endpoint, int16(role), true, false, false)

	return &MatchEntrant{
		ServerSessionID: serverSessionID,
		ClientSessionID: clientSessionID,
		SessionMeta:     msg,
	}
}

func TestProcessOutgoingNotificationConnectionInfo(t *testing.T) {
	testLogger := NewConsoleLogger(os.Stdout, true)
	matchID := MatchID{uuid.Must(uuid.NewV4()), "node"}
	groupID := uuid.Must(uuid.NewV4())
	endpoint := evr.Endpoint{
		InternalIP: net.ParseIP("192.168.1.1"),
		ExternalIP: net.ParseIP("10.0.0.1"),
		Port:       6792,
	}
	role := evr.TeamOrange

	entrant := NewMatchEntrant(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), matchID, evr.ModeArenaPublic, groupID, endpoint, role)

	content, err := json.Marshal(entrant)
	if err != nil {
		t.Fatalf("Error marshalling message: %v", err)
	}

	subject := "connection-info"

	code := 1
	senderId := uuid.Nil.String() // System sender
	persistent := false

	nots := &api.Notification{
		Id:         uuid.Must(uuid.NewV4()).String(),
		Subject:    subject,
		Content:    string(content),
		Code:       int32(code),
		SenderId:   senderId,
		Persistent: persistent,
		CreateTime: &timestamppb.Timestamp{Seconds: time.Now().UTC().Unix()},
	}

	type args struct {
		logger  *zap.Logger
		session *sessionEVR
		in      *rtapi.Envelope
	}
	tests := []struct {
		name    string
		args    args
		want    []evr.Message
		wantErr bool
	}{
		{
			name: "Connection info",
			args: args{
				logger:  testLogger,
				session: &sessionEVR{},
				in: &rtapi.Envelope{
					Cid: "1",
					Message: &rtapi.Envelope_Notifications{
						Notifications: &rtapi.Notifications{Notifications: []*api.Notification{nots}},
					},
				},
			},
			want:    []evr.Message{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProcessOutgoing(tt.args.logger, tt.args.session, tt.args.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ProcessOutgoing() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ProcessOutgoing() = %v, want %v", got, tt.want)
			}
		})
	}
}
