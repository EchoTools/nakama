package evr

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLobbyPingRequest_Marshal(t *testing.T) {
	endpoints := []Endpoint{
		{
			InternalIP: net.ParseIP("192.168.56.1"),
			ExternalIP: net.ParseIP("172.29.112.1"),
			Port:       6792,
		},
		{
			InternalIP: net.ParseIP("192.168.56.1"),
			ExternalIP: net.ParseIP("172.29.112.1"),
			Port:       6794,
		},
	}

	lobbyPingRequest := NewLobbyPingRequest(275, endpoints)

	expectedResult := []byte{
		0xf6, 0x40, 0xbb, 0x78, 0xa2, 0xe7, 0x8c, 0xbb,
		0xf3, 0xeb, 0xbf, 0x19, 0x87, 0x5f, 0xbf, 0xfa,
		0x20, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x04, 0x00, 0x13, 0x01, 0x00, 0x00,
		0xc0, 0xa8, 0x38, 0x01, 0xac, 0x1d, 0x70, 0x01,
		0x1a, 0x88, 0x00, 0x00, 0xc0, 0xa8, 0x38, 0x01,
		0xac, 0x1d, 0x70, 0x01, 0x1a, 0x8a, 0x00, 0x00,
	}

	// Unmarshal the expected result to a string
	packet, err := ParsePacket(expectedResult)
	if err != nil {
		t.Fatalf("error in Unmarshal: %v", err)
	}

	packetJSON, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("error in Marshal: %v", err)
	}
	log.Println(string(packetJSON))

	result, err := Marshal(lobbyPingRequest)
	if err != nil {
		t.Fatalf("error in MarshalJSON: %v", err)
	}

	if !cmp.Equal(result, expectedResult) {
		t.Error(cmp.Diff(result, expectedResult))
	}
}

func TestEndpoint_MarshalJSON(t *testing.T) {
	internalAddress := net.ParseIP("127.0.0.1")
	externalAddress := net.ParseIP("192.168.0.1")
	port := uint16(8080)

	endpoint := Endpoint{
		InternalIP: internalAddress,
		ExternalIP: externalAddress,
		Port:       port,
	}

	expectedResult := fmt.Sprintf("%s:%s:%d", internalAddress.String(), externalAddress.String(), port)

	resultJson, err := endpoint.MarshalJSON()
	if err != nil {
		t.Fatalf("error in MarshalJSON: %v", err)
	}

	var resultString string
	err = json.Unmarshal(resultJson, &resultString)
	if err != nil {
		t.Fatalf("error in Unmarshal: %v", err)
	}

	if resultString != expectedResult {
		t.Errorf("expected %s, got %s", expectedResult, resultString)
	}
}
func TestEndpoint_UnmarshalJSON(t *testing.T) {
	testCases := []struct {
		name           string
		jsonData       []byte
		expectedError  error
		expectedResult Endpoint
	}{
		{
			name:           "Valid JSON",
			jsonData:       []byte(`"127.0.0.1:192.168.0.1:8080"`),
			expectedError:  nil,
			expectedResult: Endpoint{InternalIP: net.ParseIP("127.0.0.1"), ExternalIP: net.ParseIP("192.168.0.1"), Port: 8080},
		},
		{
			name:           "Empty String",
			jsonData:       []byte(`""`),
			expectedError:  nil,
			expectedResult: Endpoint{},
		},
		// Add more test cases here if needed
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var endpoint Endpoint
			err := endpoint.UnmarshalJSON(tc.jsonData)
			if err != tc.expectedError {
				t.Errorf("expected error: %v, got: %v", tc.expectedError, err)
			}
			if !reflect.DeepEqual(endpoint, tc.expectedResult) {
				t.Errorf("expected result: %v, got: %v", tc.expectedResult, endpoint)
			}
		})
	}
}

// TestEndpoint_SanitizedForClient covers the issue #465 sanitization at the
// Endpoint level: non-routable external addresses are replaced with 0.0.0.0
// (the EVR client's "skip" sentinel), routable external addresses and the LAN
// internal address are preserved, and the port is always kept.
func TestEndpoint_SanitizedForClient(t *testing.T) {
	tests := []struct {
		name         string
		in           Endpoint
		wantExternal net.IP
		wantInternal net.IP
	}{
		{
			name:         "public external untouched",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("203.0.113.7"), Port: 6792},
			wantExternal: net.ParseIP("203.0.113.7"),
			wantInternal: net.ParseIP("192.168.1.10"),
		},
		{
			name:         "rfc1918 external zeroed, internal kept",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("172.16.5.5"), Port: 6792},
			wantExternal: net.IPv4zero,
			wantInternal: net.ParseIP("192.168.1.10"),
		},
		{
			name:         "cgnat external zeroed",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("100.127.0.1"), Port: 6792},
			wantExternal: net.IPv4zero,
			wantInternal: net.ParseIP("192.168.1.10"),
		},
		{
			name:         "loopback external zeroed",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("127.0.0.1"), Port: 6792},
			wantExternal: net.IPv4zero,
			wantInternal: net.ParseIP("192.168.1.10"),
		},
		{
			name:         "ipv6 link-local external zeroed",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("fe80::1"), Port: 6792},
			wantExternal: net.IPv4zero,
			wantInternal: net.ParseIP("192.168.1.10"),
		},
		{
			name:         "ipv6 ula (private) external zeroed",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("fd00::1"), Port: 6792},
			wantExternal: net.IPv4zero,
			wantInternal: net.ParseIP("192.168.1.10"),
		},
		{
			name:         "global ipv6 external untouched",
			in:           Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("2001:db8::1"), Port: 6792},
			wantExternal: net.ParseIP("2001:db8::1"),
			wantInternal: net.ParseIP("192.168.1.10"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.in.SanitizedForClient()
			if !got.ExternalIP.Equal(tt.wantExternal) {
				t.Errorf("ExternalIP = %v, want %v", got.ExternalIP, tt.wantExternal)
			}
			if !got.InternalIP.Equal(tt.wantInternal) {
				t.Errorf("InternalIP = %v, want %v", got.InternalIP, tt.wantInternal)
			}
			if got.Port != tt.in.Port {
				t.Errorf("Port = %d, want %d", got.Port, tt.in.Port)
			}
		})
	}
}
