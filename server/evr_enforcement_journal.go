package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEnforcementJournal                = "Enforcement"
	StorageKeyEnforcementJournal                       = "journal"
	StorageCollectionEnforcementJournalSuspensionIndex = "EnforcementJournalSuspensionsIndex"
)

var _ = IndexedVersionedStorable(&GuildEnforcementJournal{})

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

func (s GuildEnforcementJournal) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection:      StorageCollectionEnforcementJournal,
		Key:             StorageKeyEnforcementJournal,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}
func (s GuildEnforcementJournal) GetStorageVersion() string {
	return s.version
}

func (s *GuildEnforcementJournal) SetStorageVersion(userID, version string) {
	s.UserID = userID
	s.version = version
}

func (s GuildEnforcementJournal) StorageIndexes() []StorageIndexMeta {
	return nil
}

func GuildEnforcementJournalFromStorageObject(obj *api.StorageObject) (*GuildEnforcementJournal, error) {
	journal := &GuildEnforcementJournal{}
	if err := json.Unmarshal([]byte(obj.GetValue()), journal); err != nil {
		return nil, err
	}
	journal.UserID = obj.GetUserId()
	journal.version = obj.GetVersion()
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

			if e, ok := activeByGroupID[groupID]; !ok || r.SuspensionExpiry.After(e) {
				activeByGroupID[groupID] = r.SuspensionExpiry
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
			if o, ok := active[groupID]; !ok || r.SuspensionExpiry.After(o.SuspensionExpiry) {
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

func (s *GuildEnforcementJournal) AddRecord(groupID, enforcerUserID, enforcerDiscordID, suspensionNotice, notes string, requireCommunityValues bool, suspensionDuration time.Duration) GuildEnforcementRecord {
	if s.RecordsByGroupID == nil {
		s.RecordsByGroupID = make(map[string][]GuildEnforcementRecord)
	}
	if s.RecordsByGroupID[groupID] == nil {
		s.RecordsByGroupID[groupID] = make([]GuildEnforcementRecord, 0)
	}
	now := time.Now().UTC()
	record := GuildEnforcementRecord{
		ID:                      uuid.Must(uuid.NewV4()).String(),
		EnforcerUserID:          enforcerUserID,
		EnforcerDiscordID:       enforcerDiscordID,
		CreatedAt:               now,
		UpdatedAt:               now,
		UserNoticeText:          suspensionNotice,
		SuspensionExpiry:        now.Add(suspensionDuration),
		AuditorNotes:            notes,
		CommunityValuesRequired: requireCommunityValues,
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
			for k, v := range groupVoids {
				voids[k] = v
			}
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
				if record.SuspensionExpiry.After(latest.Record.SuspensionExpiry) {
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

// map[GroupID]map[UserID]GuildEnforcementRecord
func CheckEnforcementSuspensions(ctx context.Context, nk runtime.NakamaModule, guildGroupRegistry *GuildGroupRegistry, userID string, firstAltIDs []string) (map[string]map[string]GuildEnforcementRecord, error) {

	// Get the GroupID from the user's metadata
	guildGroups, err := GuildUserGroupsList(ctx, nk, guildGroupRegistry, userID)
	if err != nil {
		return nil, err
	}

	// Create a map of guild inheritence by parent/child group ID.
	guildEnforcementInheritenceMap := make(map[string][]string) // map[parentGroupID][]childGroupID
	for _, gg := range guildGroups {
		if len(gg.SuspensionInheritanceGroupIDs) > 0 {
			for _, parentID := range gg.SuspensionInheritanceGroupIDs {
				guildEnforcementInheritenceMap[parentID] = append(guildEnforcementInheritenceMap[parentID], gg.IDStr())
			}
		}
	}

	// Check for suspensions for this user and their first degree alts.
	activeSuspensionRecords := make(map[string]map[string]GuildEnforcementRecord)

	if journals, err := EnforcementJournalsLoad(ctx, nk, append(firstAltIDs, userID)); err != nil {
		return nil, fmt.Errorf("failed to load enforcement journals: %w", err)
	} else {
		for uID, journal := range journals {
			activeSuspensionRecords[uID] = applySuspensionInheritence(journal, guildEnforcementInheritenceMap)
		}
	}

	return activeSuspensionRecords, nil

}

func applySuspensionInheritence(journal *GuildEnforcementJournal, inheritenceMap map[string][]string) map[string]GuildEnforcementRecord {
	activeSuspensions := journal.ActiveSuspensions()
	for parent, children := range inheritenceMap {
		for _, child := range children {
			if parentSuspension, ok := activeSuspensions[parent]; ok {
				if _, ok := activeSuspensions[child]; !ok || parentSuspension.SuspensionExpiry.After(activeSuspensions[child].SuspensionExpiry) {
					activeSuspensions[child] = parentSuspension
				}
			}
		}
	}
	return activeSuspensions
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

func createSuspensionDetailsEmbedField(guildName string, records []GuildEnforcementRecord, voids map[string]GuildEnforcementRecordVoid, includeInactive, includeAuditorNotes bool) *discordgo.MessageEmbedField {
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
		durationText := fmt.Sprintf("for **%s** (%s <t:%d:R>)", FormatDuration(r.SuspensionExpiry.Sub(r.CreatedAt)), expWord, r.SuspensionExpiry.UTC().Unix())
		if voids != nil && !voids[r.ID].VoidedAt.IsZero() {
			durationText = fmt.Sprintf("~~%s~~", durationText)
		}

		parts = append(parts,
			fmt.Sprintf("<t:%d:R> by <@!%s> %s:", r.CreatedAt.UTC().Unix(), r.EnforcerDiscordID, durationText),
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

	displayName := profile.GetGroupDisplayNameOrDefault(gg.Group.Id)
	displayName = EscapeDiscordMarkdown(displayName)

	fields := []*discordgo.MessageEmbedField{
		{
			Name:   "Target",
			Value:  fmt.Sprintf("%s (%s)", target.User.String(), target.User.ID),
			Inline: true,
		},
	}
	if !record.SuspensionExpiry.IsZero() {

		suspensionText := fmt.Sprintf("%s (expires <t:%d:R>)", FormatDuration(record.SuspensionExpiry.Sub(record.CreatedAt)), record.SuspensionExpiry.UTC().Unix())

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
