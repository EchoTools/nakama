package service

import (
	"encoding/json"
	"errors"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/intinig/go-openskill/types"
)

var EntrantIDSalt uuid.UUID
var EntrantIDSaltStr string

func init() {
	EntrantIDSalt = uuid.Must(uuid.NewV4())
	EntrantIDSaltStr = EntrantIDSalt.String()
}

// Represents identity information for a single match participant.
type MatchPresence struct {
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
	RankPercentile    float64      `json:"rank_percentile,omitempty"`
	Rating            types.Rating `json:"rating,omitempty"`
	PingMillis        int          `json:"ping_ms,omitempty"`
	MatchmakingAt     *time.Time   `json:"matchmaking_at,omitempty"` // Whether the player is matchmaking
}

func (p MatchPresence) EntrantID(matchID MatchID) uuid.UUID {
	return NewEntrantID(matchID, p.EvrID)
}

func (p MatchPresence) GetUserId() string {
	return p.UserID.String()
}
func (p MatchPresence) GetSessionId() string {
	return p.SessionID.String()
}
func (p MatchPresence) GetNodeId() string {
	return p.Node
}
func (p MatchPresence) GetHidden() bool {
	return false
}
func (p MatchPresence) GetPersistence() bool {
	return false
}
func (p MatchPresence) GetUsername() string {
	return p.Username
}
func (p MatchPresence) GetStatus() string {
	data, _ := json.Marshal(p)
	return string(data)
}
func (p *MatchPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReasonUnknown
}
func (p MatchPresence) GetEvrID() string {
	return p.EvrID.Token()
}

func (p MatchPresence) IsPlayer() bool {
	return p.RoleAlignment != evr.TeamModerator && p.RoleAlignment != evr.TeamSpectator
}

func (p MatchPresence) IsModerator() bool {
	return p.RoleAlignment == evr.TeamModerator
}

func (p MatchPresence) IsSpectator() bool {
	return p.RoleAlignment == evr.TeamSpectator
}

func (p MatchPresence) IsSocial() bool {
	return p.RoleAlignment == evr.TeamSocial
}

func (p MatchPresence) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
}

func NewEntrantID(matchID MatchID, evrID evr.EvrId) uuid.UUID {
	return uuid.NewV5(matchID.UUID, EntrantIDSaltStr+evrID.String())
}

func EntrantPresenceFromSession(session server.Session, partyID uuid.UUID, roleAlignment int, rating types.Rating, rankPercentile float64, groupID string, ping int, query string) (*MatchPresence, error) {

	params, ok := LoadParams(session.Context())
	if !ok {
		return nil, errors.New("failed to get session parameters")
	}

	return &MatchPresence{
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
		RankPercentile:    rankPercentile,

		DisableEncryption: params.disableEncryption,
		DisableMAC:        params.disableMAC,
		PingMillis:        ping,
		Query:             query,
	}, nil
}
