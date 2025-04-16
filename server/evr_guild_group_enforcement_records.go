package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
		if time.Now().Before(r.SuspensionExpiry) {
			active = append(active, r)
		}
	}
	return active
}

func (s *GuildEnforcementRecords) MarshalJSON() ([]byte, error) {

	for i := len(s.Records) - 1; i >= 0; i-- {
		if s.Records[i].IsSuspended() && s.Records[i].SuspensionExpiry.After(s.SuspensionExpiry) {
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
	EnforcerUserID          string    `json:"enforcer_id"`
	CreatedAt               time.Time `json:"created_at"`
	SuspensionNotice        string    `json:"suspension_notice"`
	SuspensionExpiry        time.Time `json:"suspension_expiry"`
	CommunityValuesRequired bool      `json:"community_values_required"`
	Notes                   string    `json:"notes"`
	IsVoid                  bool      `json:"is_void"`
}

func NewGuildEnforcementRecord(enforcerUserID string, suspensionNotice, notes string, requireCommunityValues bool, suspensionExpiry time.Time) *GuildEnforcementRecord {
	return &GuildEnforcementRecord{
		ID:               uuid.Must(uuid.NewV4()).String(),
		EnforcerUserID:   enforcerUserID,
		CreatedAt:        time.Now(),
		SuspensionNotice: suspensionNotice,
		SuspensionExpiry: suspensionExpiry,
		Notes:            notes,
	}
}

func (s *GuildEnforcementRecord) IsExpired() bool {
	return time.Now().After(s.SuspensionExpiry)
}

func (s *GuildEnforcementRecord) IsSuspended() bool {
	return time.Now().Before(s.SuspensionExpiry)
}

func (s *GuildEnforcementRecord) RequiresCommunityValues() bool {
	return s.CommunityValuesRequired
}

func EnforcementSuspensionSearch(ctx context.Context, nk runtime.NakamaModule, groupID string, userIDs []string, includeExpired bool, includeVoided bool) (map[string]map[string]*GuildEnforcementRecords, error) {
	qparts := []string{}

	if !includeVoided {
		qparts = append(qparts, "+value.is_void:F")
	}

	if !includeExpired {
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
		"+value.is_void:F",
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
