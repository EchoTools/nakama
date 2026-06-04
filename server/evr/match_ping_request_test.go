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

// TestEndpoint_IsValid covers issue #465: the internal slot is optional. An
// endpoint with only an external IP + port is valid; missing external or port
// is not.
func TestEndpoint_IsValid(t *testing.T) {
	tests := []struct {
		name string
		in   Endpoint
		want bool
	}{
		{
			name: "both set is valid",
			in:   Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("203.0.113.7"), Port: 6792},
			want: true,
		},
		{
			name: "external-only (internal nil) is valid",
			in:   Endpoint{ExternalIP: net.ParseIP("203.0.113.7"), Port: 6792},
			want: true,
		},
		{
			name: "missing external is invalid",
			in:   Endpoint{InternalIP: net.ParseIP("192.168.1.10"), Port: 6792},
			want: false,
		},
		{
			name: "missing port is invalid",
			in:   Endpoint{ExternalIP: net.ParseIP("203.0.113.7")},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.IsValid(); got != tc.want {
				t.Errorf("IsValid() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEndpoint_MarshalJSON_NilInternal covers issue #465: when the internal
// slot is empty it serializes as 0.0.0.0 (the client's "skip this address"
// value) rather than dropping the endpoint, and the both-set case is unchanged.
func TestEndpoint_MarshalJSON_NilInternal(t *testing.T) {
	tests := []struct {
		name string
		in   Endpoint
		want string
	}{
		{
			name: "nil internal serializes as 0.0.0.0",
			in:   Endpoint{ExternalIP: net.ParseIP("203.0.113.7"), Port: 6792},
			want: "0.0.0.0:203.0.113.7:6792",
		},
		{
			name: "both set unchanged",
			in:   Endpoint{InternalIP: net.ParseIP("192.168.1.10"), ExternalIP: net.ParseIP("203.0.113.7"), Port: 6792},
			want: "192.168.1.10:203.0.113.7:6792",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.in.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			var got string
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got != tc.want {
				t.Errorf("MarshalJSON() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEndpoint_Stream_NilInternal covers issue #465: the wire encoder writes
// 0.0.0.0 for a nil internal IP, and the encoded form round-trips to a
// wire-valid endpoint (internal decodes back to 0.0.0.0, external + port
// preserved).
func TestEndpoint_Stream_NilInternal(t *testing.T) {
	in := Endpoint{ExternalIP: net.ParseIP("203.0.113.7"), Port: 6792}

	enc := NewEasyStream(EncodeMode, nil)
	if err := in.Stream(enc); err != nil {
		t.Fatalf("encode Stream: %v", err)
	}
	encoded := enc.Bytes()

	// 4 bytes internal + 4 bytes external + 2 bytes port = 10 bytes.
	if len(encoded) != 10 {
		t.Fatalf("encoded length = %d, want 10", len(encoded))
	}
	// Internal slot (first 4 bytes) must be 0.0.0.0.
	for i := 0; i < net.IPv4len; i++ {
		if encoded[i] != 0 {
			t.Fatalf("internal slot byte %d = %d, want 0 (0.0.0.0)", i, encoded[i])
		}
	}

	var out Endpoint
	dec := NewEasyStream(DecodeMode, encoded)
	if err := out.Stream(dec); err != nil {
		t.Fatalf("decode Stream: %v", err)
	}
	if !out.InternalIP.Equal(net.IPv4zero) {
		t.Errorf("decoded InternalIP = %v, want 0.0.0.0", out.InternalIP)
	}
	if !out.ExternalIP.Equal(in.ExternalIP) {
		t.Errorf("decoded ExternalIP = %v, want %v", out.ExternalIP, in.ExternalIP)
	}
	if out.Port != in.Port {
		t.Errorf("decoded Port = %d, want %d", out.Port, in.Port)
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
