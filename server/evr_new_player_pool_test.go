package server

import (
	"reflect"
	"testing"
	"time"
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
			got := applyNewPlayerPool(&settings, tt.created, tt.haveCreated, now, tt.maxAgeDays, tt.isModerator)
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
