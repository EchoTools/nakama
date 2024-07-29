package server

import (
	"encoding/json"
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var _ runtime.Presence = &EvrMatchPresence{}

// Represents identity information for a single match participant.
type EvrMatchPresence struct {
	Node           string    `json:"node,omitempty"`
	SessionID      uuid.UUID `json:"session_id,omitempty"`       // The Player's "match" connection session ID
	LoginSessionID uuid.UUID `json:"login_session_id,omitempty"` // The Player's "login" connection session ID
	EntrantID      uuid.UUID `json:"entrant_id,omitempty"`       // The Player's game server session ID
	UserID         uuid.UUID `json:"user_id,omitempty"`
	EvrID          evr.EvrId `json:"evr_id,omitempty"`
	DiscordID      string    `json:"discord_id,omitempty"`
	ClientIP       string    `json:"client_ip,omitempty"`
	ClientPort     string    `json:"client_port,omitempty"`
	Username       string    `json:"username,omitempty"`
	DisplayName    string    `json:"display_name,omitempty"`
	PartyID        uuid.UUID `json:"party_id,omitempty"`
	RoleAlignment  int       `json:"role,omitempty"`  // The team they want to be on
	Query          string    `json:"query,omitempty"` // Their matchmaking query
	SessionExpiry  int64     `json:"session_expiry,omitempty"`
	HeadsetType    string    `json:"headset_type,omitempty"` // PCVR or Standalone
}

func (p EvrMatchPresence) String() string {
	data, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(data)
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
func (p EvrMatchPresence) GetEvrId() string {
	return p.EvrID.Token()
}

func (p EvrMatchPresence) GetPlayerSession() string {
	return p.EntrantID.String()
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

type JoinMetadata struct {
	Presence EvrMatchPresence
}

func NewJoinMetadata(p EvrMatchPresence) *JoinMetadata {
	return &JoinMetadata{Presence: p}
}

func (m JoinMetadata) MarshalMap() map[string]string {
	data, err := json.Marshal(m.Presence)
	if err != nil {
		return nil
	}
	return map[string]string{"presence": string(data)}
}

func (m *JoinMetadata) UnmarshalMap(md map[string]string) error {
	data, ok := md["presence"]
	if !ok {
		return errors.New("no presence")
	}
	if err := json.Unmarshal([]byte(data), &m.Presence); err != nil {
		return err
	}
	return nil
}
