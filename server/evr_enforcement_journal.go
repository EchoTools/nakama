package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	StorageCollectionEnforcementJournal                = "Enforcement"
	StorageKeyEnforcementJournal                       = "journal"
	StorageCollectionEnforcementJournalSuspensionIndex = "EnforcementJournalSuspensionsIndex"
)

type GuildEnforcementRecordVoid struct {
	GroupID         string    `json:"group_id"`
	RecordID        string    `json:"record_id"`
	AuthorID        string    `json:"user_id"`
	AuthorDiscordID string    `json:"discord_id"`
	VoidedAt        time.Time `json:"voided_at"`
	Notes           string    `json:"notes"`
}

type GuildEnforcementJournal struct {
	CommunityValuesCompletedAt time.Time                                        `json:"community_values_completed_at"`
	RecordsByGroupID           map[string][]GuildEnforcementRecord              `json:"records"`
	VoidsByRecordIDByGroupID   map[string]map[string]GuildEnforcementRecordVoid `json:"voids"`
	UserID                     string                                           `json:"user_id"`
	version                    string
}

func NewGuildEnforcementJournal(userID string) *GuildEnforcementJournal {
	return &GuildEnforcementJournal{
		CommunityValuesCompletedAt: time.Now().UTC(),
		UserID:                     userID,
		version:                    "*",
	}
}

func (s *GuildEnforcementJournal) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionEnforcementJournal,
		Key:             StorageKeyEnforcementJournal,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s *GuildEnforcementJournal) SetStorageMeta(meta StorableMetadata) {
	s.UserID = meta.UserID
	s.version = meta.Version
}

func (s GuildEnforcementJournal) GetStorageVersion() string {
	return s.version
}

func GuildEnforcementJournalFromStorageObject(obj *api.StorageObject) (*GuildEnforcementJournal, error) {
	journal := &GuildEnforcementJournal{}
	if err := json.Unmarshal([]byte(obj.GetValue()), journal); err != nil {
		return nil, err
	}
	journal.UserID = obj.GetUserId()
	journal.version = obj.GetVersion()

	for groupID, records := range journal.RecordsByGroupID {
		for i := range records {
			if records[i].UserID == "" {
				records[i].UserID = journal.UserID
			}
			if records[i].GroupID == "" {
				records[i].GroupID = groupID
			}
		}
		journal.RecordsByGroupID[groupID] = records
	}
	return journal, nil
}

func (s *GuildEnforcementJournal) updateFields() {
	// Update the top level fields

	activeByGroupID := make(map[string]time.Time, len(s.RecordsByGroupID))

	for groupID, records := range s.RecordsByGroupID {
		for _, r := range records {

			if s.IsVoid(groupID, r.ID) {
				continue
			}

			if !s.CommunityValuesCompletedAt.IsZero() && r.CommunityValuesRequired && r.CreatedAt.After(s.CommunityValuesCompletedAt) {
				s.CommunityValuesCompletedAt = time.Time{}
			}

			if r.IsExpired() {
				continue
			}

			if e, ok := activeByGroupID[groupID]; !ok || r.Expiry.After(e) {
				activeByGroupID[groupID] = r.Expiry
			}
		}

	}
}

func (s *GuildEnforcementJournal) MarshalJSON() ([]byte, error) {

	// Update the top level fields
	s.updateFields()

	// Use the default JSON marshaler for the struct
	type Alias GuildEnforcementJournal
	b, err := json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(s),
	})
	if err != nil {
		return nil, err
	}

	return b, nil
}

func (s *GuildEnforcementJournal) GroupRecords(groupID string) []GuildEnforcementRecord {
	if s.RecordsByGroupID == nil {
		return []GuildEnforcementRecord{}
	}
	if records, ok := s.RecordsByGroupID[groupID]; ok {
		return records
	}
	return []GuildEnforcementRecord{}
}

// map[groupID]GuildEnforcementRecord
func (s *GuildEnforcementJournal) ActiveSuspensions() map[string]GuildEnforcementRecord {
	active := make(map[string]GuildEnforcementRecord, 0)
	for groupID, records := range s.RecordsByGroupID {
		for _, r := range records {
			if !r.IsSuspension() || s.IsVoid(groupID, r.ID) || r.IsExpired() {
				continue
			}
			if o, ok := active[groupID]; !ok || r.Expiry.After(o.Expiry) {
				active[groupID] = r
			}
		}
	}
	return active
}

func (s *GuildEnforcementJournal) GetVoid(groupID, recordID string) (void GuildEnforcementRecordVoid, found bool) {
	if s.VoidsByRecordIDByGroupID == nil {
		return void, false
	}
	if voids, ok := s.VoidsByRecordIDByGroupID[groupID]; ok {
		if void, ok := voids[recordID]; ok {
			return void, true
		}
	}
	return void, false
}

func (s *GuildEnforcementJournal) IsVoid(groupID, recordID string) bool {
	_, found := s.GetVoid(groupID, recordID)
	return found
}

func (s *GuildEnforcementJournal) AddRecord(groupID, enforcerUserID, enforcerDiscordID, suspensionNotice, notes string, requireCommunityValues, allowPrivateLobbies bool, suspensionDuration time.Duration) GuildEnforcementRecord {
	return s.AddRecordWithOptions(groupID, enforcerUserID, enforcerDiscordID, suspensionNotice, notes, "", requireCommunityValues, allowPrivateLobbies, false, suspensionDuration)
}

// AddRecordWithOptions creates a new enforcement record with additional options
func (s *GuildEnforcementJournal) AddRecordWithOptions(groupID, enforcerUserID, enforcerDiscordID, suspensionNotice, notes, ruleViolated string, requireCommunityValues, allowPrivateLobbies, isPubliclyVisible bool, suspensionDuration time.Duration) GuildEnforcementRecord {
	if s.RecordsByGroupID == nil {
		s.RecordsByGroupID = make(map[string][]GuildEnforcementRecord)
	}
	if s.RecordsByGroupID[groupID] == nil {
		s.RecordsByGroupID[groupID] = make([]GuildEnforcementRecord, 0)
	}
	now := time.Now().UTC()
	record := GuildEnforcementRecord{
		ID:                      uuid.Must(uuid.NewV4()).String(),
		UserID:                  s.UserID,
		GroupID:                 groupID,
		EnforcerUserID:          enforcerUserID,
		EnforcerDiscordID:       enforcerDiscordID,
		CreatedAt:               now,
		UpdatedAt:               now,
		UserNoticeText:          suspensionNotice,
		Expiry:                  now.Add(suspensionDuration),
		AuditorNotes:            notes,
		CommunityValuesRequired: requireCommunityValues,
		AllowPrivateLobbies:     allowPrivateLobbies,
		RuleViolated:            ruleViolated,
		IsPubliclyVisible:       isPubliclyVisible,
		DMNotificationSent:      false,
	}
	s.RecordsByGroupID[groupID] = append(s.RecordsByGroupID[groupID], record)

	return record
}

func (s *GuildEnforcementJournal) VoidRecord(groupID, recordID, authorUserID, authorDiscordID, notes string) GuildEnforcementRecordVoid {
	if s.VoidsByRecordIDByGroupID == nil {
		s.VoidsByRecordIDByGroupID = make(map[string]map[string]GuildEnforcementRecordVoid)
	}
	if s.VoidsByRecordIDByGroupID[groupID] == nil {
		s.VoidsByRecordIDByGroupID[groupID] = make(map[string]GuildEnforcementRecordVoid)
	}
	s.VoidsByRecordIDByGroupID[groupID][recordID] = GuildEnforcementRecordVoid{
		GroupID:         groupID,
		RecordID:        recordID,
		AuthorID:        authorUserID,
		AuthorDiscordID: authorDiscordID,
		VoidedAt:        time.Now().UTC(),
		Notes:           notes,
	}
	return s.VoidsByRecordIDByGroupID[groupID][recordID]
}
func (s *GuildEnforcementJournal) GroupVoids(groupID ...string) map[string]GuildEnforcementRecordVoid {
	voids := make(map[string]GuildEnforcementRecordVoid)
	for _, g := range groupID {
		if groupVoids, ok := s.VoidsByRecordIDByGroupID[g]; ok {
			maps.Copy(voids, groupVoids)
		}
	}
	return voids
}

type GuildEnforcementJournalList map[string]*GuildEnforcementJournal // map[userID]map[groupID]GuildEnforcementRecord

func (l GuildEnforcementJournalList) Latest(groupIDs []string) (string, string, GuildEnforcementRecord) {

	type recordCompact struct {
		GroupID string
		UserID  string
		Record  GuildEnforcementRecord
	}

	latest := recordCompact{}
	for userID, journal := range l {
		for _, groupID := range groupIDs {
			for _, record := range journal.GroupRecords(groupID) {
				if record.IsExpired() || journal.IsVoid(groupID, record.ID) {
					continue
				}
				if record.Expiry.After(latest.Record.Expiry) {
					latest = recordCompact{
						GroupID: groupID,
						UserID:  userID,
						Record:  record,
					}
				}
			}
		}
	}

	return latest.GroupID, latest.UserID, latest.Record
}

func EnforcementJournalsLoad(ctx context.Context, nk runtime.NakamaModule, userIDs []string) (GuildEnforcementJournalList, error) {

	ops := make([]*runtime.StorageRead, 0, len(userIDs))
	for _, userID := range userIDs {
		ops = append(ops, &runtime.StorageRead{
			Collection: StorageCollectionEnforcementJournal,
			Key:        StorageKeyEnforcementJournal,
			UserID:     userID,
		})
	}

	objs, err := nk.StorageRead(ctx, ops)
	if err != nil {
		return nil, err
	}

	journals := make(map[string]*GuildEnforcementJournal, len(objs))
	for _, obj := range objs {
		journal, err := GuildEnforcementJournalFromStorageObject(obj)
		if err != nil {
			return nil, err
		}

		journals[obj.GetUserId()] = journal
	}

	return journals, nil
}

// map[GroupID]map[GameMode]GuildEnforcementRecord
type ActiveGuildEnforcements map[string]map[evr.Symbol]GuildEnforcementRecord

// map[GroupID]map[GameMode]GuildEnforcementRecord
func CheckEnforcementSuspensions(journals GuildEnforcementJournalList, inheritanceMap map[string][]string) (ActiveGuildEnforcements, error) {

	type index struct {
		GroupID  string
		GameMode evr.Symbol
	}

	// Collect all active suspensions for the user and their alts
	activeRecords := make(map[index]GuildEnforcementRecord, len(journals))
	for _, journal := range journals {
		// Check if the user has an active suspension
		for srcGroupID, r := range journal.ActiveSuspensions() {
			// Include the groups that will inherit the suspension
			// map[parentGroupID]map[childGroupID]bool
			affectedGroupIDs := append(inheritanceMap[srcGroupID][:], srcGroupID)
			// Apply the suspension to all affected modes
			affectedModes := evr.AllModes
			if r.SuspensionExcludesPrivateLobbies() {
				affectedModes = evr.PublicModes
			}
			// Apply the suspension to all affected modes/groups
			for _, mode := range affectedModes {
				for _, affectedGroupID := range affectedGroupIDs {
					idx := index{
						GroupID:  affectedGroupID,
						GameMode: mode,
					}
					if a := activeRecords[idx]; a.Expiry.Before(r.Expiry) && !journal.IsVoid(affectedGroupID, r.ID) {
						activeRecords[idx] = r
					}
				}
			}
		}
	}
	// Build the final map of records by group ID and user ID
	activeEnforcements := make(ActiveGuildEnforcements, len(activeRecords))
	for idx, record := range activeRecords {
		if _, ok := activeEnforcements[idx.GroupID]; !ok {
			activeEnforcements[idx.GroupID] = make(map[evr.Symbol]GuildEnforcementRecord)
		}
		activeEnforcements[idx.GroupID][idx.GameMode] = record
	}
	return activeEnforcements, nil
}

func FormatDuration(d time.Duration) string {

	if d == 0 {
		return "0s"
	}

	prefix := ""
	if d < 0 {
		d = -d
		prefix = "-"
	}

	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		d = d.Round(time.Hour)
		if hours := int(d.Hours()) % 24; hours > 0 {
			return fmt.Sprintf("%s%dd%dh", prefix, int(d.Hours()/24), hours)
		}
		return fmt.Sprintf("%s%dd", prefix, int(d.Hours())/24)
	} else if hours > 0 {
		d = d.Round(time.Minute)
		if minutes := int(d.Minutes()) % 60; minutes > 0 {
			return fmt.Sprintf("%s%dh%dm", prefix, hours, minutes)
		}
		return fmt.Sprintf("%s%dh", prefix, int(d.Hours()))
	} else if minutes > 0 {

		if seconds > 0 {
			return fmt.Sprintf("%s%dm%ds", prefix, minutes, seconds)
		}
		return fmt.Sprintf("%s%dm", prefix, minutes)
	} else if seconds > 0 {
		return fmt.Sprintf("%s%ds", prefix, seconds)
	}

	return "0s"
}

func createSuspensionDetailsEmbedField(guildName string, records []GuildEnforcementRecord, voids map[string]GuildEnforcementRecordVoid, includeInactive, includeAuditorNotes, showEnforcerID bool, currentGuildID string) *discordgo.MessageEmbedField {
	if len(records) == 0 {
		return nil
	}

	parts := make([]string, 0, 3)

	for _, r := range records {
		if !includeInactive && (r.IsExpired() || (voids != nil && !voids[r.ID].VoidedAt.IsZero())) {
			continue
		}
		expWord := "expires"
		if r.IsExpired() {
			expWord = "expired"
		}
		durationText := fmt.Sprintf("for **%s** (%s <t:%d:R>)", FormatDuration(r.Expiry.Sub(r.CreatedAt)), expWord, r.Expiry.UTC().Unix())
		if voids != nil && !voids[r.ID].VoidedAt.IsZero() {
			durationText = fmt.Sprintf("~~%s~~", durationText)
		}

		// Show enforcer Discord ID only if viewer is a moderator AND suspension is for current guild
		enforcerInfo := ""
		if showEnforcerID && r.GroupID == currentGuildID {
			enforcerInfo = fmt.Sprintf(" by <@!%s>", r.EnforcerDiscordID)
		}

		parts = append(parts,
			fmt.Sprintf("<t:%d:R>%s %s:", r.CreatedAt.UTC().Unix(), enforcerInfo, durationText),
			fmt.Sprintf("- `%s`", r.UserNoticeText),
		)

		if includeAuditorNotes {
			if r.AuditorNotes != "" {
				parts = append(parts,
					fmt.Sprintf("- *%s*", r.AuditorNotes),
				)
			}
			if voids != nil {
				if v, ok := voids[r.ID]; ok {
					parts = append(parts,
						fmt.Sprintf("- voided by <@!%s> <t:%d:R> *%s*", v.AuthorDiscordID, v.VoidedAt.UTC().Unix(), v.Notes),
					)
				}
			}
		}
	}
	field := &discordgo.MessageEmbedField{
		Name:   guildName,
		Value:  strings.Join(parts, "\n"),
		Inline: false,
	}
	return field
}

func createEnforcementActionComponents(record GuildEnforcementRecord, profile *EVRProfile, gg *GuildGroup, guild *discordgo.Guild, enforcer, target *discordgo.Member, isVoid bool) *discordgo.MessageSend {
	// Is just a kick

	displayName := profile.GetGroupIGN(gg.Group.Id)
	displayName = EscapeDiscordMarkdown(displayName)

	fields := []*discordgo.MessageEmbedField{
		{
			Name:   "Target",
			Value:  fmt.Sprintf("%s (%s)", target.User.String(), target.User.ID),
			Inline: true,
		},
	}
	if !record.Expiry.IsZero() {

		suspensionText := fmt.Sprintf("%s (expires <t:%d:R>)", FormatDuration(record.Expiry.Sub(record.CreatedAt)), record.Expiry.UTC().Unix())

		if isVoid {
			suspensionText = fmt.Sprintf("~~%s~~", suspensionText)
		}
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Suspension",
			Value:  suspensionText,
			Inline: true,
		})
	}

	fields = append(fields, &discordgo.MessageEmbedField{
		Name:   "Notes",
		Value:  fmt.Sprintf("*%s*", record.AuditorNotes),
		Inline: true,
	})

	if record.CommunityValuesRequired {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Community Values Required",
			Value:  fmt.Sprintf("%t", record.CommunityValuesRequired),
			Inline: true,
		})
	}

	if record.UserNoticeText != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "User Notice",
			Value:  fmt.Sprintf("*%s*", record.UserNoticeText),
			Inline: true,
		})
	}
	if record.AuditorNotes != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Auditor Notes",
			Value:  fmt.Sprintf("*%s*", record.AuditorNotes),
			Inline: true,
		})
	}

	embeds := []*discordgo.MessageEmbed{
		{
			Author: &discordgo.MessageEmbedAuthor{
				Name:    enforcer.User.String(),
				IconURL: enforcer.User.AvatarURL(""),
			},
			Thumbnail: &discordgo.MessageEmbedThumbnail{
				URL:    target.User.AvatarURL("128"),
				Width:  128,
				Height: 128,
			},
			Title:       "Enforcement Entry",
			Description: fmt.Sprintf("Enforcement notice for %s (%s)", displayName, target.Mention()),
			Color:       0x9656ce,
			Fields:      fields,
			Timestamp:   record.UpdatedAt.UTC().Format(time.RFC3339),
		},
	}
	components := []discordgo.MessageComponent{
		&discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.Button{
					Label: "Void Record",
					Style: discordgo.SecondaryButton,
					Emoji: &discordgo.ComponentEmoji{
						Name: "heavy_multiplication_x",
					},
					CustomID: fmt.Sprintf("void_record:%s", record.ID),
				},
			},
		},
	}

	return &discordgo.MessageSend{
		Embeds:     embeds,
		Components: components,
		AllowedMentions: &discordgo.MessageAllowedMentions{
			Parse: []discordgo.AllowedMentionType{},
		},
	}
}

// SendEnforcementNotification sends a DM to the user about their enforcement action
// Returns whether the notification was sent successfully
func SendEnforcementNotification(ctx context.Context, dg *discordgo.Session, record GuildEnforcementRecord, targetDiscordID, guildName string) (bool, error) {
	if dg == nil {
		return false, fmt.Errorf("discord session is nil")
	}
	if targetDiscordID == "" {
		return false, fmt.Errorf("target Discord ID is empty")
	}

	// Get the formatted notification message
	message := record.GetNotificationMessage(guildName)

	// Attempt to send the DM
	_, err := SendUserMessage(ctx, dg, targetDiscordID, message)
	if err != nil {
		// Log but don't fail the enforcement action if DM fails
		// Common reasons: user has DMs disabled, user blocked the bot, user is not in a shared server
		return false, fmt.Errorf("failed to send DM to user %s: %w", targetDiscordID, err)
	}

	return true, nil
}

// UpdateRecordNotificationStatus updates a record to mark that a DM notification was attempted/sent
func (s *GuildEnforcementJournal) UpdateRecordNotificationStatus(groupID, recordID string, sent bool) error {
	records, ok := s.RecordsByGroupID[groupID]
	if !ok {
		return fmt.Errorf("group ID not found: %s", groupID)
	}

	for i := range records {
		if records[i].ID == recordID {
			records[i].DMNotificationSent = sent
			records[i].DMNotificationAttempted = time.Now().UTC()
			records[i].UpdatedAt = time.Now().UTC()
			s.RecordsByGroupID[groupID][i] = records[i]
			return nil
		}
	}

	return fmt.Errorf("record ID not found: %s", recordID)
}
