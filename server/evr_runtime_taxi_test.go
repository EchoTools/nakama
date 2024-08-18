package server

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

func createTestTaxiBot(t *testing.T, logger *zap.Logger) (*TaxiBot, error) {
	ctx := context.Background()
	runtimeLogger := NewRuntimeGoLogger(logger)
	config := NewConfig(logger)
	db := NewDB(t)
	dg := createTestDiscordGoSession(t, logger)
	node := config.GetName()
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	taxi := NewTaxiBot(ctx, runtimeLogger, nk, db, node, dg)

	return taxi, nil
}

func TestTaxiBot_Deconflict(t *testing.T) {
	t.Skip("Skipping test for now")
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

func Test_extractMatchComponents(t *testing.T) {
	type args struct {
		content string
	}
	tests := []struct {
		name              string
		args              args
		wantHttpPrefix    string
		wantApplinkPrefix string
		wantMatchID       string
	}{
		{
			name:              "Test Spark Link prefixed with echo taxi",
			args:              args{content: "https://echo.taxi/spark://j/c74b6b7a-b77f-460e-baac-e2f1437001d9"},
			wantHttpPrefix:    "https://echo.taxi/",
			wantApplinkPrefix: "spark://j/",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
		{
			name:              "Test Spark Link prefixed with sprock.io",
			args:              args{content: "https://sprock.io/spark://j/c74b6b7a-b77f-460e-baac-e2f1437001d9"},
			wantHttpPrefix:    "https://sprock.io/",
			wantApplinkPrefix: "spark://j/",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
		{
			name:              "Test Spark Link with lowercase",
			args:              args{content: "spark://j/c74b6b7a-b77f-460e-baac-e2f1437001d9"},
			wantHttpPrefix:    "",
			wantApplinkPrefix: "spark://j/",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
		{
			name:              "Test Spark Link (Choose)",
			args:              args{content: "spark://c/c74b6b7a-b77F-460E-BAAC-E2F1437001D9"},
			wantHttpPrefix:    "",
			wantApplinkPrefix: "spark://c/",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
		{
			name:              "Test Spark Link (Join)",
			args:              args{content: "spark://j/C74B6B7A-B77F-460E-BAAC-E2F1437001D9"},
			wantHttpPrefix:    "",
			wantApplinkPrefix: "spark://j/",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
		{
			name:              "Test Spark Link (Spectate)",
			args:              args{content: "spark://s/c74b6b7a-b77F-460E-BAAC-E2F1437001D9"},
			wantHttpPrefix:    "",
			wantApplinkPrefix: "spark://s/",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
		{
			name:              "Test Aether link",
			args:              args{content: "Aether://C74B6B7A-B77F-460E-BAAC-E2F1437001D9"},
			wantHttpPrefix:    "",
			wantApplinkPrefix: "Aether://",
			wantMatchID:       "c74b6b7a-b77f-460e-baac-e2f1437001d9.test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matchLink := (&TaxiBot{node: "test"}).matchLinkFromStringOrNil(tt.args.content)
			if matchLink.HTTPPrefix != tt.wantHttpPrefix {
				t.Errorf("extractMatchComponents() gotHttpPrefix = %v, want %v", matchLink.HTTPPrefix, tt.wantHttpPrefix)
			}
			if matchLink.AppLinkPrefix != tt.wantApplinkPrefix {
				t.Errorf("extractMatchComponents() gotApplinkPrefix = %v, want %v", matchLink.AppLinkPrefix, tt.wantApplinkPrefix)
			}
			if matchLink.MatchID != MatchIDFromStringOrNil(tt.wantMatchID) {
				t.Errorf("extractMatchComponents() gotMatchID = %v, want %v", matchLink.MatchID.String(), tt.wantMatchID)
			}
		})
	}
}
