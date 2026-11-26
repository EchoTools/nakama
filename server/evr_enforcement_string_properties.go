package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// String properties are free-form key/value tags on an enforcement record
// (GuildEnforcementRecord.StringProperties). The issuing side sets them when it
// writes the record (the portal sends e.g. tier=high); nakama stores them and
// never interprets what a property means. An inheriting guild filters on them
// with SuspensionInheritanceFilter's include and exclude rules, by exact string
// match. See docs/spec-suspension-inheritance-string-properties.md.
//
// Nakama also writes the keys below itself, from the record's own fields. They
// are nakama-owned: a value a caller sends for one is overwritten.
const (
	// StringPropertyPermanent is "true" when the record lasts
	// LifetimeDurationThreshold or longer, else "false".
	StringPropertyPermanent = "permanent"
	// StringPropertyAllowPrivates mirrors AllowPrivateLobbies.
	StringPropertyAllowPrivates = "allow_privates"
	// StringPropertyCommunityValuesRequired mirrors CommunityValuesRequired.
	StringPropertyCommunityValuesRequired = "community_values_required"
	// StringPropertyIsPubliclyVisible mirrors IsPubliclyVisible.
	StringPropertyIsPubliclyVisible = "is_publicly_visible"
	// StringPropertyRuleViolated mirrors RuleViolated, and is absent when that is empty.
	StringPropertyRuleViolated = "rule_violated"
)

// Bounds on caller-supplied string properties and on inheritance rules. They keep
// a record and a guild's metadata small; nothing else depends on the numbers.
const (
	maxStringPropertyBytes  = 64
	maxStringProperties     = 32
	maxInheritanceRulesList = 32
)

// nakamaStringPropertyKeys lists the nakama-owned keys.
var nakamaStringPropertyKeys = []string{
	StringPropertyPermanent,
	StringPropertyAllowPrivates,
	StringPropertyCommunityValuesRequired,
	StringPropertyIsPubliclyVisible,
	StringPropertyRuleViolated,
}

// validateStringProperties checks caller-supplied properties. allowEmptyValue is
// true for an edit, where an empty value deletes the key.
func validateStringProperties(props map[string]string, allowEmptyValue bool) error {
	if len(props) > maxStringProperties {
		return fmt.Errorf("string_properties: at most %d keys, got %d", maxStringProperties, len(props))
	}
	for k, v := range props {
		if k == "" {
			return fmt.Errorf("string_properties: a key must not be empty")
		}
		if len(k) > maxStringPropertyBytes {
			return fmt.Errorf("string_properties: key %q is longer than %d bytes", k, maxStringPropertyBytes)
		}
		if len(v) > maxStringPropertyBytes {
			return fmt.Errorf("string_properties[%q]: value is longer than %d bytes", k, maxStringPropertyBytes)
		}
		if v == "" && !allowEmptyValue {
			return fmt.Errorf("string_properties[%q]: value must not be empty", k)
		}
	}
	return nil
}

// nakamaStringProperties returns the nakama-owned facts of r.
func (r GuildEnforcementRecord) nakamaStringProperties() map[string]string {
	props := map[string]string{
		StringPropertyPermanent:               strconv.FormatBool(r.Expiry.Sub(r.CreatedAt) >= LifetimeDurationThreshold),
		StringPropertyAllowPrivates:           strconv.FormatBool(r.AllowPrivateLobbies),
		StringPropertyCommunityValuesRequired: strconv.FormatBool(r.CommunityValuesRequired),
		StringPropertyIsPubliclyVisible:       strconv.FormatBool(r.IsPubliclyVisible),
	}
	if r.RuleViolated != "" {
		props[StringPropertyRuleViolated] = r.RuleViolated
	}
	return props
}

// mergeStringProperties applies changes to r.StringProperties (an empty value
// deletes the key) and then rewrites the nakama-owned keys from r's fields. It
// returns the nakama-owned keys the caller sent, sorted, so the caller can log them.
func (r *GuildEnforcementRecord) mergeStringProperties(changes map[string]string) []string {
	var owned []string
	merged := maps.Clone(r.StringProperties)
	if merged == nil {
		merged = make(map[string]string, len(changes)+len(nakamaStringPropertyKeys))
	}
	for k, v := range changes {
		if slices.Contains(nakamaStringPropertyKeys, k) {
			owned = append(owned, k)
			continue
		}
		if v == "" {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	for _, k := range nakamaStringPropertyKeys {
		delete(merged, k)
	}
	maps.Copy(merged, r.nakamaStringProperties())
	r.StringProperties = merged
	slices.Sort(owned)
	return owned
}

// StringProperty returns the value of key on r. A stored value wins. A
// nakama-owned key a record lacks (every record written before string properties
// existed) is derived from the record's fields, so such a record still matches a
// rule on, say, permanent=true.
func (r GuildEnforcementRecord) StringProperty(key string) (string, bool) {
	if v, ok := r.StringProperties[key]; ok {
		return v, true
	}
	if !slices.Contains(nakamaStringPropertyKeys, key) {
		return "", false
	}
	v, ok := r.nakamaStringProperties()[key]
	return v, ok
}

// SetRecordStringProperties merges props into a record already in the journal and
// rewrites its nakama-owned keys. It returns the updated record (nil if not found)
// and the nakama-owned keys props carried.
func (s *GuildEnforcementJournal) SetRecordStringProperties(groupID, recordID string, props map[string]string) (*GuildEnforcementRecord, []string) {
	record := s.GetRecord(groupID, recordID)
	if record == nil {
		return nil, nil
	}
	owned := record.mergeStringProperties(props)
	return record, owned
}

// enforcementRecordEdit is the full set of values an edit writes to a record.
type enforcementRecordEdit struct {
	Expiry                  time.Time
	UserNotice              string
	AuditorNotes            string
	AllowPrivateLobbies     bool
	CommunityValuesRequired bool
	// StringProperties is merged into the record: an empty value deletes the key.
	StringProperties map[string]string
}

// ApplyRecordEdit writes e to a record, appends an EditLog entry and rewrites the
// record's nakama-owned string properties. It returns the updated record (nil if
// not found) and the nakama-owned keys e.StringProperties carried.
func (s *GuildEnforcementJournal) ApplyRecordEdit(groupID, recordID, editorUserID, editorDiscordID string, e enforcementRecordEdit) (*GuildEnforcementRecord, []string) {
	record := s.GetRecord(groupID, recordID)
	if record == nil {
		return nil, nil
	}
	editEntry := GuildEnforcementEditEntry{
		EditorUserID:           editorUserID,
		EditorDiscordID:        editorDiscordID,
		EditedAt:               time.Now().UTC(),
		PreviousExpiry:         record.Expiry,
		PreviousUserNoticeText: record.UserNoticeText,
		PreviousAuditorNotes:   record.AuditorNotes,
		NewExpiry:              e.Expiry,
		NewUserNoticeText:      e.UserNotice,
		NewAuditorNotes:        e.AuditorNotes,
		PreviousAllowPrivates:  record.AllowPrivateLobbies,
		NewAllowPrivates:       e.AllowPrivateLobbies,

		PreviousCommunityValuesRequired: record.CommunityValuesRequired,
		NewCommunityValuesRequired:      e.CommunityValuesRequired,
		PreviousStringProperties:        maps.Clone(record.StringProperties),
	}
	record.Expiry = e.Expiry
	record.UserNoticeText = e.UserNotice
	record.AuditorNotes = e.AuditorNotes
	record.AllowPrivateLobbies = e.AllowPrivateLobbies
	record.CommunityValuesRequired = e.CommunityValuesRequired
	owned := record.mergeStringProperties(e.StringProperties)
	editEntry.NewStringProperties = maps.Clone(record.StringProperties)
	record.UpdatedAt = time.Now().UTC()
	record.EditLog = append(record.EditLog, editEntry)
	return record, owned
}

// previewRecordEdit returns the record as ApplyRecordEdit would leave it, without
// touching the journal, so a caller can tell whether an edit changes anything.
func previewRecordEdit(r GuildEnforcementRecord, e enforcementRecordEdit) GuildEnforcementRecord {
	r.Expiry = e.Expiry
	r.UserNoticeText = e.UserNotice
	r.AuditorNotes = e.AuditorNotes
	r.AllowPrivateLobbies = e.AllowPrivateLobbies
	r.CommunityValuesRequired = e.CommunityValuesRequired
	r.mergeStringProperties(e.StringProperties)
	return r
}

// warnNakamaOwnedStringProperties logs the nakama-owned keys a caller sent. Their
// values were overwritten with nakama's own; the caller should stop sending them.
func warnNakamaOwnedStringProperties(logger runtime.Logger, rpc string, owned []string) {
	if len(owned) > 0 {
		LogWarn(logger, "RPC request set nakama-owned string properties; nakama's values were kept",
			zap.String("rpc", rpc), zap.Strings("keys", owned))
	}
}

// logUnknownJSONKeys logs, at warn, the names of the top-level keys in payload
// that v's struct has no json field for. Values are never logged. It exists so
// drift between what the portal sends and what nakama reads shows up in the log
// instead of as a silently dropped field.
func logUnknownJSONKeys(logger runtime.Logger, rpc string, payload string, v any) {
	if unknown := unknownJSONKeys(payload, v); len(unknown) > 0 {
		LogWarn(logger, "RPC request carried unknown keys; the server ignored them",
			zap.String("rpc", rpc), zap.Strings("unknown_keys", unknown))
	}
}

// unknownJSONKeys returns, sorted, the top-level keys of payload that the struct
// v points to has no json field for. A payload that is not a JSON object yields nil.
func unknownJSONKeys(payload string, v any) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	known := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = t.Field(i).Name
		}
		known[strings.ToLower(name)] = true
	}
	var unknown []string
	for k := range raw {
		// encoding/json matches field names case-insensitively, so this does too.
		if !known[strings.ToLower(k)] {
			unknown = append(unknown, k)
		}
	}
	slices.Sort(unknown)
	return unknown
}
