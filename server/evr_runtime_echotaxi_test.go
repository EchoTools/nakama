package server

import (
	"context"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func TestTaxiLinkRegistry_Process(t *testing.T) {
	type fields struct {
		Mutex             sync.Mutex
		ctx               context.Context
		ctxCancelFn       context.CancelFunc
		nk                runtime.NakamaModule
		logger            runtime.Logger
		dg                *discordgo.Session
		node              string
		tracked           map[MatchToken][]TrackID
		reactOnlyChannels sync.Map
	}
	type args struct {
		channelID string
		messageID string
		content   string
		reactOnly bool
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &TaxiLinkRegistry{
				ctx:         tt.fields.ctx,
				ctxCancelFn: tt.fields.ctxCancelFn,
				nk:          tt.fields.nk,
				logger:      tt.fields.logger,
				dg:          tt.fields.dg,
				node:        tt.fields.node,
			}
			if err := e.Process(tt.args.channelID, tt.args.messageID, tt.args.content, tt.args.reactOnly); (err != nil) != tt.wantErr {
				t.Errorf("TaxiLinkRegistry.Process() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_findMatchID(t *testing.T) {
	type args struct {
		content string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			"Spark Link",
			args{
				content: "spark://j/C74B6B7A-B77F-460E-BAAC-E2F1437001D9",
			},
			"C74B6B7A-B77F-460E-BAAC-E2F1437001D9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findMatchID(tt.args.content); got != tt.want {
				t.Errorf("findMatchID() = %v, want %v", got, tt.want)
			}
		})
	}
}

func createTestTaxiBot(t *testing.T, logger *zap.Logger) (*TaxiBot, error) {
	ctx := context.Background()
	runtimeLogger := NewRuntimeGoLogger(logger)
	config := NewConfig(logger)
	db := NewDB(t)
	dg := createTestDiscordGoSession(t, logger)
	node := config.GetName()
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	linkRegistry := NewTaxiLinkRegistry(context.Background(), runtimeLogger, nk, config, dg)
	taxi := NewTaxiBot(ctx, runtimeLogger, nk, node, linkRegistry, dg)

	return taxi, nil
}

func TestTaxiBot_Deconflict(t *testing.T) {
	testTaxiBot, err := createTestTaxiBot(t, logger)
	if err != nil {
		t.Errorf("Error creating TaxiBot: %v", err)
	}

	// Create a message from a user
	userMessage := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "messageID",
			GuildID:   "guildID",
			ChannelID: "channelID",
			Content:   "spark://j/C74B6B7A-B77F-460E-BAAC-E2F1437001D9",
		},
	}

	// Create a message from sprocklink
	sprocklinkMessage := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "SPRmessageID",
			GuildID:   "guildID",
			ChannelID: "channelID",
			Content:   "https://sprock.io/spark://j/C74B6B7A-B77F-460E-BAAC-E2F1437001D9",
		},
	}
	_, _, _ = userMessage, sprocklinkMessage, testTaxiBot

	// Call the deconflict with the the userMessage first, then immediately call the deconflict with the sprocklinkMessage

	/* Scenario 1
	EchoTaxi has not seen any spark links in the channel before.
	It shoudl wait 2 seconds before responding. If Sprocklink responded in that time, then
	EchoTaxi should not respond.
	*/

}
