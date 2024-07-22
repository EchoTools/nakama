package server

import (
	"net"
	"reflect"
	"testing"
)

func Test_ipToKey(t *testing.T) {
	type args struct {
		ip net.IP
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "IPv4",
			args: args{
				ip: net.ParseIP("192.168.1.1"),
			},
			want: "rttc0a80101",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ipToKey(tt.args.ip); got != tt.want {
				t.Errorf("ipToKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_distributeParties(t *testing.T) {
	partyA1 := &MatchmakerEntry{}
	partyA2 := &MatchmakerEntry{}
	partyA3 := &MatchmakerEntry{}
	partyB1 := &MatchmakerEntry{}
	partyB2 := &MatchmakerEntry{}
	partyB3 := &MatchmakerEntry{}
	partyC1 := &MatchmakerEntry{}
	partyD1 := &MatchmakerEntry{}
	type args struct {
		parties [][]*MatchmakerEntry
	}
	tests := []struct {
		name string
		args args
		want [][]*MatchmakerEntry
	}{
		{
			name: "Test Case 1",
			args: args{
				parties: [][]*MatchmakerEntry{
					{
						partyA1,
						partyA2,
						partyA3,
					},
					{
						partyB1,
						partyB2,
						partyB3,
					},
					{
						partyC1,
					},
					{
						partyD1,
					},
				},
			},
			want: [][]*MatchmakerEntry{
				{
					partyA1,
					partyA2,
					partyA3,
					partyC1,
				},
				{
					partyB1,
					partyB2,
					partyB3,
					partyD1,
				},
			},
		},
		// Add more test cases here
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := distributeParties(tt.args.parties); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("distributeParties() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSortByFrequency(t *testing.T) {
	type args struct {
		items []int
	}
	tests := []struct {
		name string
		args args
		want []int
	}{
		{
			name: "Test Case 1",
			args: args{
				items: []int{
					1,
					2,
					3,
					2,
					1,
					1,
					2,
					2,
				},
			},
			want: []int{
				2,
				1,
				3,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompactedFrequencySort(tt.args.items, true)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CompactedFrequencySort() = %v, want %v", tt.args.items, tt.want)
			}
		})
	}
}
