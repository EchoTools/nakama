package server

import (
	"strings"
	"testing"
	"time"
)

func TestCreatePastDisplayNameEmbed(t *testing.T) {
	tests := []struct {
		name          string
		history       *DisplayNameHistory
		groupID       string
		expectedNames []string
		expectedNil   bool
	}{
		{
			name:        "nil history",
			history:     nil,
			groupID:     "",
			expectedNil: true,
		},
		{
			name: "empty history",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{},
			},
			groupID:     "",
			expectedNil: true,
		},
		{
			name: "single group with multiple names - most recent first",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"OldName":    time.Now().Add(-48 * time.Hour),
						"NewerName":  time.Now().Add(-24 * time.Hour),
						"NewestName": time.Now().Add(-1 * time.Hour),
					},
				},
			},
			groupID: "",
			expectedNames: []string{
				"`OldName`",
				"`NewerName`",
				"`NewestName`",
			},
		},
		{
			name: "multiple groups - filter by groupID",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Group1Name": time.Now().Add(-24 * time.Hour),
					},
					"group2": {
						"Group2Name": time.Now().Add(-12 * time.Hour),
					},
				},
			},
			groupID: "group1",
			expectedNames: []string{
				"`Group1Name`",
			},
		},
		{
			name: "more than 10 names - should limit to most recent 10",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"Name01": time.Now().Add(-100 * time.Hour),
						"Name02": time.Now().Add(-90 * time.Hour),
						"Name03": time.Now().Add(-80 * time.Hour),
						"Name04": time.Now().Add(-70 * time.Hour),
						"Name05": time.Now().Add(-60 * time.Hour),
						"Name06": time.Now().Add(-50 * time.Hour),
						"Name07": time.Now().Add(-40 * time.Hour),
						"Name08": time.Now().Add(-30 * time.Hour),
						"Name09": time.Now().Add(-20 * time.Hour),
						"Name10": time.Now().Add(-10 * time.Hour),
						"Name11": time.Now().Add(-5 * time.Hour),
						"Name12": time.Now().Add(-1 * time.Hour),
					},
				},
			},
			groupID: "",
			expectedNames: []string{
				"`Name03`",
				"`Name04`",
				"`Name05`",
				"`Name06`",
				"`Name07`",
				"`Name08`",
				"`Name09`",
				"`Name10`",
				"`Name11`",
				"`Name12`",
			},
		},
		{
			name: "duplicate names across groups - should keep earliest timestamp",
			history: &DisplayNameHistory{
				Histories: map[string]map[string]time.Time{
					"group1": {
						"SharedName": time.Now().Add(-48 * time.Hour),
					},
					"group2": {
						"SharedName": time.Now().Add(-24 * time.Hour),
					},
				},
			},
			groupID: "",
			expectedNames: []string{
				"`SharedName`",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WhoAmI{}
			embed := w.createPastDisplayNameEmbed(tt.history, tt.groupID)

			if tt.expectedNil {
				if embed != nil {
					t.Errorf("expected nil embed, got %+v", embed)
				}
				return
			}

			if embed == nil {
				t.Fatalf("expected non-nil embed, got nil")
			}

			if embed.Title != "Past Display Names" {
				t.Errorf("expected title 'Past Display Names', got %q", embed.Title)
			}

			if len(embed.Fields) != 1 {
				t.Fatalf("expected 1 field, got %d", len(embed.Fields))
			}

			field := embed.Fields[0]
			if field.Name != "Display Names" {
				t.Errorf("expected field name 'Display Names', got %q", field.Name)
			}

			// Split the value to get individual names
			actualNames := strings.Split(field.Value, "\n")

			// Check count
			if len(actualNames) != len(tt.expectedNames) {
				t.Errorf("expected %d names, got %d: %v", len(tt.expectedNames), len(actualNames), actualNames)
			}

			// Check that all expected names are present (order matters due to timestamp sorting)
			for i, expectedName := range tt.expectedNames {
				if i >= len(actualNames) {
					t.Errorf("missing expected name at position %d: %q", i, expectedName)
					continue
				}
				if actualNames[i] != expectedName {
					t.Errorf("at position %d: expected %q, got %q", i, expectedName, actualNames[i])
				}
			}
		})
	}
}

/*
func Test_parseTime(t *testing.T) {
	type args struct {
		s string
	}
	tests := []struct {
		name      string
		args      args
		want      time.Time
		wantError bool
	}{
		{
			name: "parse time ``",
			args: args{"0"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC),
		},
		{
			name: "parse time missing timezone",
			args: args{"8p"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC),
		},
		{
			name: "parse time 0",
			args: args{"0"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC),
		},
		{
			name: "parse minutes from now",
			args: args{"+5m"},
			want: time.Now().Add(5 * time.Minute),
		},
		{
			name: "parse time 8p est",
			args: args{"8p est"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 20, 0, 0, 0, time.UTC),
		},
		{
			name: "parse time 8p CST",
			args: args{"8p CST"},
			want: time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 20, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.args.s)
			if err != nil {
				if !tt.wantError {
					t.Errorf("parseTime() error = %v, wantErr %v", err, tt.wantError)
				} else {
					if !reflect.DeepEqual(got, tt.want) {
						t.Errorf("parseTime() = %v, want %v", got, tt.want)
					}
				}
			}
		})

	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTime(tt.args.s)

			if err != nil {
				if !tt.wantError {
					t.Errorf("parseTime() error = %v, wantErr %v", err, tt.wantError)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseTime() = %v, want %v", got, tt.want)
			}
		})
	}
}
*/
