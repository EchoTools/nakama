package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type mockVacateNakamaModule struct {
	runtime.NakamaModule
	matchLabels map[string]*MatchLabel
	signalData  string
	signalErr   error
}

func (m *mockVacateNakamaModule) MatchSignal(ctx context.Context, matchID string, data string) (string, error) {
	m.signalData = data
	return `{"success":true,"message":"","payload":""}`, m.signalErr
}

func (m *mockVacateNakamaModule) MatchGet(ctx context.Context, id string) (*api.Match, error) {
	if label, ok := m.matchLabels[id]; ok {
		labelJSON, _ := json.Marshal(label)
		labelValue := &wrapperspb.StringValue{Value: string(labelJSON)}
		return &api.Match{
			MatchId: id,
			Label:   labelValue,
		}, nil
	}
	return nil, ErrMatchNotFound
}

func TestVacateCommand_Normal(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.Must(uuid.NewV4())
	matchID := MatchID{uuid.Must(uuid.NewV4()), "node1"}

	nk := &mockVacateNakamaModule{
		matchLabels: map[string]*MatchLabel{
			matchID.String(): {
				ID:    matchID,
				Owner: ownerID,
			},
		},
	}

	handler := &VacateCommandHandler{
		nk:     nk,
		logger: &testLogger{t: t},
	}

	i := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "match-id",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: matchID.String(),
					},
				},
			},
		},
	}

	err := handler.HandleVacateCommand(ctx, nil, i, ownerID.String())

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if nk.signalData == "" {
		t.Fatal("Expected signal to be sent to match")
	}

	var envelope SignalEnvelope
	if err := json.Unmarshal([]byte(nk.signalData), &envelope); err != nil {
		t.Fatalf("Failed to parse signal envelope: %v", err)
	}

	if envelope.OpCode != SignalShutdown {
		t.Errorf("Expected SignalShutdown opcode, got %d", envelope.OpCode)
	}

	var payload SignalShutdownPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("Failed to parse shutdown payload: %v", err)
	}

	if payload.GraceSeconds != 60 {
		t.Errorf("Expected grace period of 60 seconds, got %d", payload.GraceSeconds)
	}
}

func TestVacateCommand_Override(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.Must(uuid.NewV4())
	matchID := MatchID{uuid.Must(uuid.NewV4()), "node1"}

	nk := &mockVacateNakamaModule{
		matchLabels: map[string]*MatchLabel{
			matchID.String(): {
				ID:    matchID,
				Owner: ownerID,
			},
		},
	}

	handler := &VacateCommandHandler{
		nk:     nk,
		logger: &testLogger{t: t},
	}

	i := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "match-id",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: matchID.String(),
					},
					{
						Name:  "override",
						Type:  discordgo.ApplicationCommandOptionBoolean,
						Value: true,
					},
				},
			},
		},
	}

	err := handler.HandleVacateCommand(ctx, nil, i, ownerID.String())

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if nk.signalData == "" {
		t.Fatal("Expected signal to be sent to match")
	}

	var envelope SignalEnvelope
	if err := json.Unmarshal([]byte(nk.signalData), &envelope); err != nil {
		t.Fatalf("Failed to parse signal envelope: %v", err)
	}

	if envelope.OpCode != SignalShutdown {
		t.Errorf("Expected SignalShutdown opcode, got %d", envelope.OpCode)
	}

	var payload SignalShutdownPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("Failed to parse shutdown payload: %v", err)
	}

	if payload.GraceSeconds != 20 {
		t.Errorf("Expected grace period of 20 seconds, got %d", payload.GraceSeconds)
	}
}

func TestVacateCommand_NotOwner(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.Must(uuid.NewV4())
	otherUserID := uuid.Must(uuid.NewV4())
	matchID := MatchID{uuid.Must(uuid.NewV4()), "node1"}

	nk := &mockVacateNakamaModule{
		matchLabels: map[string]*MatchLabel{
			matchID.String(): {
				ID:    matchID,
				Owner: ownerID,
			},
		},
	}

	handler := &VacateCommandHandler{
		nk:     nk,
		logger: &testLogger{t: t},
	}

	i := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "match-id",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: matchID.String(),
					},
				},
			},
		},
	}

	err := handler.HandleVacateCommand(ctx, nil, i, otherUserID.String())

	if err == nil {
		t.Fatal("Expected permission denied error, got nil")
	}

	if err.Error() != "only the reservation owner can vacate this server" {
		t.Errorf("Expected permission error, got: %v", err)
	}

	if nk.signalData != "" {
		t.Error("Expected no signal to be sent for non-owner")
	}
}

func TestVacateCommand_MatchNotFound(t *testing.T) {
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4())
	matchID := MatchID{uuid.Must(uuid.NewV4()), "node1"}

	nk := &mockVacateNakamaModule{
		matchLabels: map[string]*MatchLabel{},
	}

	handler := &VacateCommandHandler{
		nk:     nk,
		logger: &testLogger{t: t},
	}

	i := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionApplicationCommand,
			Data: discordgo.ApplicationCommandInteractionData{
				Options: []*discordgo.ApplicationCommandInteractionDataOption{
					{
						Name:  "match-id",
						Type:  discordgo.ApplicationCommandOptionString,
						Value: matchID.String(),
					},
				},
			},
		},
	}

	err := handler.HandleVacateCommand(ctx, nil, i, userID.String())

	if err == nil {
		t.Fatal("Expected error for nonexistent match, got nil")
	}
}
