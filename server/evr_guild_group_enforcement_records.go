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
	StorageCollectionEnforcementJournal      = "EnforcementJournal"
	StorageCollectionEnforcementJournalIndex = "EnforcementJournalIndex"
)

var _ = IndexedVersionedStorable(&GuildEnforcementRecords{})

type GuildEnforcementRecords struct {
	SuspensionExpiry           time.Time                 `json:"suspension_expiry"`
	CommunityValuesCompletedAt time.Time                 `json:"community_values_completed_at"`
	IsCommunityValuesRequired  bool                      `json:"is_community_values_required"`
	UserID                     string                    `json:"user_id"`
	GroupID                    string                    `json:"group_id"`
	Records                    []*GuildEnforcementRecord `json:"records"`

	version string
}

func NewGuildEnforcementRecords(userID, groupID string) *GuildEnforcementRecords {
	return &GuildEnforcementRecords{
		UserID:  userID,
		GroupID: groupID,
		Records: []*GuildEnforcementRecord{},
	}
}

func (s *GuildEnforcementRecords) AddRecord(record *GuildEnforcementRecord) {
	s.Records = append(s.Records, record)
}

func (s GuildEnforcementRecords) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection:      StorageCollectionEnforcementJournal,
		Key:             s.GroupID,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s GuildEnforcementRecords) StorageIndex() *StorageIndexMeta {
	return &StorageIndexMeta{
		Name:       StorageCollectionEnforcementJournalIndex,
		Collection: StorageCollectionEnforcementJournal,
		Fields:     []string{"user_id", "group_id", "suspension_expiry", "is_community_values_required"},
		MaxEntries: 10000000,
		IndexOnly:  true,
	}
}

func (s GuildEnforcementRecords) GetStorageVersion() string {
	return s.version
}

func (s *GuildEnforcementRecords) SetStorageVersion(userID, version string) {
	s.UserID = userID
	s.version = version
}

func (s *GuildEnforcementRecords) ActiveSuspensions() []*GuildEnforcementRecord {
	active := make([]*GuildEnforcementRecord, 0)
	for _, r := range s.Records {
		if r.IsActive() {
			active = append(active, r)
		}
	}
	return active
}

func (s *GuildEnforcementRecords) MarshalJSON() ([]byte, error) {

	s.SuspensionExpiry = time.Time{}
	for i := len(s.Records) - 1; i >= 0; i-- {
		if s.Records[i].IsActive() && s.Records[i].SuspensionExpiry.After(s.SuspensionExpiry) {
			s.SuspensionExpiry = s.Records[i].SuspensionExpiry
		}
	}

	s.IsCommunityValuesRequired = false
	for i := len(s.Records) - 1; i >= 0; i-- {
		if s.Records[i].CommunityValuesRequired && s.Records[i].CreatedAt.After(s.CommunityValuesCompletedAt) {
			// Set the flag to true if any record requires community values
			s.IsCommunityValuesRequired = true
			break
		}
	}

	// Use the default JSON marshaler for the struct
	type Alias GuildEnforcementRecords
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
	EnforcerUserID          string    `json:"enforcer_user_id"`
	EnforcerDiscordID       string    `json:"enforcer_discord_id"`
	CreatedAt               time.Time `json:"created_at"`
	SuspensionNotice        string    `json:"suspension_notice"`
	SuspensionExpiry        time.Time `json:"suspension_expiry"`
	CommunityValuesRequired bool      `json:"community_values_required"`
	Notes                   string    `json:"notes"`
	IsVoid                  bool      `json:"is_void"`
}

func NewGuildEnforcementRecord(enforcerUserID, enforcerDiscordID string, suspensionNotice, notes string, requireCommunityValues bool, suspensionExpiry time.Time) *GuildEnforcementRecord {
	return &GuildEnforcementRecord{
		ID:                uuid.Must(uuid.NewV4()).String(),
		EnforcerUserID:    enforcerUserID,
		EnforcerDiscordID: enforcerDiscordID,
		CreatedAt:         time.Now(),
		SuspensionNotice:  suspensionNotice,
		SuspensionExpiry:  suspensionExpiry,
		Notes:             notes,
	}
}

func (r *GuildEnforcementRecord) IsActive() bool {
	return time.Now().Before(r.SuspensionExpiry) && !r.IsVoid
}

func (r *GuildEnforcementRecord) IsExpired() bool {
	return time.Now().After(r.SuspensionExpiry)
}

func (r *GuildEnforcementRecord) RequiresCommunityValues() bool {
	return r.CommunityValuesRequired
}

func EnforcementSuspensionSearch(ctx context.Context, nk runtime.NakamaModule, groupID string, userIDs []string, activeOnly bool) (map[string]map[string]*GuildEnforcementRecords, error) {
	qparts := []string{}

	if activeOnly {
		qparts = append(qparts, fmt.Sprintf(`+value.suspension_expiry:>="%s"`, time.Now().Format(time.RFC3339)))
	}

	if groupID != "" {
		qparts = append(qparts, fmt.Sprintf("+value.group_id:%s", Query.Escape(groupID)))
	}

	if len(userIDs) > 0 {
		qparts = append(qparts, fmt.Sprintf(`+value.user_id:%s`, Query.MatchItem(userIDs)))
	}

	query := strings.Join(qparts, " ")
	orderBy := []string{"value.created_at"}

	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, "")
	if err != nil {
		return nil, err
	}

	if len(objs.GetObjects()) == 0 {
		return nil, nil
	}

	allRecords := make(map[string]map[string]*GuildEnforcementRecords, len(objs.GetObjects()))

	for _, obj := range objs.GetObjects() {

		records := NewGuildEnforcementRecords(obj.GetUserId(), obj.GetKey())

		if err := StorageRead(ctx, nk, records.UserID, records, false); err != nil {
			return nil, err
		}

		for _, record := range records.Records {
			if activeOnly && !record.IsActive() {
				continue
			}
		}

		if _, ok := allRecords[records.GroupID]; !ok {
			allRecords[records.GroupID] = make(map[string]*GuildEnforcementRecords, len(userIDs))
		}
		allRecords[records.GroupID][records.UserID] = records
	}

	// Load the entire record
	return allRecords, nil
}

func EnforcementCommunityValuesSearch(ctx context.Context, nk runtime.NakamaModule, groupID string, userIDs ...string) (map[string]*GuildEnforcementRecords, error) {

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
		allRecords = make(map[string]*GuildEnforcementRecords, 3)
		cursor     = ""
	)

	for {
		objs, cursor, err := nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, cursor)
		if err != nil {
			return nil, err
		}

		for _, obj := range objs.GetObjects() {

			allRecords[obj.GetKey()] = &GuildEnforcementRecords{}
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

func EnforcementJournalSearch(ctx context.Context, nk runtime.NakamaModule, groupID string, userIDs ...string) (map[string]*GuildEnforcementRecords, error) {

	qparts := make([]string, 0, 3)

	if groupID != "" {
		qparts = append(qparts, fmt.Sprintf("+value.group_id:%s", Query.Escape(groupID)))
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
		allRecords = make(map[string]*GuildEnforcementRecords, 0)
	)

	for {
		objs, cursor, err = nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, cursor)
		if err != nil {
			return nil, err
		}
		allRecords = make(map[string]*GuildEnforcementRecords, len(objs.GetObjects()))

		for _, obj := range objs.GetObjects() {

			allRecords[obj.GetKey()] = &GuildEnforcementRecords{}
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

func createSuspensionDetailsEmbedField(guildName string, record []*GuildEnforcementRecord, includeNotes bool) *discordgo.MessageEmbedField {

	parts := make([]string, 0, 3)
	if len(record) == 0 {
		return nil
	}
	for _, r := range record {
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

		parts = append(parts, fmt.Sprintf("- <t:%d:R>: `%s` [%s, %s]", r.CreatedAt.UTC().Unix(), r.SuspensionNotice, formatDuration(duration), expiry))

		if includeNotes {
			details := ""
			if r.EnforcerDiscordID != "" {
				details = fmt.Sprintf(" - by <@%s>", r.EnforcerDiscordID)
			}
			if r.Notes != "" {
				details += fmt.Sprintf(": %s", r.Notes)
			}
			if details != "" {
				parts = append(parts, details)
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
