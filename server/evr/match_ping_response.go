package evr

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// LobbyPingResponse is a message from client to server, providing the results of a ping request.
// It tells the server which game servers are optimal for the client.
type LobbyPingResponse struct {
	Results []EndpointPingResult // Results are the endpoints which the client should be asked to ping.
}

func (m *LobbyPingResponse) Token() string {
	return SymbolOf(m).Token().String()
}

func (m *LobbyPingResponse) Symbol() Symbol {
	return SymbolOf(m)
}

func (r LobbyPingResponse) String() string {
	resultStrings := make([]string, len(r.Results))
	for i, result := range r.Results {
		resultStrings[i] = result.String()
	}
	return fmt.Sprintf("%s(results=[%s])", r.Token(), strings.Join(resultStrings, ", "))
}

// NewLobbyPingResponse initializes a new LobbyPingResponse message.
func NewLobbyPingResponse() *LobbyPingResponse {
	return &LobbyPingResponse{
		Results: []EndpointPingResult{},
	}
}

const MaxPingResults = 256

func (m *LobbyPingResponse) Stream(s *EasyStream) error {
	rLength := uint64(len(m.Results))
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &rLength) },
		func() error {
			if s.Mode == DecodeMode {
				if rLength > MaxPingResults {
					return fmt.Errorf("ping result count %d exceeds maximum %d", rLength, MaxPingResults)
				}
			}
			m.Results = make([]EndpointPingResult, rLength)
			for i := 0; i < len(m.Results); i++ {
				err := s.StreamStruct(&m.Results[i])
				if err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// EndpointPingResult is the result of a LobbyPingRequestv3 to an individual game server.
type EndpointPingResult struct {
	InternalIP       net.IP // InternalAddress is the internal/private address of the endpoint.
	ExternalIP       net.IP // ExternalAddress is the external/public address of the endpoint.
	PingMilliseconds uint32 // PingMilliseconds is the ping time the client took to reach the game server, in milliseconds.
}

func (m EndpointPingResult) EndpointID() string {
	return fmt.Sprintf("%s:%s", m.GetInternalIP(), m.GetExternalIP())
}

func (m EndpointPingResult) GetInternalIP() string {
	return m.InternalIP.String()
}

func (m EndpointPingResult) GetExternalIP() string {
	return m.ExternalIP.String()
}
func (m EndpointPingResult) RTT() time.Duration {
	return time.Duration(m.PingMilliseconds) * time.Millisecond
}

// Stream streams the EndpointPingResult data in/out based on the streaming mode set.
func (r *EndpointPingResult) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamIpAddress(&r.InternalIP) },
		func() error { return s.StreamIpAddress(&r.ExternalIP) },
		func() error { return s.StreamNumber(binary.LittleEndian, &r.PingMilliseconds) },
	})
}

func (e EndpointPingResult) String() string {
	return fmt.Sprintf("%s %dms", e.ExternalIP.String(), e.PingMilliseconds)
}
