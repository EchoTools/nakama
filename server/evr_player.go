package server

import (
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
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
	JoinTime    int64        `json:"join_time_ms"` // The time on the round clock that the player joined
	Rating      types.Rating `json:"rating,omitempty"`
	IPinfo      *ipinfo.Core `json:"ip_info,omitempty"`
}
