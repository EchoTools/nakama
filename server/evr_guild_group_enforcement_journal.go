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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	StorageCollectionEnforcementJournal      = "EnforcementJournal"
	StorageCollectionEnforcementJournalIndex = "EnforcementJournalIndex"
)

var _ = IndexedVersionedStorable(&GuildEnforcementJournal{})

type GuildEnforcementJournal struct {
	SuspensionExpiry           time.Time                 `json:"suspension_expiry"`
	CommunityValuesCompletedAt time.Time                 `json:"community_values_completed_at"`
	IsCommunityValuesRequired  bool                      `json:"is_community_values_required"`
	UserID                     string                    `json:"user_id"`
	GroupID                    string                    `json:"group_id"`
	Records                    []*GuildEnforcementRecord `json:"records"`

	version string
}

func NewGuildEnforcementJournal(userID, groupID string) *GuildEnforcementJournal {
	return &GuildEnforcementJournal{
		UserID:  userID,
		GroupID: groupID,
		Records: []*GuildEnforcementRecord{},
	}
}

func (s *GuildEnforcementJournal) AddRecord(record *GuildEnforcementRecord) {
	s.Records = append(s.Records, record)
}

func (s GuildEnforcementJournal) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection:      StorageCollectionEnforcementJournal,
		Key:             s.GroupID,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s GuildEnforcementJournal) StorageIndex() *StorageIndexMeta {
	return &StorageIndexMeta{
		Name:       StorageCollectionEnforcementJournalIndex,
		Collection: StorageCollectionEnforcementJournal,
		Fields:     []string{"user_id", "group_id", "suspension_expiry", "is_community_values_required"},
		MaxEntries: 10000000,
		IndexOnly:  false,
	}
}

func (s GuildEnforcementJournal) GetStorageVersion() string {
	return s.version
}

func (s *GuildEnforcementJournal) SetStorageVersion(userID, version string) {
	s.UserID = userID
	s.version = version
}

func (s *GuildEnforcementJournal) ActiveSuspensions() []*GuildEnforcementRecord {
	active := make([]*GuildEnforcementRecord, 0)
	for _, r := range s.Records {
		if r.IsActive() {
			active = append(active, r)
		}
	}
	return active
}

func (s *GuildEnforcementJournal) MarshalJSON() ([]byte, error) {

	s.SuspensionExpiry = time.Time{}
	s.IsCommunityValuesRequired = false

	for _, r := range s.Records {
		if r.IsActive() && r.SuspensionExpiry.After(s.SuspensionExpiry) {
			s.SuspensionExpiry = r.SuspensionExpiry
		}
		// Update the community values requirement
		if r.CommunityValuesRequired && r.CreatedAt.After(s.CommunityValuesCompletedAt) {
			s.IsCommunityValuesRequired = true
		}

		// Set the group ID if not already set
		if r.GroupID == "" {
			r.GroupID = s.GroupID
		}
		if r.UserID == "" {
			r.UserID = s.UserID
		}
	}

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

type GuildEnforcementRecord struct {
	ID                      string    `json:"id"`
	GroupID                 string    `json:"group_id"`
	EnforcerUserID          string    `json:"enforcer_user_id"`
	EnforcerDiscordID       string    `json:"enforcer_discord_id"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
	SuspensionNotice        string    `json:"suspension_notice"`
	SuspensionExpiry        time.Time `json:"suspension_expiry"`
	CommunityValuesRequired bool      `json:"community_values_required"`
	AuditorNotes            string    `json:"notes"`
	IsVoid                  bool      `json:"is_void"`
}

func NewGuildEnforcementRecord(enforcerUserID, enforcerDiscordID string, groupID string, suspensionNotice, notes string, requireCommunityValues bool, suspensionExpiry time.Time) *GuildEnforcementRecord {
	return &GuildEnforcementRecord{
		ID:                      uuid.Must(uuid.NewV4()).String(),
		GroupID:                 groupID,
		EnforcerUserID:          enforcerUserID,
		EnforcerDiscordID:       enforcerDiscordID,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
		SuspensionNotice:        suspensionNotice,
		SuspensionExpiry:        suspensionExpiry,
		AuditorNotes:            notes,
		IsVoid:                  false,
		CommunityValuesRequired: requireCommunityValues,
	}
}

func (r GuildEnforcementRecord) Clone() *GuildEnforcementRecord {
	return &GuildEnforcementRecord{
		ID:                      r.ID,
		GroupID:                 r.GroupID,
		EnforcerUserID:          r.EnforcerUserID,
		EnforcerDiscordID:       r.EnforcerDiscordID,
		CreatedAt:               r.CreatedAt,
		UpdatedAt:               r.UpdatedAt,
		SuspensionNotice:        r.SuspensionNotice,
		SuspensionExpiry:        r.SuspensionExpiry,
		AuditorNotes:            r.AuditorNotes,
		IsVoid:                  r.IsVoid,
		CommunityValuesRequired: r.CommunityValuesRequired,
	}
}

func (r *GuildEnforcementRecord) Void(notes string) {
	r.IsVoid = true
	if r.AuditorNotes != "" {
		r.AuditorNotes += "\n"
	}
	r.AuditorNotes += notes
	r.UpdatedAt = time.Now()
}

func (r *GuildEnforcementRecord) Merge(other *GuildEnforcementRecord) {
	if other == nil {
		return
	}

	if r.CreatedAt.IsZero() || other.CreatedAt.Before(r.CreatedAt) {
		r.GroupID = other.GroupID
		r.CreatedAt = other.CreatedAt
		r.SuspensionExpiry = other.SuspensionExpiry
		r.SuspensionNotice = other.SuspensionNotice
		r.EnforcerUserID = other.EnforcerUserID
		r.EnforcerDiscordID = other.EnforcerDiscordID
		if r.AuditorNotes != "" {
			r.AuditorNotes += "\n"
		}
		r.AuditorNotes += other.AuditorNotes
	}

	if r.UpdatedAt.IsZero() || other.UpdatedAt.After(r.UpdatedAt) {
		r.UpdatedAt = other.UpdatedAt

		r.IsVoid = r.IsVoid || other.IsVoid
	}

	if r.SuspensionExpiry.IsZero() || other.SuspensionExpiry.After(r.SuspensionExpiry) {
		r.SuspensionExpiry = other.SuspensionExpiry
	}
}

func (r *GuildEnforcementRecord) IsActive() bool {
	return !r.IsVoid && r.SuspensionExpiry.After(time.Now())
}

func (r *GuildEnforcementRecord) IsExpired() bool {
	return time.Now().After(r.SuspensionExpiry)
}

func (r *GuildEnforcementRecord) RequiresCommunityValues() bool {
	return r.CommunityValuesRequired
}

func EnforcementActiveSuspensionSearch(ctx context.Context, nk runtime.NakamaModule, targetGroupID string, inheritedGroupIDs []string, userID string) (*GuildEnforcementRecord, error) {

	groupIDs := []string{targetGroupID}
	if len(inheritedGroupIDs) > 0 {
		groupIDs = append(groupIDs, inheritedGroupIDs...)
	}

	qparts := []string{
		fmt.Sprintf(`+value.suspension_expiry:>"%s"`, time.Now().UTC().Format(time.RFC3339)),
		fmt.Sprintf("+value.group_id:%s", Query.MatchItem(groupIDs)),
		fmt.Sprintf(`+value.user_id:%s`, Query.MatchItem([]string{userID})),
	}

	query := strings.Join(qparts, " ")

	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, nil, "")
	if err != nil {
		return nil, err
	}

	if len(objs.GetObjects()) == 0 {
		return nil, nil
	}

	journal := NewGuildEnforcementJournal(userID, targetGroupID)
	if err := StorageRead(ctx, nk, userID, journal, false); err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}

	voidedIDs := make(map[string]bool, len(objs.GetObjects()))
	for _, r := range journal.Records {
		if r.IsVoid {
			voidedIDs[r.ID] = true
		}
	}

	activeRecords := make([]*GuildEnforcementRecord, 0, len(objs.GetObjects()))

	for _, obj := range objs.GetObjects() {

		journal := &GuildEnforcementJournal{}
		if err := json.Unmarshal([]byte(obj.GetValue()), journal); err != nil {
			return nil, err
		}
		journal.version = obj.GetVersion()

		for _, r := range journal.Records {
			if voidedIDs[r.ID] {
				continue
			}
			if r.IsActive() {
				activeRecords = append(activeRecords, r)
			}
		}
	}
	if len(activeRecords) == 0 {
		return nil, nil
	}

	// Find the latest active record
	var latestRecord *GuildEnforcementRecord
	for _, r := range activeRecords {
		if voidedIDs[r.ID] {
			continue
		}
		if latestRecord == nil || r.SuspensionExpiry.After(latestRecord.SuspensionExpiry) {
			latestRecord = r
		}
	}

	// Load the entire record
	return latestRecord, nil
}

func EnforcementCommunityValuesSearch(ctx context.Context, nk runtime.NakamaModule, groupID string, userIDs ...string) (map[string]*GuildEnforcementJournal, error) {

	qparts := []string{
		"+value.is_community_values_required:T",
	}

	if groupID != "" {
		qparts = append(qparts, fmt.Sprintf("+value.group_id:%s", Query.Escape(groupID)))
	}

	if len(userIDs) > 0 {
		qparts = append(qparts, fmt.Sprintf(`+value.user_id:%s`, Query.MatchItem(userIDs)))
	}

	var (
		query      = strings.Join(qparts, " ")
		orderBy    = []string{"value.created_at"}
		allRecords = make(map[string]*GuildEnforcementJournal, 3)
		cursor     = ""
	)

	for {
		objs, cursor, err := nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, cursor)
		if err != nil {
			return nil, err
		}

		for _, obj := range objs.GetObjects() {

			allRecords[obj.GetKey()] = &GuildEnforcementJournal{}
			if err := json.Unmarshal([]byte(obj.GetValue()), allRecords[obj.GetKey()]); err != nil {
				return nil, err
			}
		}

		if len(objs.GetObjects()) == 0 || cursor == "" {
			break
		}
	}
	return allRecords, nil
}

func EnforcementJournalSearch(ctx context.Context, nk runtime.NakamaModule, groupIDs []string, userIDs ...string) (map[string]*GuildEnforcementJournal, error) {

	qparts := make([]string, 0, 3)

	if len(groupIDs) > 0 {
		qparts = append(qparts, fmt.Sprintf("+value.group_id:%s", Query.MatchItem(groupIDs)))
	}

	if len(userIDs) > 0 {
		qparts = append(qparts, fmt.Sprintf(`+value.user_id:%s`, Query.MatchItem(userIDs)))
	}

	if len(qparts) == 0 {
		return nil, fmt.Errorf("no search criteria provided")
	}

	var (
		err        error
		query      = strings.Join(qparts, " ")
		orderBy    = []string{"value.created_at"}
		objs       *api.StorageObjects
		cursor     = ""
		allRecords = make(map[string]*GuildEnforcementJournal, 0)
	)

	for {
		objs, cursor, err = nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, cursor)
		if err != nil {
			return nil, err
		}
		allRecords = make(map[string]*GuildEnforcementJournal, len(objs.GetObjects()))

		for _, obj := range objs.GetObjects() {

			allRecords[obj.GetKey()] = &GuildEnforcementJournal{}
			if err := json.Unmarshal([]byte(obj.GetValue()), allRecords[obj.GetKey()]); err != nil {
				return nil, err
			}
		}

		if len(objs.GetObjects()) == 0 || cursor == "" {
			break
		}
	}

	return allRecords, nil
}

func createSuspensionDetailsEmbedField(guildName string, records []*GuildEnforcementRecord) *discordgo.MessageEmbedField {

	parts := make([]string, 0, 3)
	if len(records) == 0 {
		return nil
	}
	for _, r := range records {
		if r == nil {
			continue
		}

		expiry := ""
		if r.IsVoid {
			expiry = "voided"
		} else if r.IsExpired() {
			expiry = "expired"
		} else {
			expiry = fmt.Sprintf("expires <t:%d:R>", r.SuspensionExpiry.UTC().Unix())
		}
		duration := r.SuspensionExpiry.Sub(r.CreatedAt)

		parts = append(parts, fmt.Sprintf("- <t:%d:R>: `%s` [%s, %s]", r.CreatedAt.UTC().Unix(), r.SuspensionNotice, FormatDuration(duration), expiry))

		details := ""
		if r.EnforcerDiscordID != "" {
			details = fmt.Sprintf(" - by <@%s>", r.EnforcerDiscordID)
		}
		if r.AuditorNotes != "" {
			details += fmt.Sprintf(": %s", r.AuditorNotes)
		}
		if details != "" {
			parts = append(parts, details)
		}

	}
	field := &discordgo.MessageEmbedField{
		Name:   guildName,
		Value:  strings.Join(parts, "\n"),
		Inline: false,
	}
	return field
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
