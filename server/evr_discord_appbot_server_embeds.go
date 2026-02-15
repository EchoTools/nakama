package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	EmbedColorIdling              = 0x808080 // Gray for idling
	EmbedColorActive              = 0x00FF00 // Green for active match
	EmbedColorOver                = 0xFF0000 // Red for match over
	EmbedUpdatePeriod             = 15 * time.Second
	EmbedCleanupDelay             = 1 * time.Hour
	MaxDescriptionFieldLength     = 1024 // Discord embed field value limit
)

// ServerEmbedTracker tracks active server embed messages for auto-updates
type ServerEmbedTracker struct {
	sync.RWMutex
	embeds map[string]*ServerEmbedInfo // key: channelID
}

type ServerEmbedInfo struct {
	sync.RWMutex
	ChannelID     string
	Region        string
	MessageIDs    map[string]string // matchID -> messageID
	LastUpdate    time.Time
	StopChan      chan struct{}
	CompletedAt   map[string]time.Time // matchID -> completion time for cleanup
}

var globalEmbedTracker = &ServerEmbedTracker{
	embeds: make(map[string]*ServerEmbedInfo),
}

func (d *DiscordAppBot) handleShowServerEmbeds(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, userID, groupID, region string) error {
	// List all matches (reduced limit for performance)
	matches, err := d.nk.MatchList(ctx, MatchListLimit, true, "", nil, nil, "*")
	if err != nil {
		return d.editDeferredResponse(s, i, fmt.Sprintf("❌ Failed to list matches: %v", err))
	}

	// Filter matches by region
	var regionMatches []*MatchLabel
	for _, match := range matches {
		labelStr := ""
		if match.Label != nil {
			labelStr = match.Label.Value
		}
		label, err := MatchLabelFromString(labelStr)
		if err != nil {
			logger.Warn("Failed to parse match label", zap.Error(err))
			continue
		}

		// Check if match belongs to this guild (require non-nil GroupID)
		if label.GroupID == nil || label.GroupID.String() != groupID {
			continue
		}

		// Check if match is in the requested region
		if label.GameServer != nil {
			matchRegion := label.GameServer.LocationRegionCode(false, false)
			if matchRegion == region {
				regionMatches = append(regionMatches, label)
			}
		}
	}

	if len(regionMatches) == 0 {
		return d.editDeferredResponse(s, i, fmt.Sprintf("No matches found in region `%s`", region))
	}

	// Stop any existing embed tracker for this channel and delete old embeds
	channelID := i.ChannelID
	var prevMessageIDs []string
	globalEmbedTracker.Lock()
	if existing, ok := globalEmbedTracker.embeds[channelID]; ok {
		for _, msgID := range existing.MessageIDs {
			prevMessageIDs = append(prevMessageIDs, msgID)
		}
		close(existing.StopChan)
		delete(globalEmbedTracker.embeds, channelID)
	}
	globalEmbedTracker.Unlock()

	// Delete any previous embeds for this channel to avoid stale status messages
	for _, msgID := range prevMessageIDs {
		if err := s.ChannelMessageDelete(channelID, msgID); err != nil {
			logger.Warn("Failed to delete previous server status embed",
				zap.Error(err),
				zap.String("channelID", channelID),
				zap.String("messageID", msgID),
			)
		}
	}

	// Create new tracker
	embedInfo := &ServerEmbedInfo{
		ChannelID:   channelID,
		Region:      region,
		MessageIDs:  make(map[string]string),
		LastUpdate:  time.Now(),
		StopChan:    make(chan struct{}),
		CompletedAt: make(map[string]time.Time),
	}

	// Post initial embeds for each match
	for _, label := range regionMatches {
		embed := d.createServerEmbed(label)
		msg, err := s.ChannelMessageSendEmbed(channelID, embed)
		if err != nil {
			logger.Warn("Failed to send embed", zap.Error(err), zap.String("matchID", label.ID.String()))
			continue
		}
		embedInfo.MessageIDs[label.ID.String()] = msg.ID
	}

	// Store tracker
	globalEmbedTracker.Lock()
	globalEmbedTracker.embeds[channelID] = embedInfo
	globalEmbedTracker.Unlock()

	// Start background updater
	go d.updateServerEmbedsLoop(ctx, logger, s, embedInfo, groupID)

	// Edit the deferred response to confirm
	return d.editDeferredResponse(s, i, fmt.Sprintf("Showing %d server(s) in region `%s`. Updates every %d seconds.", len(regionMatches), region, int(EmbedUpdatePeriod.Seconds())))
}

func (d *DiscordAppBot) updateServerEmbedsLoop(ctx context.Context, logger runtime.Logger, s *discordgo.Session, embedInfo *ServerEmbedInfo, groupID string) {
	ticker := time.NewTicker(EmbedUpdatePeriod)
	defer ticker.Stop()

	for {
		select {
		case <-embedInfo.StopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.updateServerEmbeds(ctx, logger, s, embedInfo, groupID)
		}
	}
}

func (d *DiscordAppBot) updateServerEmbeds(ctx context.Context, logger runtime.Logger, s *discordgo.Session, embedInfo *ServerEmbedInfo, groupID string) {
	// List current matches without holding the embedInfo lock to avoid
	// blocking other operations on this tracker during potentially slow I/O.
	matches, err := d.nk.MatchList(ctx, MatchListLimit, true, "", nil, nil, "*")
	if err != nil {
		logger.Warn("Failed to list matches for embed update", zap.Error(err))
		return
	}

	// Now lock embedInfo while we work with its shared state.
	embedInfo.Lock()
	defer embedInfo.Unlock()

	// Track which matches we found
	foundMatches := make(map[string]*MatchLabel)

	// Update existing embeds and create new ones
	for _, match := range matches {
		labelStr := ""
		if match.Label != nil {
			labelStr = match.Label.Value
		}
		label, err := MatchLabelFromString(labelStr)
		if err != nil {
			continue
		}

		// Filter by guild and region: require a non-nil GroupID that matches this guild
		if label.GroupID == nil || label.GroupID.String() != groupID {
			continue
		}

		if label.GameServer != nil {
			matchRegion := label.GameServer.LocationRegionCode(false, false)
			if matchRegion == embedInfo.Region {
				foundMatches[label.ID.String()] = label
			}
		}
	}

	// Update or create embeds for found matches
	for matchID, label := range foundMatches {
		embed := d.createServerEmbed(label)
		
		if messageID, exists := embedInfo.MessageIDs[matchID]; exists {
			// Update existing embed
			_, err := s.ChannelMessageEditEmbed(embedInfo.ChannelID, messageID, embed)
			if err != nil {
				logger.Warn("Failed to update embed", zap.Error(err), zap.String("matchID", matchID))
			}
		} else {
			// Create new embed for new match
			msg, err := s.ChannelMessageSendEmbed(embedInfo.ChannelID, embed)
			if err != nil {
				logger.Warn("Failed to send new embed", zap.Error(err), zap.String("matchID", matchID))
				continue
			}
			embedInfo.MessageIDs[matchID] = msg.ID
		}
	}

	// Mark completed matches and schedule cleanup
	now := time.Now()
	for matchID, messageID := range embedInfo.MessageIDs {
		if _, found := foundMatches[matchID]; !found {
			// Match no longer exists - mark as over if not already
			if _, alreadyCompleted := embedInfo.CompletedAt[matchID]; !alreadyCompleted {
				embedInfo.CompletedAt[matchID] = now
				
				// Update embed to show "OVER" status
				overEmbed := &discordgo.MessageEmbed{
					Title:       "Match Over",
					Description: "This match has ended.",
					Color:       EmbedColorOver,
					Timestamp:   now.Format(time.RFC3339),
				}
				_, err := s.ChannelMessageEditEmbed(embedInfo.ChannelID, messageID, overEmbed)
				if err != nil {
					logger.Warn("Failed to mark embed as over", zap.Error(err), zap.String("matchID", matchID))
				}
			}
		}
	}

	// Clean up old completed embeds (after 1 hour)
	for matchID, completedAt := range embedInfo.CompletedAt {
		if time.Since(completedAt) > EmbedCleanupDelay {
			if messageID, exists := embedInfo.MessageIDs[matchID]; exists {
				err := s.ChannelMessageDelete(embedInfo.ChannelID, messageID)
				if err != nil {
					logger.Warn("Failed to delete old embed", zap.Error(err), zap.String("matchID", matchID))
				}
				delete(embedInfo.MessageIDs, matchID)
				delete(embedInfo.CompletedAt, matchID)
			}
		}
	}

	embedInfo.LastUpdate = now
}

func (d *DiscordAppBot) createServerEmbed(label *MatchLabel) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title: fmt.Sprintf("Match: %s", label.Mode.String()),
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Match ID",
				Value:  fmt.Sprintf("[`%s`](https://echo.taxi/spark://c/%s)", label.ID.UUID.String(), strings.ToUpper(label.ID.UUID.String())),
				Inline: false,
			},
		},
		Timestamp: time.Now().Format(time.RFC3339),
	}

	// Region
	if label.GameServer != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Region",
			Value:  label.GameServer.LocationRegionCode(true, true),
			Inline: true,
		})
	}

	// Guild
	if label.GroupID != nil {
		groupID := label.GroupID.String()
		guildID := d.cache.GroupIDToGuildID(groupID)
		if guildID != "" {
			if guild, err := d.dg.Guild(guildID); err == nil {
				embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
					Name:   "Guild",
					Value:  guild.Name,
					Inline: true,
				})
			}
		}
	}

	// Created By
	if label.SpawnedBy != "" {
		discordID := d.cache.UserIDToDiscordID(label.SpawnedBy)
		if discordID != "" {
			embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
				Name:   "Created By",
				Value:  fmt.Sprintf("<@%s>", discordID),
				Inline: true,
			})
		}
	}

	// Created At
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "Created At",
		Value:  fmt.Sprintf("<t:%d:R>", label.CreatedAt.Unix()),
		Inline: true,
	})

	// Start Time / Expiry
	if label.Started() {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Started At",
			Value:  fmt.Sprintf("<t:%d:R>", label.StartTime.Unix()),
			Inline: true,
		})
		embed.Color = EmbedColorActive
	} else {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Expires At",
			Value:  fmt.Sprintf("<t:%d:R>", label.StartTime.Unix()),
			Inline: true,
		})
		embed.Color = EmbedColorIdling
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Status",
			Value:  "⏸️ **IDLING** (waiting to start)",
			Inline: false,
		})
	}

	// Match Duration
	if label.Started() {
		duration := time.Since(label.StartTime)
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Match Duration",
			Value:  fmt.Sprintf("%dm %ds", int(duration.Minutes()), int(duration.Seconds())%60),
			Inline: true,
		})
	}

	// Game State Info
	if label.GameState != nil && label.GameState.SessionScoreboard != nil {
		// Round Duration
		roundDuration := label.GameState.SessionScoreboard.Elapsed()
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Current Round",
			Value:  fmt.Sprintf("%dm %ds", int(roundDuration.Minutes()), int(roundDuration.Seconds())%60),
			Inline: true,
		})
	}

	// Description
	if label.Description != "" {
		desc := label.Description
		// Truncate description to avoid exceeding Discord's embed field value limit.
		if len([]rune(desc)) > MaxDescriptionFieldLength {
			var b strings.Builder
			b.Grow(MaxDescriptionFieldLength)
			count := 0
			for _, r := range desc {
				if count >= MaxDescriptionFieldLength-1 {
					break
				}
				b.WriteRune(r)
				count++
			}
			desc = b.String() + "…"
		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Description",
			Value:  desc,
			Inline: false,
		})
	}

	// Players
	if len(label.Players) > 0 {
		var playerList strings.Builder
		for _, player := range label.Players {
			playerList.WriteString(fmt.Sprintf("• %s\n", player.DisplayName))
		}
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   fmt.Sprintf("Players (%d)", len(label.Players)),
			Value:  playerList.String(),
			Inline: false,
		})
	} else {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Players",
			Value:  "No players yet",
			Inline: false,
		})
	}

	return embed
}

// editDeferredResponse edits the deferred interaction response with the given content.
// This resolves the "thinking..." message and makes the output visible in-channel.
func (d *DiscordAppBot) editDeferredResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
	return err
}
