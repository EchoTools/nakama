package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// handleEnforcementInteraction handles button interactions for enforcement records (edit, void)
func (d *DiscordAppBot) handleEnforcementInteraction(ctx context.Context, _ runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, value string) error {
	// value format: action:recordID:groupID:targetUserID
	parts := strings.SplitN(value, ":", 4)
	if len(parts) != 4 {
		return fmt.Errorf("invalid enforcement interaction format")
	}

	action := parts[0]
	recordID := parts[1]
	groupID := parts[2]
	targetUserID := parts[3]

	user, _ := getScopedUserMember(i)
	if user == nil {
		return fmt.Errorf("user is nil")
	}

	callerID := d.cache.DiscordIDToUserID(user.ID)
	if callerID == "" {
		return simpleInteractionResponse(s, i, "You must have a linked account to use this feature.")
	}

	// Check permissions - any moderator (enforcer) can edit/void
	isGlobalOperator, _ := CheckSystemGroupMembership(ctx, d.db, callerID, GroupGlobalOperators)

	gg, err := GuildGroupLoad(ctx, d.nk, groupID)
	if err != nil {
		return fmt.Errorf("failed to load guild group: %w", err)
	}

	if !isGlobalOperator && !gg.IsEnforcer(callerID) {
		return simpleInteractionResponse(s, i, "You must be a guild enforcer to modify enforcement records.")
	}

	switch action {
	case "edit":
		return d.showEnforcementEditModal(s, i, recordID, groupID, targetUserID)
	case "void":
		return d.showEnforcementVoidModal(s, i, recordID, groupID, targetUserID)
	default:
		return fmt.Errorf("unknown enforcement action: %s", action)
	}
}

// showEnforcementEditModal displays the edit modal with pre-filled current values
func (d *DiscordAppBot) showEnforcementEditModal(s *discordgo.Session, i *discordgo.InteractionCreate, recordID, groupID, targetUserID string) error {
	ctx := d.ctx

	// Load the enforcement journal for the target user
	journal := NewGuildEnforcementJournal(targetUserID)
	if err := StorableRead(ctx, d.nk, targetUserID, journal, false); err != nil {
		return fmt.Errorf("failed to load enforcement journal: %w", err)
	}

	// Get the specific record
	record := journal.GetRecord(groupID, recordID)
	if record == nil {
		return simpleInteractionResponse(s, i, "Enforcement record not found.")
	}

	// Check if already voided
	if journal.IsVoid(groupID, recordID) {
		return simpleInteractionResponse(s, i, "This record has already been voided and cannot be edited.")
	}

	// Calculate original total duration (from CreatedAt to Expiry) as human-readable format
	totalDuration := record.Expiry.Sub(record.CreatedAt)
	if totalDuration < 0 {
		totalDuration = 0
	}
	durationStr := FormatDuration(totalDuration)

	// Create the modal with pre-filled values
	modal := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("enforcement_edit:%s:%s:%s", recordID, groupID, targetUserID),
			Title:    "Edit Enforcement Record",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "duration_input",
							Label:       "Duration (e.g., 1d2h, 30m, 1w)",
							Style:       discordgo.TextInputShort,
							Placeholder: "Enter duration (e.g., 15m, 1h, 2d, 1w)",
							Value:       durationStr,
							Required:    true,
							MinLength:   1,
							MaxLength:   20,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "user_notice_input",
							Label:       "User Notice (visible to user)",
							Style:       discordgo.TextInputShort,
							Placeholder: "Reason shown to the user",
							Value:       record.UserNoticeText,
							Required:    true,
							MinLength:   1,
							MaxLength:   45,
						},
					},
				},
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "mod_notes_input",
							Label:       "Moderator Notes (internal only)",
							Style:       discordgo.TextInputParagraph,
							Placeholder: "Internal notes for moderators",
							Value:       record.AuditorNotes,
							Required:    false,
							MaxLength:   200,
						},
					},
				},
			},
		},
	}

	return s.InteractionRespond(i.Interaction, modal)
}

// showEnforcementVoidModal displays the void confirmation modal
func (d *DiscordAppBot) showEnforcementVoidModal(s *discordgo.Session, i *discordgo.InteractionCreate, recordID, groupID, targetUserID string) error {
	ctx := d.ctx

	// Load the enforcement journal to verify the record exists
	journal := NewGuildEnforcementJournal(targetUserID)
	if err := StorableRead(ctx, d.nk, targetUserID, journal, false); err != nil {
		return fmt.Errorf("failed to load enforcement journal: %w", err)
	}

	// Get the specific record
	record := journal.GetRecord(groupID, recordID)
	if record == nil {
		return simpleInteractionResponse(s, i, "Enforcement record not found.")
	}

	// Check if already voided
	if journal.IsVoid(groupID, recordID) {
		return simpleInteractionResponse(s, i, "This record has already been voided.")
	}

	// Get target display info
	targetDiscordID := d.cache.UserIDToDiscordID(targetUserID)
	targetMention := targetUserID
	if targetDiscordID != "" {
		targetMention = fmt.Sprintf("<@%s>", targetDiscordID)
	}

	// Create the void confirmation modal
	modal := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: fmt.Sprintf("enforcement_void:%s:%s:%s", recordID, groupID, targetUserID),
			Title:    "Void Enforcement Record",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "void_reason_input",
							Label:       fmt.Sprintf("Reason for voiding (target: %s)", targetMention),
							Style:       discordgo.TextInputParagraph,
							Placeholder: "Enter reason for voiding this suspension",
							Required:    true,
							MinLength:   1,
							MaxLength:   200,
						},
					},
				},
			},
		},
	}

	return s.InteractionRespond(i.Interaction, modal)
}

// handleEnforcementEditModalSubmit handles the edit modal form submission
func (d *DiscordAppBot) handleEnforcementEditModalSubmit(logger runtime.Logger, i *discordgo.InteractionCreate, value string) error {
	ctx := d.ctx

	// Parse value: recordID:groupID:targetUserID
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid enforcement edit modal value")
	}
	recordID := parts[0]
	groupID := parts[1]
	targetUserID := parts[2]

	// Get caller info
	callerID := d.cache.DiscordIDToUserID(i.Member.User.ID)
	if callerID == "" {
		return fmt.Errorf("caller not found")
	}

	// Verify permissions again
	isGlobalOperator, _ := CheckSystemGroupMembership(ctx, d.db, callerID, GroupGlobalOperators)
	gg, err := GuildGroupLoad(ctx, d.nk, groupID)
	if err != nil {
		return fmt.Errorf("failed to load guild group: %w", err)
	}
	if !isGlobalOperator && !gg.IsEnforcer(callerID) {
		return simpleInteractionResponse(d.dg, i, "You must be a guild enforcer to edit enforcement records.")
	}

	// Get form data
	data := i.Interaction.ModalSubmitData()
	durationStr := data.Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
	userNotice := data.Components[1].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
	modNotes := data.Components[2].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value

	// Parse the new duration
	newDuration, err := parseSuspensionDuration(durationStr)
	if err != nil {
		return simpleInteractionResponse(d.dg, i, fmt.Sprintf("Invalid duration format: %s\n\nUse formats like: 15m, 1h, 2d, 1w, 2h30m", err.Error()))
	}

	// Load the enforcement journal
	journal := NewGuildEnforcementJournal(targetUserID)
	if err := StorableRead(ctx, d.nk, targetUserID, journal, false); err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to load enforcement journal: %w", err)
	}

	// Get the record for "before" snapshot
	record := journal.GetRecord(groupID, recordID)
	if record == nil {
		return simpleInteractionResponse(d.dg, i, "Enforcement record not found.")
	}

	// Check if already voided
	if journal.IsVoid(groupID, recordID) {
		return simpleInteractionResponse(d.dg, i, "This record has already been voided and cannot be edited.")
	}

	// Create "before" embed for audit
	beforeEmbed := createEnforcementRecordEmbed("Before Edit", *record, gg)

	// Calculate new expiry: from original CreatedAt + new duration
	newExpiry := record.CreatedAt.Add(newDuration)

	// Edit the record (this creates the edit log entry)
	updatedRecord := journal.EditRecord(groupID, recordID, callerID, i.Member.User.ID, newExpiry, userNotice, modNotes)
	if updatedRecord == nil {
		return simpleInteractionResponse(d.dg, i, "Failed to edit enforcement record.")
	}

	// Save the journal
	if err := StorableWrite(ctx, d.nk, targetUserID, journal); err != nil {
		return fmt.Errorf("failed to save enforcement journal: %w", err)
	}

	// Create "after" embed for audit
	afterEmbed := createEnforcementRecordEmbed("After Edit", *updatedRecord, gg)

	// Send audit log with before/after embeds
	d.sendEnforcementEditAuditLog(logger, i.Member.User, groupID, gg, beforeEmbed, afterEmbed)

	// Update the original message to reflect the changes
	if err := d.updateEnforcementMessage(i, *updatedRecord, gg, false); err != nil {
		logger.WithField("error", err).Warn("Failed to update enforcement message")
	}

	// Respond to the interaction
	return d.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: fmt.Sprintf("✅ Enforcement record updated successfully.\n\n**New Duration:** %s (expires <t:%d:R>)\n**User Notice:** %s", FormatDuration(newDuration), newExpiry.Unix(), userNotice),
		},
	})
}

// handleEnforcementVoidModalSubmit handles the void confirmation modal submission
func (d *DiscordAppBot) handleEnforcementVoidModalSubmit(logger runtime.Logger, i *discordgo.InteractionCreate, value string) error {
	ctx := d.ctx

	// Parse value: recordID:groupID:targetUserID
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid enforcement void modal value")
	}
	recordID := parts[0]
	groupID := parts[1]
	targetUserID := parts[2]

	// Get caller info
	callerID := d.cache.DiscordIDToUserID(i.Member.User.ID)
	if callerID == "" {
		return fmt.Errorf("caller not found")
	}

	// Verify permissions again
	isGlobalOperator, _ := CheckSystemGroupMembership(ctx, d.db, callerID, GroupGlobalOperators)
	gg, err := GuildGroupLoad(ctx, d.nk, groupID)
	if err != nil {
		return fmt.Errorf("failed to load guild group: %w", err)
	}
	if !isGlobalOperator && !gg.IsEnforcer(callerID) {
		return simpleInteractionResponse(d.dg, i, "You must be a guild enforcer to void enforcement records.")
	}

	// Get form data
	data := i.Interaction.ModalSubmitData()
	voidReason := data.Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value

	// Load the enforcement journal
	journal := NewGuildEnforcementJournal(targetUserID)
	if err := StorableRead(ctx, d.nk, targetUserID, journal, false); err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to load enforcement journal: %w", err)
	}

	// Get the record for "before" snapshot
	record := journal.GetRecord(groupID, recordID)
	if record == nil {
		return simpleInteractionResponse(d.dg, i, "Enforcement record not found.")
	}

	// Check if already voided
	if journal.IsVoid(groupID, recordID) {
		return simpleInteractionResponse(d.dg, i, "This record has already been voided.")
	}

	// Create "before" embed for audit
	beforeEmbed := createEnforcementRecordEmbed("Before Void", *record, gg)

	// Void the record
	journal.VoidRecord(groupID, recordID, callerID, i.Member.User.ID, voidReason)

	// Save the journal
	if err := StorableWrite(ctx, d.nk, targetUserID, journal); err != nil {
		return fmt.Errorf("failed to save enforcement journal: %w", err)
	}

	// Create "after" embed for audit (show as voided)
	afterEmbed := createEnforcementRecordEmbed("After Void (VOIDED)", *record, gg)
	afterEmbed.Color = 0x808080 // Gray color for voided
	afterEmbed.Description = fmt.Sprintf("~~%s~~\n\n**Voided by:** <@%s>\n**Reason:** %s", afterEmbed.Description, i.Member.User.ID, voidReason)

	// Send audit log with before/after embeds
	d.sendEnforcementVoidAuditLog(logger, i.Member.User, groupID, gg, beforeEmbed, afterEmbed, voidReason)

	// Update the original message to reflect the voided state
	if err := d.updateEnforcementMessage(i, *record, gg, true); err != nil {
		logger.WithField("error", err).Warn("Failed to update enforcement message")
	}

	// Respond to the interaction
	return d.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: fmt.Sprintf("✅ Enforcement record voided successfully.\n\n**Reason:** %s", voidReason),
		},
	})
}

// createEnforcementRecordEmbed creates an embed displaying an enforcement record
func createEnforcementRecordEmbed(title string, record GuildEnforcementRecord, gg *GuildGroup) *discordgo.MessageEmbed {
	fields := []*discordgo.MessageEmbedField{
		{
			Name:   "Duration",
			Value:  fmt.Sprintf("%s (expires <t:%d:R>)", FormatDuration(record.Expiry.Sub(record.CreatedAt)), record.Expiry.Unix()),
			Inline: true,
		},
		{
			Name:   "User Notice",
			Value:  fmt.Sprintf("`%s`", record.UserNoticeText),
			Inline: true,
		},
	}

	if record.AuditorNotes != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Moderator Notes",
			Value:  fmt.Sprintf("*%s*", record.AuditorNotes),
			Inline: false,
		})
	}

	fields = append(fields, &discordgo.MessageEmbedField{
		Name:   "Enforcer",
		Value:  fmt.Sprintf("<@%s>", record.EnforcerDiscordID),
		Inline: true,
	})

	fields = append(fields, &discordgo.MessageEmbedField{
		Name:   "Created",
		Value:  fmt.Sprintf("<t:%d:F>", record.CreatedAt.Unix()),
		Inline: true,
	})

	return &discordgo.MessageEmbed{
		Title:       title,
		Description: fmt.Sprintf("Record ID: `%s`\nGuild: %s", record.ID, gg.Name()),
		Color:       0x9656ce,
		Fields:      fields,
		Timestamp:   record.UpdatedAt.Format(time.RFC3339),
	}
}

// sendEnforcementEditAuditLog sends audit log embeds for an edit operation
func (d *DiscordAppBot) sendEnforcementEditAuditLog(logger runtime.Logger, editor *discordgo.User, _ string, gg *GuildGroup, beforeEmbed, afterEmbed *discordgo.MessageEmbed) {
	// Add editor info to embeds
	beforeEmbed.Footer = &discordgo.MessageEmbedFooter{
		Text:    fmt.Sprintf("Edited by %s", editor.String()),
		IconURL: editor.AvatarURL(""),
	}
	afterEmbed.Footer = &discordgo.MessageEmbedFooter{
		Text:    fmt.Sprintf("Edited by %s", editor.String()),
		IconURL: editor.AvatarURL(""),
	}

	// Send to guild audit channel
	if gg.AuditChannelID != "" {
		_, err := d.dg.ChannelMessageSendComplex(gg.AuditChannelID, &discordgo.MessageSend{
			Content:         fmt.Sprintf("📝 **Enforcement Record Edited** by <@%s>", editor.ID),
			Embeds:          []*discordgo.MessageEmbed{beforeEmbed, afterEmbed},
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
		if err != nil {
			logger.WithField("error", err).Warn("Failed to send guild audit log")
		}
	}

	// Send to service audit channel
	serviceAuditChannel := ServiceSettings().ServiceAuditChannelID
	if serviceAuditChannel != "" {
		content := fmt.Sprintf("[`%s/%s`] 📝 **Enforcement Record Edited** by <@%s>", gg.Name(), gg.GuildID, editor.ID)
		_, err := d.dg.ChannelMessageSendComplex(serviceAuditChannel, &discordgo.MessageSend{
			Content:         content,
			Embeds:          []*discordgo.MessageEmbed{beforeEmbed, afterEmbed},
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
		if err != nil {
			logger.WithField("error", err).Warn("Failed to send service audit log")
		}
	}
}

// sendEnforcementVoidAuditLog sends audit log embeds for a void operation
func (d *DiscordAppBot) sendEnforcementVoidAuditLog(logger runtime.Logger, editor *discordgo.User, _ string, gg *GuildGroup, beforeEmbed, afterEmbed *discordgo.MessageEmbed, reason string) {
	// Add editor info to embeds
	beforeEmbed.Footer = &discordgo.MessageEmbedFooter{
		Text:    fmt.Sprintf("Voided by %s", editor.String()),
		IconURL: editor.AvatarURL(""),
	}
	afterEmbed.Footer = &discordgo.MessageEmbedFooter{
		Text:    fmt.Sprintf("Voided by %s - Reason: %s", editor.String(), reason),
		IconURL: editor.AvatarURL(""),
	}

	// Send to guild audit channel
	if gg.AuditChannelID != "" {
		_, err := d.dg.ChannelMessageSendComplex(gg.AuditChannelID, &discordgo.MessageSend{
			Content:         fmt.Sprintf("❌ **Enforcement Record Voided** by <@%s>\n**Reason:** %s", editor.ID, reason),
			Embeds:          []*discordgo.MessageEmbed{beforeEmbed, afterEmbed},
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
		if err != nil {
			logger.WithField("error", err).Warn("Failed to send guild audit log")
		}
	}

	// Send to service audit channel
	serviceAuditChannel := ServiceSettings().ServiceAuditChannelID
	if serviceAuditChannel != "" {
		content := fmt.Sprintf("[`%s/%s`] ❌ **Enforcement Record Voided** by <@%s>\n**Reason:** %s", gg.Name(), gg.GuildID, editor.ID, reason)
		_, err := d.dg.ChannelMessageSendComplex(serviceAuditChannel, &discordgo.MessageSend{
			Content:         content,
			Embeds:          []*discordgo.MessageEmbed{beforeEmbed, afterEmbed},
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
		if err != nil {
			logger.WithField("error", err).Warn("Failed to send service audit log")
		}
	}
}

// updateEnforcementMessage updates the original enforcement message with new record data
func (d *DiscordAppBot) updateEnforcementMessage(i *discordgo.InteractionCreate, record GuildEnforcementRecord, gg *GuildGroup, isVoid bool) error {
	if i.Message == nil {
		return fmt.Errorf("no message to update")
	}

	// Build updated embed
	fields := []*discordgo.MessageEmbedField{}

	// Find and preserve Target field from original embed
	for _, embed := range i.Message.Embeds {
		for _, field := range embed.Fields {
			if field.Name == "Target" {
				fields = append(fields, field)
				break
			}
		}
	}

	// Add suspension field
	if !record.Expiry.IsZero() {
		suspensionText := fmt.Sprintf("%s (expires <t:%d:R>)", FormatDuration(record.Expiry.Sub(record.CreatedAt)), record.Expiry.Unix())
		if isVoid {
			suspensionText = fmt.Sprintf("~~%s~~ **VOIDED**", suspensionText)
		}
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Suspension",
			Value:  suspensionText,
			Inline: true,
		})
	}

	// Add user notice
	if record.UserNoticeText != "" {
		noticeText := fmt.Sprintf("*%s*", record.UserNoticeText)
		if isVoid {
			noticeText = fmt.Sprintf("~~%s~~", noticeText)
		}
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "User Notice",
			Value:  noticeText,
			Inline: true,
		})
	}

	// Add auditor notes
	if record.AuditorNotes != "" {
		notesText := fmt.Sprintf("*%s*", record.AuditorNotes)
		if isVoid {
			notesText = fmt.Sprintf("~~%s~~", notesText)
		}
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Auditor Notes",
			Value:  notesText,
			Inline: true,
		})
	}

	// Determine embed color
	embedColor := 0x9656ce // Purple for active
	if isVoid {
		embedColor = 0x808080 // Gray for voided
	}

	// Build the updated embed
	var originalEmbed *discordgo.MessageEmbed
	if len(i.Message.Embeds) > 0 {
		originalEmbed = i.Message.Embeds[0]
	} else {
		originalEmbed = &discordgo.MessageEmbed{}
	}

	updatedEmbed := &discordgo.MessageEmbed{
		Author:      originalEmbed.Author,
		Thumbnail:   originalEmbed.Thumbnail,
		Title:       originalEmbed.Title,
		Description: originalEmbed.Description,
		Color:       embedColor,
		Fields:      fields,
		Timestamp:   record.UpdatedAt.Format(time.RFC3339),
	}

	if isVoid {
		updatedEmbed.Title = "Enforcement Entry (VOIDED)"
	}

	// Build updated buttons (disabled if voided)
	buttons := []discordgo.MessageComponent{
		&discordgo.Button{
			Label:    "Edit",
			Style:    discordgo.PrimaryButton,
			CustomID: fmt.Sprintf("enforcement:edit:%s:%s:%s", record.ID, gg.Group.Id, record.UserID),
			Disabled: isVoid,
		},
		&discordgo.Button{
			Label:    "Void",
			Style:    discordgo.DangerButton,
			CustomID: fmt.Sprintf("enforcement:void:%s:%s:%s", record.ID, gg.Group.Id, record.UserID),
			Disabled: isVoid,
		},
	}

	components := []discordgo.MessageComponent{
		&discordgo.ActionsRow{
			Components: buttons,
		},
	}

	// Edit the message
	_, err := d.dg.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel:    i.Message.ChannelID,
		ID:         i.Message.ID,
		Embeds:     &[]*discordgo.MessageEmbed{updatedEmbed},
		Components: &components,
	})

	return err
}
