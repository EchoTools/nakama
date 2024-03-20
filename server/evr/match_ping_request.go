package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/samber/lo"
)

// LobbyPingRequest is a message from server to client, requesting the client ping a set of endpoints to determine
// the optimal game server to connect to.
type LobbyPingRequest struct {
	Unk0      uint16
	Unk1      uint16
	RTTMax    uint32
	Endpoints []Endpoint
}

func (m *LobbyPingRequest) Token() string {
	return "SNSLobbyPingRequestv3"
}

func (m *LobbyPingRequest) Symbol() Symbol {
	return SymbolOf(m)
}

func (m LobbyPingRequest) String() string {
	return fmt.Sprintf("%s(%s)", m.Token(), strings.Join(lo.Map(m.Endpoints, func(endpoint Endpoint, i int) string {
		return endpoint.String()
	}), ", "))
}

// Stream streams the message data in/out based on the streaming mode set.
func (m *LobbyPingRequest) Stream(s *EasyStream) error {
	RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.BigEndian, &m.Unk0) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RTTMax) },
		func() error {
			// Each endpoint is 10 bytes + 2 bytes of padding
			if s.Mode == DecodeMode {
				endpointCount := s.r.Len() / 12 // 10 byte struct + 2 byte padding
				m.Endpoints = make([]Endpoint, endpointCount)
			}
			for i := range m.Endpoints {
				if err := s.StreamStruct(&m.Endpoints[i]); err != nil {
					return err
				}
				// Stream 2 bytes of padding
				pad2 := make([]byte, 2)
				if err := s.StreamBytes(&pad2, 2); err != nil {
					return err
				}
			}
			return nil
		},
	})
	return nil
}

func NewLobbyPingRequest(rttMax int, endpoints []Endpoint) *LobbyPingRequest {

	return &LobbyPingRequest{
		Unk0:      0,
		Unk1:      4,
		RTTMax:    uint32(rttMax),
		Endpoints: endpoints,
	}
}

// Endpoint describes a game server peer.
type Endpoint struct {
	InternalIP net.IP
	ExternalIP net.IP
	Port       uint16
}

func ParseEndpointID(id string) (internalIP, externalIP net.IP, port uint16) {
	components := strings.SplitN(id, ":", 3)
	switch len(components) {
	case 2:
		return net.ParseIP(components[0]), net.ParseIP(components[1]), 0
	case 3:
		p, err := strconv.ParseUint(components[2], 10, 16)
		if err != nil {
			return nil, nil, 0
		}
		return net.ParseIP(components[0]), net.ParseIP(components[1]), uint16(p)
	default:
		return nil, nil, 0
	}
}

func FromEndpointID(id string) Endpoint {
	internalIP, externalIP, port := ParseEndpointID(id)
	return Endpoint{
		InternalIP: internalIP,
		ExternalIP: externalIP,
		Port:       port,
	}
}

// MarshalJSON marshals the endpoint to a string of "internalIP:externalIP:port"
func (e Endpoint) MarshalJSON() ([]byte, error) {
	if e.InternalIP == nil || e.ExternalIP == nil || e.Port == 0 {
		return json.Marshal("")
	}
	s := fmt.Sprintf("%s:%s:%d", e.InternalIP.String(), e.ExternalIP.String(), e.Port)
	return json.Marshal(s)
}

// UnmarshalJSON unmarshals the endpoint from a string of "internalIP:externalIP:port"
func (e *Endpoint) UnmarshalJSON(data []byte) error {

	s := ""
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if len(s) == 0 {
		return nil
	}
	components := strings.SplitN(s, ":", 3)
	if len(components) != 3 {
		return fmt.Errorf("invalid endpoint string")
	}
	port, err := strconv.ParseUint(components[2], 10, 16)
	if err != nil {
		return err
	}
	e.InternalIP = net.ParseIP(components[0])
	e.ExternalIP = net.ParseIP(components[1])
	e.Port = uint16(port)
	return nil
}

// ExternalAddress returns a string of "externalIP:port"
func (e Endpoint) ExternalAddress() string {
	return fmt.Sprintf("%s:%d", e.ExternalIP.String(), e.Port)
}

// StringIps returns a string of "internalIP:externalIP"
func (e Endpoint) ID() string {
	intIP := e.InternalIP
	if intIP == nil {
		intIP = net.IPv4zero
	}
	extIP := e.ExternalIP
	if e.ExternalIP == nil {
		extIP = net.IPv4zero
	}
	return fmt.Sprintf("%s:%s", intIP.String(), extIP.String())
}

// String returns a string of "internalIP:externalIP:port"
func (e Endpoint) String() string {
	return fmt.Sprintf("%s:%s:%d", e.InternalIP.String(), e.ExternalIP.String(), e.Port)
}

// EndpointFromString returns an Endpoint from a string of "internalIP:externalIP:port"
func EndpointFromString(s string) Endpoint {
	components := strings.SplitN(s, ":", 3)
	if len(components) != 3 {
		return Endpoint{}
	}
	port, err := strconv.ParseUint(components[2], 10, 16)
	if err != nil {
		return Endpoint{}
	}
	return Endpoint{
		InternalIP: net.ParseIP(components[0]),
		ExternalIP: net.ParseIP(components[1]),
		Port:       uint16(port),
	}
}

func (e *Endpoint) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamIpAddress(&e.InternalIP) },
		func() error { return s.StreamIpAddress(&e.ExternalIP) },
		func() error { return s.StreamNumber(binary.BigEndian, &e.Port) },
	})
}
