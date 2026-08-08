package server

import (
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// now is fixed rather than time.Now() so the boundary cases are boundary cases
// every run, not only when the suite happens to execute away from midnight.
var accountAgeNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func TestAccountTooNew(t *testing.T) {
	tests := []struct {
		name        string
		createdAt   time.Time
		minDays     int
		wantTooNew  bool
		wantAgeDays int
	}{
		{
			name:        "OlderThanGate",
			createdAt:   accountAgeNow.AddDate(0, 0, -30),
			minDays:     7,
			wantTooNew:  false,
			wantAgeDays: 30,
		},
		{
			name:        "YoungerThanGate",
			createdAt:   accountAgeNow.AddDate(0, 0, -2),
			minDays:     7,
			wantTooNew:  true,
			wantAgeDays: 2,
		},
		{
			// Exactly on the cutoff is NOT too new: the check is strictly
			// After, so an account created precisely minDays ago passes.
			name:        "ExactlyOnTheCutoff",
			createdAt:   accountAgeNow.AddDate(0, 0, -7),
			minDays:     7,
			wantTooNew:  false,
			wantAgeDays: 7,
		},
		{
			name:        "OneSecondInsideTheCutoff",
			createdAt:   accountAgeNow.AddDate(0, 0, -7).Add(time.Second),
			minDays:     7,
			wantTooNew:  true,
			wantAgeDays: 6,
		},
		{
			// A brand-new account against a one-day gate: the case the gate
			// exists for.
			name:        "CreatedMomentsAgo",
			createdAt:   accountAgeNow.Add(-time.Minute),
			minDays:     1,
			wantTooNew:  true,
			wantAgeDays: 0,
		},
		{
			// Clock skew or a bad record can put creation in the future. It
			// must read as too new, not wrap into "very old".
			name:        "CreatedInTheFuture",
			createdAt:   accountAgeNow.AddDate(0, 0, 5),
			minDays:     7,
			wantTooNew:  true,
			wantAgeDays: -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTooNew, gotAgeDays := accountTooNew(tt.createdAt, tt.minDays, accountAgeNow)
			if gotTooNew != tt.wantTooNew {
				t.Errorf("accountTooNew(...) tooNew = %v, want %v", gotTooNew, tt.wantTooNew)
			}
			if gotAgeDays != tt.wantAgeDays {
				t.Errorf("accountTooNew(...) ageDays = %d, want %d", gotAgeDays, tt.wantAgeDays)
			}
		})
	}
}

// TestAccountTooNew_DiscordGateMissesFreshNakamaAccount is #516's evasion,
// stated as a test: an aged Discord account carrying a brand-new EchoVR account
// clears the Discord-snowflake gate and is caught only by the Nakama one.
//
// If someone ever "simplifies" the two gates into one, this fails.
func TestAccountTooNew_DiscordGateMissesFreshNakamaAccount(t *testing.T) {
	const minDays = 30

	discordCreatedAt := accountAgeNow.AddDate(-4, 0, 0) // a four-year-old Discord account
	nakamaCreatedAt := accountAgeNow.Add(-2 * time.Hour)

	if tooNew, _ := accountTooNew(discordCreatedAt, minDays, accountAgeNow); tooNew {
		t.Fatalf("precondition failed: the aged Discord account should clear a %d-day gate", minDays)
	}

	tooNew, ageDays := accountTooNew(nakamaCreatedAt, minDays, accountAgeNow)
	if !tooNew {
		t.Errorf("the fresh EchoVR account cleared a %d-day gate; #516's evasion is open", minDays)
	}
	if ageDays != 0 {
		t.Errorf("ageDays = %d, want 0", ageDays)
	}
}

// TestEVRProfileAccountCreateTime covers the reason AccountCreateTime returns a
// second value: an unloaded profile must not read as an infinitely old account.
// Returning the zero time alone would make every age gate pass on a profile
// that simply failed to load -- fail-open on a fail-closed control.
func TestEVRProfileAccountCreateTime(t *testing.T) {
	createdAt := accountAgeNow.AddDate(0, 0, -10)

	tests := []struct {
		name    string
		profile EVRProfile
		wantOK  bool
		want    time.Time
	}{
		{
			name: "Loaded",
			profile: EVRProfile{account: &api.Account{
				User: &api.User{CreateTime: timestamppb.New(createdAt)},
			}},
			wantOK: true,
			want:   createdAt,
		},
		{
			name:    "NilAccount",
			profile: EVRProfile{},
			wantOK:  false,
		},
		{
			name:    "NilUser",
			profile: EVRProfile{account: &api.Account{}},
			wantOK:  false,
		},
		{
			name:    "NilCreateTime",
			profile: EVRProfile{account: &api.Account{User: &api.User{}}},
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.profile.AccountCreateTime()
			if ok != tt.wantOK {
				t.Fatalf("AccountCreateTime() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				// The caller must not be able to mistake this for a real,
				// very old creation time.
				if !got.IsZero() {
					t.Errorf("AccountCreateTime() = %v on a failure, want the zero time", got)
				}
				return
			}
			if !got.Equal(tt.want) {
				t.Errorf("AccountCreateTime() = %v, want %v", got, tt.want)
			}
		})
	}
}
