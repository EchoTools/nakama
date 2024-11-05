package server

import (
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type PlayerInfo struct {
	DisplayName   string    `json:"display_name,omitempty"`
	PartyID       string    `json:"party_id,omitempty"`
	Team          TeamIndex `json:"team"`
	IsReservation bool      `json:"is_reservation,omitempty"`
	JoinTime      int64     `json:"join_time_ms"` // The time on the round clock that the player joined
	RatingMu      float64   `json:"rating_mu,omitempty"`
	RatingSigma   float64   `json:"rating_sigma,omitempty"`
	Username      string    `json:"username,omitempty"`
	DiscordID     string    `json:"discord_id,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	EvrID         evr.EvrId `json:"evr_id,omitempty"`
	ClientIP      string    `json:"client_ip,omitempty"`
}

func (p *PlayerInfo) IsBackfill() bool {
	return p.JoinTime != 0.0
}

func (p *PlayerInfo) UUID() uuid.UUID {
	return uuid.FromStringOrNil(p.UserID)
}
