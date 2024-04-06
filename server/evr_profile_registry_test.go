package server

import (
	"context"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func createTestDiscordGoSession(t *testing.T, logger *zap.Logger) *discordgo.Session {
	return &discordgo.Session{}
}

func createTestProfileRegistry(t *testing.T, logger *zap.Logger) (*ProfileRegistry, error) {
	runtimeLogger := NewRuntimeGoLogger(logger)
	metrics := &testMetrics{}
	config := NewConfig(logger)
	dg := createTestDiscordGoSession(t, logger)
	db := NewDB(t)
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	discordRegistry := NewLocalDiscordRegistry(context.Background(), nk, runtimeLogger, metrics, config, nil, dg)
	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, discordRegistry)

	return profileRegistry, nil
}

func TestProfileRegistry(t *testing.T) {
	consoleLogger := loggerForTest(t)

	profileRegistry, err := createTestProfileRegistry(t, consoleLogger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}
	_ = profileRegistry
}

func TestSetCosmeticDefaults(t *testing.T) {
	type args struct {
		s *evr.ServerProfile
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "SetCosmeticDefaults",
			args: args{
				s: &evr.ServerProfile{},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SetCosmeticDefaults(tt.args.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("SetCosmeticDefaults() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.args.s.UnlockedCosmetics.Arena.Banner0025 != true {
				t.Errorf("cosmetic should be unlocked: %v", tt.args.s.UnlockedCosmetics.Arena)
				return
			}
		})
	}
}

func TestGetSessionProfile(t *testing.T) {
	// Create a test session and login profile
	session := &sessionWS{
		userID: uuid.Must(uuid.NewV4()),
		logger: zap.NewNop(),
	}
	profile := &GameProfile{
		Client: evr.NewClientProfile(),
		Server: evr.NewServerProfile(),
		Login:  &evr.LoginProfile{},
	}

	loginProfile := evr.LoginProfile{
		PublisherLock: "publisherlock",
		LobbyVersion:  123456,
	}

	// Create a test EVR ID
	evrID, _ := evr.ParseEvrId("OVR_ORG-123412341234")

	// Create a test context with the EVR ID
	ctx := context.WithValue(context.Background(), ctxEvrIDKey{}, evrID)

	// Create a test ProfileRegistry
	profileRegistry, err := createTestProfileRegistry(t, loggerForTest(t))
	if err != nil {
		t.Fatalf("error creating test profile registry: %v", err)
	}

	// Store the profile
	profileRegistry.SetProfile(session.userID, profile)

	// Call the GetSessionProfile method
	p, err := profileRegistry.GetSessionProfile(ctx, session, loginProfile)
	if err != nil {
		t.Fatalf("GetSessionProfile returned an error: %v", err)
	}

	// Check the assignments to p.Server
	if p.Server.PublisherLock != "publisherlock" {
		t.Errorf("p.Server.PublisherLock = false, want true")
	}
	if p.Server.LobbyVersion != 123456 {
		t.Errorf("p.Server.LobbyVersion = %d, want %d", p.Server.LobbyVersion, 123456)
	}
	if p.Server.EchoUserIdToken != string(evrID.Token()) {
		t.Errorf("p.Server.EchoUserIdToken = %s, want %s", p.Server.EchoUserIdToken, evrID.Token())
	}
	if p.Server.Social != p.Client.Social {
		t.Errorf("p.Server.Social = %+v, want %+v", p.Server.Social, p.Client.Social)
	}
	if p.Server.CreateTime != time.Date(2023, 10, 31, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("p.Server.CreateTime = %d, want %d", p.Server.CreateTime, time.Date(2023, 10, 31, 0, 0, 0, 0, time.UTC).Unix())
	}
	if p.Server.LoginTime != time.Now().UTC().Unix() {
		t.Errorf("p.Server.LoginTime = %d, want %d", p.Server.LoginTime, time.Now().UTC().Unix())
	}
	if p.Server.UpdateTime != time.Now().UTC().Unix() {
		t.Errorf("p.Server.UpdateTime = %d, want %d", p.Server.UpdateTime, time.Now().UTC().Unix())
	}

	// Check the assignments to p.Login
	if p.Login != &loginProfile {
		t.Errorf("p.Login = %+v, want %+v", p.Login, &loginProfile)
	}
}
func TestUpdateEquippedItem(t *testing.T) {
	// Create a test profile
	profile := &GameProfile{
		Server: &evr.ServerProfile{
			EquippedCosmetics: evr.EquippedCosmetics{
				Instances: evr.CosmeticInstances{
					Unified: evr.UnifiedCosmeticInstance{
						Slots: evr.CosmeticLoadout{},
					},
				},
			},
			UnlockedCosmetics: evr.UnlockedCosmetics{
				Arena:  evr.ArenaUnlocks{},
				Combat: evr.CombatUnlocks{},
			},
			UpdateTime: time.Now().UTC().Unix(),
		},
	}

	// Create a test ProfileRegistry
	profileRegistry, err := createTestProfileRegistry(t, loggerForTest(t))
	if err != nil {
		t.Fatalf("error creating test profile registry: %v", err)
	}
	profile.Server.UnlockedCosmetics.Arena.Tint0015 = true
	// Call the UpdateEquippedItem method
	err = profileRegistry.UpdateEquippedItem(profile, "emote", "rwd_tint_0015")
	if err != nil {
		t.Fatalf("UpdateEquippedItem returned an error: %v", err)
	}

	assert.Equal(t, "rwd_tint_0015", profile.Server.EquippedCosmetics.Instances.Unified.Slots.Emote)
	// Verify the changes in the profile
	// TODO: Add assertions here to verify the changes in the profile
}
