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

// TODO: Break out the pcvr, encryption, mac, and supported features into a separate struct
// Represents identity information for a single match participant.
type LobbyPresence struct {
	Node              string       `json:"node,omitempty"`
	SessionID         uuid.UUID    `json:"session_id,omitempty"`       // The Player's "match" connection session ID
	LoginSessionID    uuid.UUID    `json:"login_session_id,omitempty"` // The Player's "login" connection session ID
	UserID            uuid.UUID    `json:"user_id,omitempty"`
	XPID              evr.XPID     `json:"evr_id,omitempty"`
	DiscordID         string       `json:"discord_id,omitempty"`
	ClientIP          string       `json:"client_ip,omitempty"`
	ClientPort        string       `json:"client_port,omitempty"`
	GeoHash           string       `json:"geohash,omitempty"`
	Username          string       `json:"username,omitempty"`
	DisplayName       string       `json:"display_name,omitempty"`
	PartyID           uuid.UUID    `json:"party_id,omitempty"`
	SupportedFeatures []string     `json:"supported_features,omitempty"`
	SessionExpiry     int64        `json:"session_expiry,omitempty"`
	IsPCVR            bool         `json:"is_pcvr,omitempty"` // PCVR or Standalone
	DisableEncryption bool         `json:"disable_encryption,omitempty"`
	DisableMAC        bool         `json:"disable_mac,omitempty"`
	Query             string       `json:"query,omitempty"` // Their matchmaking query
	Rating            types.Rating `json:"rating,omitempty"`
	RoleAlignment     RoleIndex    `json:"role,omitempty"` // The team they want to be on
	JoinTime          time.Time    `json:"join_time,omitempty"`
	MatchmakingAt     *time.Time   `json:"matchmaking_at,omitempty"` // Whether the player is matchmaking
	PingMillis        int          `json:"ping_ms,omitempty"`
}

func (p LobbyPresence) EntrantID(matchID MatchID) uuid.UUID {
	return NewEntrantID(matchID, p.XPID)
}

func (p LobbyPresence) GetUserId() string {
	return p.UserID.String()
}
func (p LobbyPresence) GetSessionId() string {
	return p.SessionID.String()
}
func (p LobbyPresence) GetNodeId() string {
	return p.Node
}
func (p LobbyPresence) GetHidden() bool {
	return false
}
func (p LobbyPresence) GetPersistence() bool {
	return false
}
func (p LobbyPresence) GetUsername() string {
	return p.Username
}
func (p LobbyPresence) GetStatus() string {
	data, _ := json.Marshal(p)
	return string(data)
}
func (p *LobbyPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReasonUnknown
}
func (p LobbyPresence) GetEvrID() string {
	return p.XPID.String()
}

func (p LobbyPresence) IsPlayer() bool {
	return p.RoleAlignment != Moderator && p.RoleAlignment != Spectator
}

func (p LobbyPresence) IsModerator() bool {
	return p.RoleAlignment == Moderator
}

func (p LobbyPresence) IsSpectator() bool {
	return p.RoleAlignment == Spectator
}

func (p LobbyPresence) IsSocial() bool {
	return p.RoleAlignment == SocialLobbyParticipant
}

func (p LobbyPresence) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
}

func NewEntrantID(matchID MatchID, evrID evr.XPID) uuid.UUID {
	return uuid.NewV5(matchID.UUID, EntrantIDSaltStr+evrID.String())
}

func EntrantPresenceFromSession(session server.Session, partyID uuid.UUID, roleAlignment RoleIndex, rating types.Rating, rankPercentile float64, groupID string, ping int, query string) (*LobbyPresence, error) {

	params, ok := LoadParams(session.Context())
	if !ok {
		return nil, errors.New("failed to get session parameters")
	}

	return &LobbyPresence{
		Node:              params.node,
		UserID:            session.UserID(),
		SessionID:         session.ID(),
		LoginSessionID:    params.loginSession.ID(),
		Username:          session.Username(),
		DisplayName:       params.profile.GetGroupIGN(groupID),
		XPID:              params.xpID,
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
