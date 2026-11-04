package server

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// These pin the pool placement the login code applied before it was extracted
// into applyNewPlayerPool, so the extraction is provably behaviour-neutral.

func TestApplyNewPlayerPool_Characterization(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	tests := []struct {
		name         string
		divisions    []string
		excluded     []string
		created      time.Time
		haveCreated  bool
		maxAgeDays   int
		isModerator  bool
		wantDiv      []string
		wantExcluded []string
		wantUpdated  bool
	}{
		{
			name: "young account is put in the pool", created: now.Add(-1 * day), haveCreated: true, maxAgeDays: 7,
			wantDiv: []string{"green"}, wantExcluded: nil, wantUpdated: true,
		},
		{
			name: "young account already in the pool is unchanged", divisions: []string{"green"}, created: now.Add(-1 * day), haveCreated: true, maxAgeDays: 7,
			wantDiv: []string{"green"}, wantExcluded: nil, wantUpdated: false,
		},
		{
			name: "young account loses its exclusion", divisions: []string{"green"}, excluded: []string{"green"}, created: now.Add(-1 * day), haveCreated: true, maxAgeDays: 7,
			wantDiv: []string{"green"}, wantExcluded: []string{}, wantUpdated: true,
		},
		{
			name: "account exactly at the age limit is old", created: now.Add(-7 * day), haveCreated: true, maxAgeDays: 7,
			wantDiv: nil, wantExcluded: []string{"green"}, wantUpdated: true,
		},
		{
			name: "old account loses the pool and is excluded", divisions: []string{"green"}, created: now.Add(-30 * day), haveCreated: true, maxAgeDays: 7,
			wantDiv: []string{}, wantExcluded: []string{"green"}, wantUpdated: true,
		},
		{
			name: "old account already excluded is unchanged", excluded: []string{"green"}, created: now.Add(-30 * day), haveCreated: true, maxAgeDays: 7,
			wantDiv: nil, wantExcluded: []string{"green"}, wantUpdated: false,
		},
		{
			name: "old protected moderator keeps the pool", divisions: []string{"green"}, created: now.Add(-30 * day), haveCreated: true, maxAgeDays: 7, isModerator: true,
			wantDiv: []string{"green"}, wantExcluded: nil, wantUpdated: false,
		},
		{
			name: "old moderator without the pool is not excluded", created: now.Add(-30 * day), haveCreated: true, maxAgeDays: 7, isModerator: true,
			wantDiv: nil, wantExcluded: nil, wantUpdated: false,
		},
		{
			name: "account with no creation time is treated as old", haveCreated: false, maxAgeDays: 7,
			wantDiv: nil, wantExcluded: []string{"green"}, wantUpdated: true,
		},
		{
			// GreenDivisionMaxAccountAgeDays has no code default (0), so nobody is young.
			name: "max age 0 puts nobody in the pool", created: now.Add(-1 * time.Minute), haveCreated: true, maxAgeDays: 0,
			wantDiv: nil, wantExcluded: []string{"green"}, wantUpdated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := MatchmakingSettings{Divisions: append([]string(nil), tt.divisions...), ExcludedDivisions: append([]string(nil), tt.excluded...)}
			if tt.divisions == nil {
				settings.Divisions = nil
			}
			if tt.excluded == nil {
				settings.ExcludedDivisions = nil
			}
			got := applyNewPlayerPool(&settings, tt.created, tt.haveCreated, now, tt.maxAgeDays, false, tt.isModerator)
			if got != tt.wantUpdated {
				t.Errorf("updated = %v, want %v", got, tt.wantUpdated)
			}
			if len(settings.Divisions) != len(tt.wantDiv) || (len(tt.wantDiv) > 0 && !reflect.DeepEqual(settings.Divisions, tt.wantDiv)) {
				t.Errorf("Divisions = %v, want %v", settings.Divisions, tt.wantDiv)
			}
			if len(settings.ExcludedDivisions) != len(tt.wantExcluded) || (len(tt.wantExcluded) > 0 && !reflect.DeepEqual(settings.ExcludedDivisions, tt.wantExcluded)) {
				t.Errorf("ExcludedDivisions = %v, want %v", settings.ExcludedDivisions, tt.wantExcluded)
			}
		})
	}
}

// A graduated account is kept out of the new-player pool even when it is under
// the age limit. The graduated mark is read at login; it takes effect at the
// next login after the account was graduated.
func TestApplyNewPlayerPool_Graduated(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	young := now.Add(-24 * time.Hour)

	t.Run("a graduated young account is not put in the pool and is excluded", func(t *testing.T) {
		s := MatchmakingSettings{}
		if !applyNewPlayerPool(&s, young, true, now, 7, true, false) {
			t.Error("expected the settings to change")
		}
		if len(s.Divisions) != 0 || !reflect.DeepEqual(s.ExcludedDivisions, []string{"green"}) {
			t.Errorf("Divisions=%v Excluded=%v; want no pool and green excluded", s.Divisions, s.ExcludedDivisions)
		}
	})
	t.Run("a graduated young account in the pool loses it", func(t *testing.T) {
		s := MatchmakingSettings{Divisions: []string{"green"}}
		if !applyNewPlayerPool(&s, young, true, now, 7, true, false) {
			t.Error("expected the settings to change")
		}
		if len(s.Divisions) != 0 || !reflect.DeepEqual(s.ExcludedDivisions, []string{"green"}) {
			t.Errorf("Divisions=%v Excluded=%v; want it out of the pool", s.Divisions, s.ExcludedDivisions)
		}
	})
	t.Run("a graduated moderator who holds the pool keeps it, as an old moderator does", func(t *testing.T) {
		s := MatchmakingSettings{Divisions: []string{"green"}}
		if applyNewPlayerPool(&s, young, true, now, 7, true, true) {
			t.Error("a protected moderator's settings changed")
		}
		if !reflect.DeepEqual(s.Divisions, []string{"green"}) {
			t.Errorf("Divisions=%v, want the moderator's pool kept", s.Divisions)
		}
	})
	t.Run("a graduated account already excluded is unchanged", func(t *testing.T) {
		s := MatchmakingSettings{ExcludedDivisions: []string{"green"}}
		if applyNewPlayerPool(&s, young, true, now, 7, true, false) {
			t.Error("settings changed with nothing to do")
		}
	})
	t.Run("not graduated is unchanged: a young account is still put in the pool", func(t *testing.T) {
		s := MatchmakingSettings{}
		applyNewPlayerPool(&s, young, true, now, 7, false, false)
		if !reflect.DeepEqual(s.Divisions, []string{"green"}) {
			t.Errorf("Divisions=%v, want green", s.Divisions)
		}
	})
	t.Run("graduated changes nothing where the pool is never assigned (max age 0)", func(t *testing.T) {
		a, b := MatchmakingSettings{}, MatchmakingSettings{}
		applyNewPlayerPool(&a, young, true, now, 0, true, false)
		applyNewPlayerPool(&b, young, true, now, 0, false, false)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("graduated and not differ at max age 0: %+v vs %+v", a, b)
		}
	})
}

func TestLoginGraduated_ReadsTheMarkAndFailsToAgePlacementLoudly(t *testing.T) {
	ctx := context.Background()
	const uid = "00000000-0000-4000-8000-0000000000c1"
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	nk := newOCCTestNakamaModule()
	if loginGraduated(ctx, nk, logger, uid) {
		t.Error("an account with no stored mark reads as graduated")
	}
	a := AmbassadorAccount{Graduated: true, version: "*"}
	if err := StorableWrite(ctx, nk, uid, &a); err != nil {
		t.Fatal(err)
	}
	if !loginGraduated(ctx, nk, logger, uid) {
		t.Error("a stored graduated mark was not read")
	}
	if logs.Len() != 0 {
		t.Errorf("a successful read logged: %v", logs.All())
	}

	failing := &graduatedReadFailsNK{occTestNakamaModule: nk}
	if loginGraduated(ctx, failing, logger, uid) {
		t.Error("a failed read reported graduated")
	}
	if logs.FilterMessageSnippet("Failed to load the graduated mark").Len() != 1 {
		t.Errorf("a failed read was not logged at Warn: %v", logs.All())
	}
}

type graduatedReadFailsNK struct{ *occTestNakamaModule }

func (n *graduatedReadFailsNK) StorageRead(context.Context, []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return nil, errors.New("storage down")
}
