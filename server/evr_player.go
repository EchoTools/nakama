package server

import (
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/ipinfo/go/v2/ipinfo"
)

type PlayerInfo struct {
	UserID      string       `json:"user_id,omitempty"`
	Username    string       `json:"username,omitempty"`
	DisplayName string       `json:"display_name,omitempty"`
	EvrID       evr.EvrId    `json:"evr_id,omitempty"`
	Team        TeamIndex    `json:"team"`
	ClientIP    string       `json:"client_ip,omitempty"`
	DiscordID   string       `json:"discord_id,omitempty"`
	PartyID     string       `json:"party_id,omitempty"`
	IPinfo      *ipinfo.Core `json:"ip_info,omitempty"`
}
