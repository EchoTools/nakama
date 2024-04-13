package server

import (
	"net"
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
