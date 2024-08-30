package server

import (
	"context"
	"sync"
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

	tracker := &testTracker{}
	db := NewDB(t)
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, tracker)

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
			err := SetCosmeticDefaults(tt.args.s, false)
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
	profile := GameProfileData{
		Client: evr.NewClientProfile(),
		Server: evr.NewServerProfile(),
		Login:  evr.LoginProfile{},
	}

	loginProfile := evr.LoginProfile{
		PublisherLock: "publisherlock",
		LobbyVersion:  123456,
	}

	// Create a test EVR ID
	evrID, _ := evr.ParseEvrId("OVR_ORG-123412341234")

	// Create a test context with the EVR ID
	ctx := context.WithValue(context.Background(), ctxEvrIDKey{}, *evrID)

	// Create a test ProfileRegistry
	profileRegistry, err := createTestProfileRegistry(t, loggerForTest(t))
	if err != nil {
		t.Fatalf("error creating test profile registry: %v", err)
	}

	// Store the profile
	profileRegistry.SaveAndCache(ctx, session.userID, &profile)

	// Call the GetSessionProfile method

	p, err := profileRegistry.GameProfile(ctx, logger, session.userID, loginProfile, *evrID)
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
	if p.Server.EvrID != *evrID {
		t.Errorf("p.Server.EchoUserIdToken = %s, want %s", p.Server.EvrID.Token(), evrID.Token())
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
	if p.Login != loginProfile {
		t.Errorf("p.Login = %+v, want %+v", p.Login, &loginProfile)
	}
}
func TestUpdateEquippedItem(t *testing.T) {
	// Create a test profile
	profile := &GameProfileData{
		Server: evr.ServerProfile{
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

func TestGameProfileData_UpdateUnlocks(t *testing.T) {
	type fields struct {
		RWMutex sync.RWMutex
		Login   evr.LoginProfile
		Client  evr.ClientProfile
		Server  evr.ServerProfile
	}
	type args struct {
		unlocks evr.UnlockedCosmetics
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *GameProfileData
		wantErr bool
	}{
		{
			"UpdateUnlocks",
			fields{
				Client: evr.ClientProfile{},
				Server: evr.ServerProfile{},
			},
			args{
				evr.UnlockedCosmetics{
					Arena: evr.ArenaUnlocks{
						Banner0025: true,
					},
					Combat: evr.CombatUnlocks{
						DecalCombatFlamingoA: true,
					},
				},
			},
			&GameProfileData{
				Client: evr.ClientProfile{
					NewUnlocks: []int64{
						4066825755375865053,
						-827271357203098561,
					},
				},
				Server: evr.ServerProfile{
					UnlockedCosmetics: evr.UnlockedCosmetics{
						Arena: evr.ArenaUnlocks{
							Banner0025: true,
						},
						Combat: evr.CombatUnlocks{
							DecalCombatFlamingoA: true,
						},
					},
				},
			},

			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := GameProfileData{
				Login:  tt.fields.Login,
				Client: tt.fields.Client,
				Server: tt.fields.Server,
			}
			if err := r.UpdateUnlocks(tt.args.unlocks); (err != nil) != tt.wantErr {
				t.Errorf("GameProfileData.UpdateUnlocks() error = %v, wantErr %v", err, tt.wantErr)
			}

			assert.Equal(t, tt.want.Server, r.Server)
			assert.Equal(t, tt.want.Client, r.Client)
		})
	}
}

type testProfileRegistry struct {
	ProfileRegistry
	mockLoadFn func(userID uuid.UUID) (*GameProfileData, bool)
}

func (r *testProfileRegistry) Load(userID uuid.UUID) (*GameProfileData, bool) {
	return r.mockLoadFn(userID)
}
