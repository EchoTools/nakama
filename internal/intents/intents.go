package intents

import (
	"context"
	"reflect"
	"strconv"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

type Intent struct {
	GuildMatches      bool `json:"guild_matches"` // Access to guild matches.
	Matches           bool `json:"matches"`       // Access to all matches.
	StorageObjects    bool `json:"storage"`       // Access to read/write all storage objects.
	IsGlobalOperator  bool `json:"global_operator"`
	IsGlobalDeveloper bool `json:"global_developer"`
}

func (i Intent) MarshalText() ([]byte, error) {
	// Marshal as a comma separated list.
	var parts []string
	v := reflect.ValueOf(i)
	t := reflect.TypeOf(i)
	for idx := 0; idx < v.NumField(); idx++ {
		if v.Field(idx).Bool() {
			tag := t.Field(idx).Tag.Get("json")
			if tag != "" {
				parts = append(parts, tag)
			}
		}
	}
	return []byte(strconv.QuoteToASCII(strings.Join(parts, ","))), nil
}

func (i *Intent) UnmarshalText(data []byte) error {
	// Unmarshal from a comma separated list using reflection.
	str := strings.Trim(string(data), "\"")
	if str == "" {
		return nil // No intents set, nothing to do.
	}

	parts := strings.Split(str, ",")
	t := reflect.TypeOf(*i)
	v := reflect.ValueOf(i).Elem()

	for _, part := range parts {
		for idx := 0; idx < t.NumField(); idx++ {
			tag := t.Field(idx).Tag.Get("json")
			if tag == part {
				v.Field(idx).SetBool(true)
				break
			}
		}
	}
	return nil
}

func (i Intent) PackAsBits() int {
	var bits int
	v := reflect.ValueOf(i)
	for idx := 0; idx < v.NumField(); idx++ {
		if v.Field(idx).Bool() {
			bits |= 1 << idx
		}
	}
	return bits
}

func (i Intent) UnpackFromBits(bits int) Intent {
	var intent Intent
	v := reflect.ValueOf(&intent).Elem()
	for idx := 0; idx < v.NumField(); idx++ {
		if bits&(1<<idx) != 0 {
			v.Field(idx).SetBool(true)
		}
	}
	return intent
}

func (i Intent) String() string {
	// Convert the intent to a string representation by packing it as bits.
	bits := i.PackAsBits()
	if bits == 0 {
		return "" // Return an empty string if no intents are set.
	}
	// Convert the bits to a string representation.
	return strconv.Itoa(bits)
}

func IntentFromString(s string) (Intent, error) {
	if s == "" {
		return Intent{}, nil // Return an empty intent if the string is empty.
	}
	bits, err := strconv.Atoi(s)
	if err != nil {
		return Intent{}, err // Return an error if the string cannot be converted to an integer.
	}

	intent := Intent{}
	intent = intent.UnpackFromBits(bits)
	return intent, nil
}

type SessionVars struct {
	Intents           Intent
	IsGlobalOperator  bool   // Whether the user is a member of the "Global Operators" group.
	IsGlobalDeveloper bool   // Whether the user is a member of the "Global Developers" group.
	GuildID           string // Optional guild ID for the session.
}

func (s *SessionVars) MarshalVars() map[string]string {

	intentMap := map[string]string{
		"int": s.Intents.String(),
		"gid": s.GuildID,
	}

	for k, v := range intentMap {
		if v == "" {
			delete(intentMap, k) // Remove empty values to keep the map clean.
		}
	}
	if len(intentMap) == 0 {
		return map[string]string{}
	}

	return intentMap
}

func (s *SessionVars) UnmarshalVars(vars map[string]string) error {
	intentStr, exists := vars["int"]
	if !exists {
		return nil // No intents set, nothing to do.
	}
	intent, err := IntentFromString(intentStr)
	if err != nil {
		return err
	}
	s.Intents = intent

	guildID, exists := vars["gid"]
	if exists {
		s.GuildID = guildID // Set the guild ID if it exists.
	} else {
		s.GuildID = "" // Default to an empty string if no guild ID is set
	}

	return nil
}

func SessionVarsFromRuntimeContext(ctx context.Context) (*SessionVars, error) {
	vars, ok := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string)
	if !ok {
		return nil, nil // No session variables found.
	}

	sessionVars := &SessionVars{}
	if err := sessionVars.UnmarshalVars(vars); err != nil {
		return nil, err
	}

	return sessionVars, nil
}
