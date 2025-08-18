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
