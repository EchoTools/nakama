package server

import (
	"encoding/json"
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

var EntrantIDSalt uuid.UUID
var EntrantIDSaltStr string

func init() {
	EntrantIDSalt = uuid.Must(uuid.NewV4())
	EntrantIDSaltStr = EntrantIDSalt.String()
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
	RankPercentile    float64      `json:"rank_percentile,omitempty"`
	Rating            types.Rating `json:"rating,omitempty"`
	PingMillis        int          `json:"ping_ms,omitempty"`
}

func (p EvrMatchPresence) EntrantID(matchID MatchID) uuid.UUID {
	return NewEntrantID(matchID, p.EvrID)
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

func NewEntrantID(matchID MatchID, evrID evr.EvrId) uuid.UUID {
	return uuid.NewV5(matchID.UUID, EntrantIDSaltStr+evrID.String())
}

func EntrantPresenceFromLobbyParams(session Session, lobbyParams *LobbySessionParameters) (*EvrMatchPresence, error) {

	sessionCtx := session.Context()
	params, ok := LoadParams(sessionCtx)
	if !ok {
		return nil, errors.New("failed to get session parameters")
	}

	entrant := &EvrMatchPresence{
		Node:              lobbyParams.Node,
		UserID:            session.UserID(),
		SessionID:         session.ID(),
		LoginSessionID:    params.loginSession.ID(),
		Username:          session.Username(),
		DisplayName:       params.accountMetadata.GetGroupDisplayNameOrDefault(lobbyParams.GroupID.String()),
		EvrID:             params.xpID,
		PartyID:           lobbyParams.PartyID,
		RoleAlignment:     lobbyParams.Role,
		DiscordID:         params.DiscordID(),
		ClientIP:          session.ClientIP(),
		ClientPort:        session.ClientPort(),
		IsPCVR:            params.IsPCVR(),
		Rating:            lobbyParams.GetRating(),
		SupportedFeatures: params.supportedFeatures,
		RankPercentile:    lobbyParams.GetRankPercentile(),

		DisableEncryption: params.disableEncryption,
		DisableMAC:        params.disableMAC,
	}

	return entrant, nil
}
