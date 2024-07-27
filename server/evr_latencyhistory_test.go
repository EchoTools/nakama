package server

import (
	"reflect"
	"testing"
	"time"
)

func TestLatencyHistoryData_AddRecord(t *testing.T) {
	type fields struct {
		History []map[string]time.Duration
	}
	type args struct {
		record map[string]time.Duration
		rtt    int
		limit  int
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []map[string]time.Duration
	}{
		{
			name: "History is reduced to given limit",
			fields: fields{
				History: []map[string]time.Duration{
					{"1": 1 * time.Millisecond},
					{"2": 2 * time.Millisecond},
					{"3": 3 * time.Millisecond},
					{"4": 4 * time.Millisecond},
					{"5": 5 * time.Millisecond},
				},
			},
			args: args{
				record: map[string]time.Duration{"6": 6 * time.Millisecond},
				rtt:    6,
				limit:  5,
			},
			want: []map[string]time.Duration{
				{"2": 2 * time.Millisecond},
				{"3": 3 * time.Millisecond},
				{"4": 4 * time.Millisecond},
				{"5": 5 * time.Millisecond},
				{"6": 6 * time.Millisecond},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &LatencyHistoryData{
				History: tt.fields.History,
			}
			l.AddRecord(tt.args.record, tt.args.limit)
		})
	}
}

func TestLatencyHistoryData_GetAveragesByIP(t *testing.T) {
	type fields struct {
		History []map[string]time.Duration
	}
	type args struct {
		ips []string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []time.Duration
	}{
		{
			name: "Returns correct averages",
			fields: fields{
				History: []map[string]time.Duration{
					{"1": 1 * time.Millisecond, "2": 2 * time.Millisecond, "3": 3 * time.Millisecond},
					{"1": 2 * time.Millisecond, "2": 3 * time.Millisecond, "3": 4 * time.Millisecond},
					{"1": 3 * time.Millisecond, "2": 4 * time.Millisecond, "3": 5 * time.Millisecond},
				},
			},
			args: args{
				ips: []string{"1", "3"},
			},
			want: []time.Duration{2 * time.Millisecond, 4 * time.Millisecond},
		},
		{
			name: "Returns empty array if no history",
			fields: fields{
				History: nil,
			},
			args: args{
				ips: []string{"1", "2", "3"},
			},
			want: []time.Duration{},
		},
		{
			name: "Returns empty array if no ips",
			fields: fields{
				History: []map[string]time.Duration{
					{"1": 1 * time.Millisecond, "2": 2 * time.Millisecond, "3": 3 * time.Millisecond},
					{"1": 2 * time.Millisecond, "2": 3 * time.Millisecond, "3": 4 * time.Millisecond},
					{"1": 3 * time.Millisecond, "2": 4 * time.Millisecond, "3": 5 * time.Millisecond},
				},
			},
			args: args{
				ips: []string{},
			},
			want: []time.Duration{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &LatencyHistoryData{
				History: tt.fields.History,
			}
			if got := l.GetAveragesByIP(tt.args.ips); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LatencyHistoryData.GetAveragesByIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLatencyHistoryData_GetLeastFrequent(t *testing.T) {
	type fields struct {
		History []map[string]time.Duration
	}
	tests := []struct {
		name   string
		fields fields
		want   []string
	}{
		{
			name: "Returns correct order",
			fields: fields{
				History: []map[string]time.Duration{
					{"1": 1 * time.Millisecond, "2": 2 * time.Millisecond, "3": 3 * time.Millisecond},
					{"2": 2 * time.Millisecond, "3": 3 * time.Millisecond},
					{"3": 3 * time.Millisecond},
					{"4": 4 * time.Millisecond},
				},
			},
			want: []string{"1", "2", "4", "3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &LatencyHistoryData{
				History: tt.fields.History,
			}
			if got := l.GetLeastFrequent(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LatencyHistoryData.GetLeastFrequent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLatencyHistoryData_SortLeastFrequentByIP(t *testing.T) {
	type fields struct {
		History []map[string]time.Duration
	}
	type args struct {
		ips []string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
		want   []string
	}{
		{
			name: "Returns correct order",
			fields: fields{
				History: []map[string]time.Duration{
					{"1": 1 * time.Millisecond, "2": 2 * time.Millisecond, "3": 3 * time.Millisecond},
					{"2": 2 * time.Millisecond, "3": 3 * time.Millisecond},
					{"3": 3 * time.Millisecond, "2": 2 * time.Millisecond},
					{"4": 4 * time.Millisecond},
					{"5": 5 * time.Millisecond},
				},
			},
			args: args{
				ips: []string{"4", "1", "3"},
			},
			want: []string{"1", "3", "4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &LatencyHistoryData{
				History: tt.fields.History,
			}
			if got := l.SortLeastFrequentIP(tt.args.ips); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LatencyHistoryData.GetLeastFrequentByIP() = %v, want %v", got, tt.want)
			}
		})
	}
}
