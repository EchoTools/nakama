package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const (
	// Redis keys for session tracking
	RedisSessionMessagesKey = "discord:session_messages"
	// Update interval for session messages
	SessionUpdateInterval = 15 * time.Second
	// Maximum players for Arena and Combat Public modes
	MaxPlayersArenaCombatPublic = 15
	// Default Discord icon URL
	DefaultDiscordIconURL = "https://cdn.discordapp.com/embed/avatars/0.png"
)

// SessionMessageTracker tracks active session messages for updates
type SessionMessageTracker struct {
	SessionID    string    `json:"session_id"`
	MessageID    string    `json:"message_id"`
	ChannelID    string    `json:"channel_id"`
	GuildID      string    `json:"guild_id"`
	LastUpdated  time.Time `json:"last_updated"`
	CreatedAt    time.Time `json:"created_at"`
}

// SessionsChannelManager manages Discord session messages
type SessionsChannelManager struct {
	sync.RWMutex
	
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	dg     *discordgo.Session
	
	// Track active session messages in memory for quick access
	activeMessages map[string]*SessionMessageTracker
	
	// Channel for stopping the update goroutine
	stopCh chan struct{}
	
	// Wait group for graceful shutdown
	wg sync.WaitGroup
}

// NewSessionsChannelManager creates a new sessions channel manager
func NewSessionsChannelManager(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dg *discordgo.Session) *SessionsChannelManager {
	manager := &SessionsChannelManager{
		ctx:            ctx,
		logger:         logger.WithField("component", "sessions_channel"),
		nk:             nk,
		dg:             dg,
		activeMessages: make(map[string]*SessionMessageTracker),
		stopCh:         make(chan struct{}),
	}
	
	// Load existing tracked messages from Redis
	manager.loadTrackedMessages()
	
	// Start the update goroutine
	manager.wg.Add(1)
	go manager.updateLoop()
	
	return manager
}

// Stop gracefully stops the session manager
func (sm *SessionsChannelManager) Stop() {
	close(sm.stopCh)
	sm.wg.Wait()
}

// loadTrackedMessages loads session message trackers from Redis
func (sm *SessionsChannelManager) loadTrackedMessages() {
	storageObjects, err := sm.nk.StorageRead(sm.ctx, []*runtime.StorageRead{
		{
			Collection: "discord",
			Key:        RedisSessionMessagesKey,
		},
	})
	if err != nil {
		sm.logger.Error("Failed to load session message trackers from storage", zap.Error(err))
		return
	}
	
	if len(storageObjects) == 0 {
		return
	}
	
	var trackers map[string]*SessionMessageTracker
	if err := json.Unmarshal([]byte(storageObjects[0].Value), &trackers); err != nil {
		sm.logger.Error("Failed to unmarshal session message trackers", zap.Error(err))
		return
	}
	
	sm.Lock()
	defer sm.Unlock()
	sm.activeMessages = trackers
	
	sm.logger.Info("Loaded session message trackers", zap.Int("count", len(trackers)))
}

// saveTrackedMessages saves session message trackers to Redis
func (sm *SessionsChannelManager) saveTrackedMessages() {
	sm.RLock()
	trackers := make(map[string]*SessionMessageTracker)
	for k, v := range sm.activeMessages {
		trackers[k] = v
	}
	sm.RUnlock()
	
	data, err := json.Marshal(trackers)
	if err != nil {
		sm.logger.Error("Failed to marshal session message trackers", zap.Error(err))
		return
	}
	
	_, err = sm.nk.StorageWrite(sm.ctx, []*runtime.StorageWrite{
		{
			Collection:      "discord",
			Key:             RedisSessionMessagesKey,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
	if err != nil {
		sm.logger.Error("Failed to save session message trackers to storage", zap.Error(err))
	}
}

// PostSessionMessage posts a new session message to Discord
func (sm *SessionsChannelManager) PostSessionMessage(label *MatchLabel, guildGroup *GuildGroup) error {
	// Create the embed and components
	embed, components := sm.createSessionEmbed(label, guildGroup)
	
	var postedMessages []string
	
	// Post to guild sessions channel if configured
	if guildGroup.SessionsChannelID != "" {
		message, err := sm.dg.ChannelMessageSendComplex(guildGroup.SessionsChannelID, &discordgo.MessageSend{
			Embeds:     embed,
			Components: components,
		})
		if err != nil {
			sm.logger.Error("Failed to send session message to guild channel", 
				zap.String("channel_id", guildGroup.SessionsChannelID),
				zap.Error(err))
		} else {
			// Track the message for updates
			tracker := &SessionMessageTracker{
				SessionID:   label.ID.String(),
				MessageID:   message.ID,
				ChannelID:   guildGroup.SessionsChannelID,
				GuildID:     guildGroup.GuildID,
				LastUpdated: time.Now(),
				CreatedAt:   time.Now(),
			}
			
			sm.Lock()
			sm.activeMessages[label.ID.String()] = tracker
			sm.Unlock()
			
			postedMessages = append(postedMessages, fmt.Sprintf("guild:%s", message.ID))
		}
	}
	
	// Post to service sessions channel if configured
	if serviceChannelID := ServiceSettings().ServiceSessionsChannelID; serviceChannelID != "" {
		message, err := sm.dg.ChannelMessageSendComplex(serviceChannelID, &discordgo.MessageSend{
			Embeds:     embed,
			Components: components,
		})
		if err != nil {
			sm.logger.Error("Failed to send session message to service channel", 
				zap.String("channel_id", serviceChannelID),
				zap.Error(err))
		} else {
			// Track the service message separately (use different key)
			tracker := &SessionMessageTracker{
				SessionID:   label.ID.String(),
				MessageID:   message.ID,
				ChannelID:   serviceChannelID,
				GuildID:     "service",
				LastUpdated: time.Now(),
				CreatedAt:   time.Now(),
			}
			
			sm.Lock()
			sm.activeMessages[label.ID.String()+"_service"] = tracker
			sm.Unlock()
			
			postedMessages = append(postedMessages, fmt.Sprintf("service:%s", message.ID))
		}
	}
	
	if len(postedMessages) == 0 {
		// No sessions channels configured
		return nil
	}
	
	// Save to storage
	sm.saveTrackedMessages()
	
	sm.logger.Info("Posted session messages", 
		zap.String("session_id", label.ID.String()),
		zap.Strings("messages", postedMessages))
	
	return nil
}

// createSessionEmbed creates the Discord embed and components for a session
func (sm *SessionsChannelManager) createSessionEmbed(label *MatchLabel, guildGroup *GuildGroup) ([]*discordgo.MessageEmbed, []discordgo.MessageComponent) {
	sessionUUID := strings.ToUpper(label.ID.UUID.String())
	
	// Calculate duration (this will be updated in the loop)
	duration := time.Since(label.StartTime)
	durationStr := fmt.Sprintf("%.0f minutes", duration.Minutes())
	
	// Get player count
	playerCount := len(label.Players)
	maxPlayers := 8 // Default for most game modes
	if label.Mode == evr.ModeArenaPublic || label.Mode == evr.ModeCombatPublic {
		maxPlayers = MaxPlayersArenaCombatPublic
	}
	
	// Determine server location (approximate)
	serverLocation := "Unknown"
	if label.GameServer != nil {
		// Use the endpoint string for server location
		serverLocation = label.GameServer.Endpoint.String()
	}
	
	// Get operator information
	operatedBy := "Unknown"
	if label.SpawnedBy != "" {
		// Try to get Discord ID for the spawned by user
		operatedBy = label.SpawnedBy
	}
	
	// Create the main embed
	mainEmbed := &discordgo.MessageEmbed{
		Title: fmt.Sprintf("%s Session", label.Mode.String()),
		Color: 2326507, // Blue color
		Author: &discordgo.MessageEmbedAuthor{
			Name:    sessionUUID,
			IconURL: DefaultDiscordIconURL,
			URL:     fmt.Sprintf("https://echo.taxi/spark://j/%s", strings.ToLower(sessionUUID)),
		},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Start Time", Value: fmt.Sprintf("<t:%d:R>", label.StartTime.Unix()), Inline: true},
			{Name: "Duration", Value: durationStr, Inline: true},
			{Name: "Size", Value: fmt.Sprintf("%d/%d", playerCount, maxPlayers), Inline: true},
			{Name: "Server Location", Value: serverLocation, Inline: true},
			{Name: "Operated By", Value: operatedBy, Inline: true},
		},
	}
	
	// Add endpoint if available
	if label.GameServer != nil {
		endpoint := fmt.Sprintf("`%s`", label.GameServer.Endpoint.String())
		mainEmbed.Fields = append(mainEmbed.Fields, &discordgo.MessageEmbedField{
			Name:   "Endpoint",
			Value:  endpoint,
			Inline: true,
		})
	}
	
	embeds := []*discordgo.MessageEmbed{mainEmbed}
	
	// Create match details embed if there are players
	if len(label.Players) > 0 {
		matchEmbed := sm.createMatchDetailsEmbed(label)
		embeds = append(embeds, matchEmbed)
	}
	
	// Create interactive components
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Label:    "Taxi...",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("taxi_%s", sessionUUID),
					Emoji: &discordgo.ComponentEmoji{
						Name: "🚕",
					},
				},
				discordgo.Button{
					Label:    "Join...",
					Style:    discordgo.SecondaryButton,
					CustomID: fmt.Sprintf("join_%s", sessionUUID),
					Emoji: &discordgo.ComponentEmoji{
						Name: "🔗",
					},
				},
			},
		},
	}
	
	return embeds, components
}

// createMatchDetailsEmbed creates the match details embed with scores and players
func (sm *SessionsChannelManager) createMatchDetailsEmbed(label *MatchLabel) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Fields: make([]*discordgo.MessageEmbedField, 0),
	}
	
	// Add match status/scores if available
	if label.LobbyType != UnassignedLobby {
		if label.GameState != nil {
			// Add score information
			description := fmt.Sprintf("**%d - %d**", label.GameState.BlueScore, label.GameState.OrangeScore)
			if label.GameState.RoundClock != nil {
				remainingTime := label.GameState.RoundClock.Duration - label.GameState.RoundClock.Elapsed
				if remainingTime > 0 {
					roundLabel := "Current Round"
					description += fmt.Sprintf("\n%s (~%dm%02ds remaining)", 
						roundLabel, 
						int(remainingTime.Minutes()), 
						int(remainingTime.Seconds())%60)
				}
			}
			embed.Description = description
		}
		
		// Group players by team
		blueTeam := make([]string, 0)
		orangeTeam := make([]string, 0)
		spectators := make([]string, 0)
		
		for _, player := range label.Players {
			playerStr := fmt.Sprintf("`%3d ms` %s", player.PingMillis, player.DisplayName)
			
			switch player.Team {
			case BlueTeam:
				blueTeam = append(blueTeam, playerStr)
			case OrangeTeam:
				orangeTeam = append(orangeTeam, playerStr)
			default:
				spectators = append(spectators, player.DisplayName)
			}
		}
		
		// Add team fields
		if len(blueTeam) > 0 {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Blue",
				Value:  strings.Join(blueTeam, "\n"),
				Inline: true,
			})
		}
		
		if len(orangeTeam) > 0 {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Orange",
				Value:  strings.Join(orangeTeam, "\n"),
				Inline: true,
			})
		}
		
		if len(spectators) > 0 {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Spectators",
				Value:  strings.Join(spectators, ", "),
				Inline: false,
			})
		}
	}
	
	return embed
}

// updateLoop runs the periodic update of session messages
func (sm *SessionsChannelManager) updateLoop() {
	defer sm.wg.Done()
	
	ticker := time.NewTicker(SessionUpdateInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-sm.stopCh:
			return
		case <-ticker.C:
			sm.updateSessionMessages()
		}
	}
}

// updateSessionMessages updates all tracked session messages
func (sm *SessionsChannelManager) updateSessionMessages() {
	sm.RLock()
	trackers := make([]*SessionMessageTracker, 0, len(sm.activeMessages))
	for _, tracker := range sm.activeMessages {
		trackers = append(trackers, tracker)
	}
	sm.RUnlock()
	
	for _, tracker := range trackers {
		if err := sm.updateSessionMessage(tracker); err != nil {
			sm.logger.Error("Failed to update session message",
				zap.String("session_id", tracker.SessionID),
				zap.String("message_id", tracker.MessageID),
				zap.Error(err))
		}
	}
}

// updateSessionMessage updates a specific session message
func (sm *SessionsChannelManager) updateSessionMessage(tracker *SessionMessageTracker) error {
	// Get the current match state
	matches, err := sm.nk.MatchList(sm.ctx, 1, true, "", nil, nil, fmt.Sprintf("+label.id:%s", tracker.SessionID))
	if err != nil {
		return fmt.Errorf("failed to find match: %w", err)
	}
	
	if len(matches) == 0 {
		// Match no longer exists, remove from tracking
		sm.Lock()
		delete(sm.activeMessages, tracker.SessionID)
		sm.Unlock()
		sm.saveTrackedMessages()
		return nil
	}
	
	// Parse the match label
	label := &MatchLabel{}
	if err := json.Unmarshal([]byte(matches[0].GetLabel().GetValue()), label); err != nil {
		return fmt.Errorf("failed to unmarshal match label: %w", err)
	}
	
	// Get guild group for the match
	guildGroup, err := sm.getGuildGroupForMatch(label)
	if err != nil {
		return fmt.Errorf("failed to get guild group: %w", err)
	}
	
	// Create updated embed and components
	embeds, components := sm.createSessionEmbed(label, guildGroup)
	
	// Update the Discord message
	_, err = sm.dg.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    tracker.ChannelID,
		ID:         tracker.MessageID,
		Embeds:     &embeds,
		Components: &components,
	})
	if err != nil {
		// If message edit fails, the message might have been deleted
		// Remove from tracking
		sm.Lock()
		delete(sm.activeMessages, tracker.SessionID)
		sm.Unlock()
		sm.saveTrackedMessages()
		return fmt.Errorf("failed to edit message (removing from tracking): %w", err)
	}
	
	// Update tracker
	tracker.LastUpdated = time.Now()
	
	return nil
}

// getGuildGroupForMatch gets the guild group for a match label
func (sm *SessionsChannelManager) getGuildGroupForMatch(label *MatchLabel) (*GuildGroup, error) {
	// Get the guild group registry from the global app bot
	if appBot := globalAppBot.Load(); appBot != nil {
		if guildGroup := appBot.guildGroupRegistry.Get(label.GroupID.String()); guildGroup != nil {
			return guildGroup, nil
		}
	}
	
	// Fallback - return an error if we can't find the guild group
	return nil, fmt.Errorf("guild group not found for group ID: %s", label.GroupID.String())
}

// RemoveSessionMessage removes a session from tracking
func (sm *SessionsChannelManager) RemoveSessionMessage(sessionID string) {
	sm.Lock()
	defer sm.Unlock()
	
	var removedTrackers []string
	
	// Remove the primary guild message
	if tracker, exists := sm.activeMessages[sessionID]; exists {
		delete(sm.activeMessages, sessionID)
		removedTrackers = append(removedTrackers, fmt.Sprintf("guild:%s", tracker.MessageID))
	}
	
	// Remove the service message if it exists
	serviceKey := sessionID + "_service"
	if tracker, exists := sm.activeMessages[serviceKey]; exists {
		delete(sm.activeMessages, serviceKey)
		removedTrackers = append(removedTrackers, fmt.Sprintf("service:%s", tracker.MessageID))
	}
	
	if len(removedTrackers) > 0 {
		sm.saveTrackedMessages()
		
		sm.logger.Info("Removed session messages from tracking",
			zap.String("session_id", sessionID),
			zap.Strings("removed", removedTrackers))
	}
}