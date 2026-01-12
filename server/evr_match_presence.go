package server

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

// PartyIDSalt is used as a namespace for generating deterministic party IDs from group names
var PartyIDSalt uuid.UUID

func init() {
	PartyIDSalt = uuid.Must(uuid.NewV4())
}

var _ runtime.Presence = &EvrMatchPresence{}

// Represents identity information for a single match participant.
type EvrMatchPresence struct {
	Node              string       `json:"node,omitempty"`
	SessionID         uuid.UUID    `json:"session_id,omitempty"`       // The Player's "match" connection session ID
	LoginSessionID    uuid.UUID    `json:"login_session_id,omitempty"` // The Player's "login" connection session ID
	UserID            uuid.UUID    `json:"user_id,omitempty"`
	EvrID             evr.EvrId    `json:"evr_id,omitempty"`
	DiscordID         string       `json:"discord_id,omitempty"`
	ClientIP          string       `json:"client_ip,omitempty"`
	ClientPort        string       `json:"client_port,omitempty"`
	GeoHash           string       `json:"geohash,omitempty"`
	Username          string       `json:"username,omitempty"`
	DisplayName       string       `json:"display_name,omitempty"`
	PartyID           uuid.UUID    `json:"party_id,omitempty"`
	RoleAlignment     int          `json:"role,omitempty"` // The team they want to be on
	SupportedFeatures []string     `json:"supported_features,omitempty"`
	SessionExpiry     int64        `json:"session_expiry,omitempty"`
	IsPCVR            bool         `json:"is_pcvr,omitempty"` // PCVR or Standalone
	DisableEncryption bool         `json:"disable_encryption,omitempty"`
	DisableMAC        bool         `json:"disable_mac,omitempty"`
	Query             string       `json:"query,omitempty"` // Their matchmaking query
	Rating            types.Rating `json:"rating,omitempty"`
	PingMillis        int          `json:"ping_ms,omitempty"`
	MatchmakingAt     *time.Time   `json:"matchmaking_at,omitempty"` // Whether the player is matchmaking
	EntrantID         uuid.UUID    `json:"entrant_id,omitempty"`     // Single-use entrant ID for this connection attempt
}

func (p EvrMatchPresence) GetEntrantID() uuid.UUID {
	return p.EntrantID
}

func (p EvrMatchPresence) GetUserId() string {
	return p.UserID.String()
}
func (p EvrMatchPresence) GetSessionId() string {
	return p.SessionID.String()
}
func (p EvrMatchPresence) GetNodeId() string {
	return p.Node
}
func (p EvrMatchPresence) GetHidden() bool {
	return false
}
func (p EvrMatchPresence) GetPersistence() bool {
	return false
}
func (p EvrMatchPresence) GetUsername() string {
	return p.Username
}
func (p EvrMatchPresence) GetStatus() string {
	data, _ := json.Marshal(p)
	return string(data)
}
func (p *EvrMatchPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReasonUnknown
}
func (p EvrMatchPresence) GetEvrID() string {
	return p.EvrID.Token()
}

func (p EvrMatchPresence) IsPlayer() bool {
	return p.RoleAlignment != evr.TeamModerator && p.RoleAlignment != evr.TeamSpectator
}

func (p EvrMatchPresence) IsModerator() bool {
	return p.RoleAlignment == evr.TeamModerator
}

func (p EvrMatchPresence) IsSpectator() bool {
	return p.RoleAlignment == evr.TeamSpectator
}

func (p EvrMatchPresence) IsSocial() bool {
	return p.RoleAlignment == evr.TeamSocial
}

func (p EvrMatchPresence) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
}

// NewEntrantID generates a new random single-use entrant ID for each connection attempt
func NewEntrantID() uuid.UUID {
	return uuid.Must(uuid.NewV4())
}

func EntrantPresenceFromSession(session Session, partyID uuid.UUID, roleAlignment int, rating types.Rating, groupID string, ping int, query string) (*EvrMatchPresence, error) {

	params, ok := LoadParams(session.Context())
	if !ok {
		return nil, errors.New("failed to get session parameters")
	}

	if params.loginSession == nil {
		return nil, errors.New("failed to get login session parameters")
	}

	return &EvrMatchPresence{
		Node:              params.node,
		UserID:            session.UserID(),
		SessionID:         session.ID(),
		LoginSessionID:    params.loginSession.ID(),
		Username:          session.Username(),
		DisplayName:       params.profile.GetGroupIGN(groupID),
		EvrID:             params.xpID,
		PartyID:           partyID,
		RoleAlignment:     roleAlignment,
		DiscordID:         params.DiscordID(),
		ClientIP:          session.ClientIP(),
		ClientPort:        session.ClientPort(),
		GeoHash:           params.GeoHash(),
		IsPCVR:            params.IsPCVR(),
		Rating:            rating,
		SupportedFeatures: params.supportedFeatures,

		DisableEncryption: params.disableEncryption,
		DisableMAC:        params.disableMAC,
		PingMillis:        ping,
		Query:             query,
	}, nil
}
