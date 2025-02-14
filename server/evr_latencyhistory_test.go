package server

import (
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestLatencyHistory_LabelsByAverageRTT(t *testing.T) {

	latencyHistory := LatencyHistory{
		"1.1.1.1":         map[int64]int{time.Now().UTC().Unix(): 10},
		"2.2.2.2":         map[int64]int{time.Now().UTC().Unix(): 20},
		"3.3.3.3":         map[int64]int{time.Now().UTC().Unix(): 30},
		"100.100.100.100": map[int64]int{time.Now().UTC().Unix(): 100},
	}

	labels := []*MatchLabel{
		{
			GameServer: &GameServerPresence{
				Endpoint: evr.Endpoint{
					ExternalIP: net.ParseIP("1.1.1.1"),
				},
			},
		},
		{
			GameServer: &GameServerPresence{
				Endpoint: evr.Endpoint{
					ExternalIP: net.ParseIP("3.3.3.3"),
				},
			},
		},

		{
			GameServer: &GameServerPresence{
				Endpoint: evr.Endpoint{ExternalIP: net.ParseIP("100.100.100.100")},
			},
		},
		{
			GameServer: &GameServerPresence{
				Endpoint: evr.Endpoint{
					ExternalIP: net.ParseIP("2.2.2.2"),
				},
			},
		},
	}

	type args struct {
		labels []*MatchLabel
	}
	tests := []struct {
		name string
		h    LatencyHistory
		args args
		want []LabelWithLatency
	}{
		{
			name: "empty",
			h:    NewLatencyHistory(),
			args: args{
				labels: []*MatchLabel{},
			},
			want: []LabelWithLatency{},
		},
		{
			"sorts correct",
			latencyHistory,
			args{
				labels: labels,
			},
			[]LabelWithLatency{
				{
					Label: labels[0],
					RTT:   10,
				},
				{
					Label: labels[3],
					RTT:   20,
				},
				{
					Label: labels[1],
					RTT:   30,
				},
				{
					Label: labels[2],

					RTT: 100,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.h.LabelsByAverageRTT(tt.args.labels); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LatencyHistory.LabelsByAverageRTT() = %v, want %v", got, tt.want)
			}
		})
	}
}
