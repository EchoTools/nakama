package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionEnforcementJournal      = "EnforcementJournal"
	StorageCollectionEnforcementJournalIndex = "EnforcementJournalIndex"
)

var _ = IndexedVersionedStorable(&GuildEnforcementRecords{})

type GuildEnforcementRecords struct {
	UserID  string
	GroupID string                    `json:"group_id"`
	Records []*GuildEnforcementRecord `json:"records"`

	version string
}

func NewGuildEnforcementRecords() *GuildEnforcementRecords {
	return &GuildEnforcementRecords{
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
		Fields:     []string{"records.user_id", "records.guild_id", "records.suspension_expiry", "records.community_values_required", "records.community_values_completed"},
	}
}

func (s GuildEnforcementRecords) GetStorageVersion() string {
	return s.version
}

func (s *GuildEnforcementRecords) SetStorageVersion(version string) {
	s.version = version
}

func (s *GuildEnforcementRecords) ActiveSuspensions() []*GuildEnforcementRecord {
	active := make([]*GuildEnforcementRecord, 0)
	for _, r := range s.Records {
		if time.Now().Before(r.SuspensionExpiry) {
			active = append(active, r)
		}
	}
	return active
}

func (s *GuildEnforcementRecords) RequiresCommunityValues() bool {
	for _, r := range s.Records {
		if r.CommunityValuesRequired && r.CommunityValuesCompletedAt.IsZero() {
			return true
		}
	}
	return false
}

type GuildEnforcementRecord struct {
	ID                         string    `json:"id"`
	EnforcerUserID             string    `json:"enforcer_id"`
	CreatedAt                  time.Time `json:"created_at"`
	SuspensionNotice           string    `json:"suspension_notice"`
	SuspensionExpiry           time.Time `json:"suspension_expiry"`
	CommunityValuesRequired    bool      `json:"community_values_required"`
	CommunityValuesCompletedAt time.Time `json:"community_values_completed_at"`
	Notes                      string    `json:"notes"`
}

func NewEnforcementGuildEntry(guildID, enforcerUserID string, suspensionNotice, notes string, suspensionExpiry time.Time) *GuildEnforcementRecord {
	return &GuildEnforcementRecord{
		ID:               uuid.Must(uuid.NewV4()).String(),
		EnforcerUserID:   enforcerUserID,
		CreatedAt:        time.Now(),
		SuspensionNotice: suspensionNotice,
		SuspensionExpiry: suspensionExpiry,
		Notes:            notes,
	}
}

func (s *GuildEnforcementRecord) IsSuspended() bool {
	return time.Now().Before(s.SuspensionExpiry)
}

func (s *GuildEnforcementRecord) RequiresCommunityValues() bool {
	return s.CommunityValuesRequired && s.CommunityValuesCompletedAt.IsZero()
}

func EnforcementSuspensionSearch(ctx context.Context, nk runtime.NakamaModule, groupID string, userIDs []string, includeExpired bool) (map[string]*GuildEnforcementRecords, error) {

	qparts := make([]string, 1)
	if !includeExpired {
		qparts = append(qparts, fmt.Sprintf(`+value.suspension_expiry:>="%s"`, time.Now().Format(time.RFC3339)))
	}

	for _, userID := range userIDs {
		qparts = append(qparts, fmt.Sprintf(`+value.user_id:%s`, Query.Join())
	}

	if groupID != "" {
		qparts = append(qparts, "+value.group_id:%s", Query.Escape(groupID))
	}

	query := strings.Join(qparts, " ")
	orderBy := []string{"value.created_at"}

	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, "")
	if err != nil {
		return nil, err
	}
	allRecords := make(map[string]*GuildEnforcementRecords, len(objs.GetObjects()))

	for _, obj := range objs.GetObjects() {

		allRecords[obj.GetKey()] = &GuildEnforcementRecords{}
		if err := json.Unmarshal([]byte(obj.GetValue()), allRecords[obj.GetKey()]); err != nil {
			return nil, err
		}
	}
	return allRecords, nil
}
func EnforcementCommunityValuesSearch(ctx context.Context, nk runtime.NakamaModule, groupID, userID string) (map[string]*GuildEnforcementRecords, error) {

	qparts := []string{
		fmt.Sprintf(`+value.records.user_id:%s`, Query.Escape(userID)),
		fmt.Sprintf("+value.records.group_id:%s", Query.Escape(groupID)),
		"+value.records.community_values_required:true",
	}

	query := strings.Join(qparts, " ")
	orderBy := []string{"value.created_at"}

	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, StorageCollectionEnforcementJournalIndex, query, 100, orderBy, "")
	if err != nil {
		return nil, err
	}
	allRecords := make(map[string]*GuildEnforcementRecords, len(objs.GetObjects()))

	for _, obj := range objs.GetObjects() {

		allRecords[obj.GetKey()] = &GuildEnforcementRecords{}
		if err := json.Unmarshal([]byte(obj.GetValue()), allRecords[obj.GetKey()]); err != nil {
			return nil, err
		}
	}
	return allRecords, nil
}
