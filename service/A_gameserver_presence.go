package service

import (
	"encoding/json"
	"net"
	"slices"
	"strings"

	anyascii "github.com/anyascii/go"
	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/mmcloughlin/geohash"
)

var _ = runtime.Presence(&GameServerPresence{})

type GameServerPresence struct {
	Node            string       `json:"node,omitempty"`             // The node the server is on.
	Username        string       `json:"username,omitempty"`         // The server's username.
	SessionID       uuid.UUID    `json:"sid,omitempty"`              // The server's Session ID
	OperatorID      uuid.UUID    `json:"oper,omitempty"`             // The user id of the server.
	GroupIDs        []uuid.UUID  `json:"group_ids,omitempty"`        // The channels this server will host matches for.
	Endpoint        evr.Endpoint `json:"endpoint,omitempty"`         // The endpoint data used for connections.
	VersionLock     evr.Symbol   `json:"version_lock,omitempty"`     // The game build version. (EVR)
	AppID           string       `json:"app_id,omitempty"`           // The game app id. (EVR)
	RegionCodes     []string     `json:"region_codes,omitempty"`     // The region the match is hosted in. (Matching Only) (EVR)
	ServerID        uint64       `json:"server_id,omitempty"`        // The server id of the server. (EVR)
	Features        []string     `json:"features,omitempty"`         // The features of the server.
	Tags            []string     `json:"tags,omitempty"`             // The tags given on the urlparam for the match.
	DesignatedModes []evr.Symbol `json:"designated_modes,omitempty"` // The priority modes for the server.
	TimeStepUsecs   uint32       `json:"time_step_usecs,omitempty"`  // The time step in microseconds.
	NativeSupport   bool         `json:"native,omitempty"`           // The native support of the server.
	NativeVersion   string       `json:"native_version,omitempty"`   // The native version of the server.
	City            string       `json:"city,omitempty"`             // The city of the server.
	Region          string       `json:"region,omitempty"`           // The region of the server.
	CountryCode     string       `json:"country_code,omitempty"`     // The country of the server.
	GeoHash         string       `json:"geohash,omitempty"`          // The geohash of the server.
	Latitude        float64      `json:"latitude,omitempty"`         // The latitude of the server.
	Longitude       float64      `json:"longitude,omitempty"`        // The longitude of the server.
	ASNumber        int          `json:"asn,omitempty"`              // The ASN of the server.
}

func (g GameServerPresence) GetHidden() bool {
	return false
}

func (g GameServerPresence) GetPersistence() bool {
	return false
}

func (g GameServerPresence) GetUsername() string {
	return g.Username
}

func (g GameServerPresence) GetStatus() string {
	s, _ := json.Marshal(g)
	return string(s)
}

func (g GameServerPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReasonUnknown
}

func (g GameServerPresence) GetUserId() string {
	return g.OperatorID.String()
}

func (g GameServerPresence) GetSessionId() string {
	return g.SessionID.String()
}

func (g GameServerPresence) GetNodeId() string {
	return g.Node
}

func (g GameServerPresence) Location(includeCity bool) string {
	if g.CountryCode == "" {
		return ""
	}

	location := g.CountryCode
	if g.Region != "" {
		location = g.Region + ", " + location
	}

	if includeCity && g.City != "" {
		location = g.City + ", " + location
	}

	return location
}

func (g *GameServerPresence) IsPriorityFor(mode evr.Symbol) bool {
	return slices.Contains(g.DesignatedModes, mode)
}

func NewGameServerPresence(userId, sessionId uuid.UUID, serverId uint64, internalIP, externalIP net.IP, port uint16, groupIDs []uuid.UUID, regionCodes []string, versionLock evr.Symbol, tags, features []string, timeStepUsecs uint32, ipInfo IPInfo, geoPrecision int, isNative bool, nativeVersion string) *GameServerPresence {

	var asn int
	var lat, lon float64
	var geoHash, city, region, countryCode string

	if geoPrecision > 0 && ipInfo != nil {

		lat = ipInfo.Latitude()
		lon = ipInfo.Longitude()
		asn = ipInfo.ASN()

		geoHash = geohash.EncodeWithPrecision(lat, lon, uint(geoPrecision))
		city = ipInfo.City()
		region = ipInfo.Region()
		countryCode = ipInfo.CountryCode()

	}

	slices.Sort(regionCodes)
	regionCodes = slices.Compact(regionCodes)

	config := &GameServerPresence{
		SessionID:  sessionId,
		OperatorID: userId,
		ServerID:   serverId,
		Endpoint: evr.Endpoint{
			InternalIP: internalIP,
			ExternalIP: externalIP,
			Port:       port,
		},
		RegionCodes:   regionCodes,
		VersionLock:   versionLock,
		GroupIDs:      groupIDs,
		Features:      features,
		GeoHash:       geoHash,
		Latitude:      lat,
		Longitude:     lon,
		City:          city,
		Region:        region,
		CountryCode:   countryCode,
		ASNumber:      asn,
		TimeStepUsecs: timeStepUsecs,
		NativeSupport: isNative,
		NativeVersion: nativeVersion,

		Tags: make([]string, 0),
	}

	return config
}

func (p *GameServerPresence) LocationRegionCode(withCity, withRegion bool) string {
	if withCity {
		return LocationToRegionCode(p.CountryCode, p.Region, p.City)
	}
	if withRegion {
		return LocationToRegionCode(p.CountryCode, p.Region, "")
	}
	return LocationToRegionCode(p.CountryCode, "", "")
}

func (p *GameServerPresence) LocationRegionCodes() []string {
	codes := []string{
		p.LocationRegionCode(false, false),
		p.LocationRegionCode(true, false),
		p.LocationRegionCode(true, true),
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

func LocationToRegionCode(countryCode, region, city string) string {
	code := countryCode

	if region != "" {
		code = countryCode + "-" + region
	}

	if city != "" {
		code = code + "-" + city
	}

	if code == "" {
		return ""
	}

	// Convert to ASCII.
	code = anyascii.Transliterate(code)

	// remove spaces and convert to lowercase.
	code = strings.ToLower(code)
	code = strings.ReplaceAll(code, " ", "")

	return code
}
