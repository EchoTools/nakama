package intents

import (
	"reflect"
	"strconv"
	"testing"
)

func TestIntent_MarshalText(t *testing.T) {
	tests := []struct {
		name   string
		intent Intent
		want   string
	}{
		{
			name:   "AllFalse",
			intent: Intent{},
			want:   strconv.QuoteToASCII(""),
		},
		{
			name:   "GuildMatchesTrue",
			intent: Intent{GuildMatches: true},
			want:   strconv.QuoteToASCII("guild_matches"),
		},
		{
			name:   "MatchesTrue",
			intent: Intent{Matches: true},
			want:   strconv.QuoteToASCII("matches"),
		},
		{
			// "storage", not "storage_objects": the token is the struct tag,
			// and the tag is the wire vocabulary. See TestIntent_TextRoundTrip.
			name:   "StorageObjectsTrue",
			intent: Intent{StorageObjects: true},
			want:   strconv.QuoteToASCII("storage"),
		},
		{
			name:   "MultipleTrue",
			intent: Intent{GuildMatches: true, Matches: true, StorageObjects: true},
			want:   strconv.QuoteToASCII("guild_matches,matches,storage"),
		},
		{
			name:   "TwoTrue",
			intent: Intent{GuildMatches: true, Matches: true},
			want:   strconv.QuoteToASCII("guild_matches,matches"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.intent.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v, want nil", err)
			}
			if string(got) != tt.want {
				t.Errorf("MarshalText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIntent_TextRoundTrip is the reason MarshalText emits "storage" and not
// "storage_objects", and it exists so that answer stops depending on someone
// remembering it.
//
// UnmarshalText is a live wire path: evr_runtime_rpc.go parses a
// caller-supplied IntentStr with it. Both directions read the same `json`
// struct tags, so the tags -- not the Go field names -- are the vocabulary.
// Emitting a token the parser cannot read back would silently drop the intent,
// and a dropped intent is a permission that quietly fails to be granted.
//
// Every field is covered by construction, so a field added without a tag, or
// with a tag the other direction does not recognise, fails here.
func TestIntent_TextRoundTrip(t *testing.T) {
	typ := reflect.TypeOf(Intent{})

	for idx := 0; idx < typ.NumField(); idx++ {
		field := typ.Field(idx)
		t.Run(field.Name, func(t *testing.T) {
			if field.Tag.Get("json") == "" {
				t.Fatalf("field %s has no json tag; MarshalText would drop it silently", field.Name)
			}

			var want Intent
			reflect.ValueOf(&want).Elem().Field(idx).SetBool(true)

			text, err := want.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v, want nil", err)
			}

			var got Intent
			if err := got.UnmarshalText(text); err != nil {
				t.Fatalf("UnmarshalText(%s) error = %v, want nil", text, err)
			}
			if got != want {
				t.Errorf("round trip through %s = %+v, want %+v", text, got, want)
			}
		})
	}

	t.Run("AllFieldsAtOnce", func(t *testing.T) {
		want := Intent{}
		v := reflect.ValueOf(&want).Elem()
		for idx := 0; idx < v.NumField(); idx++ {
			v.Field(idx).SetBool(true)
		}

		text, err := want.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText() error = %v, want nil", err)
		}

		var got Intent
		if err := got.UnmarshalText(text); err != nil {
			t.Fatalf("UnmarshalText(%s) error = %v, want nil", text, err)
		}
		if got != want {
			t.Errorf("round trip through %s = %+v, want %+v", text, got, want)
		}
	})
}

// TestIntent_UnmarshalText_WireVocabulary pins the exact tokens a caller may
// send, because callers are outside this repository and cannot be migrated by
// editing it. Renaming a tag is a breaking wire change and must fail here.
func TestIntent_UnmarshalText_WireVocabulary(t *testing.T) {
	tests := []struct {
		token string
		want  Intent
	}{
		{"guild_matches", Intent{GuildMatches: true}},
		{"matches", Intent{Matches: true}},
		{"storage", Intent{StorageObjects: true}},
		{"global_bot", Intent{IsGlobalBot: true}},
		{"global_operator", Intent{IsGlobalOperator: true}},
		{"global_developer", Intent{IsGlobalDeveloper: true}},
	}

	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			var got Intent
			if err := got.UnmarshalText([]byte(tt.token)); err != nil {
				t.Fatalf("UnmarshalText(%q) error = %v, want nil", tt.token, err)
			}
			if got != tt.want {
				t.Errorf("UnmarshalText(%q) = %+v, want %+v", tt.token, got, tt.want)
			}
		})
	}

	// An unrecognised token grants nothing rather than granting something
	// adjacent. "storage_objects" is here specifically: it is the Go field
	// name, it is what the old test expected, and a caller sending it gets
	// no storage access.
	t.Run("UnknownTokenGrantsNothing", func(t *testing.T) {
		var got Intent
		if err := got.UnmarshalText([]byte("storage_objects")); err != nil {
			t.Fatalf("UnmarshalText() error = %v, want nil", err)
		}
		if got != (Intent{}) {
			t.Errorf("UnmarshalText(\"storage_objects\") = %+v, want zero Intent", got)
		}
	})
}
