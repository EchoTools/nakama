package server

import (
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/types"
)

type PlayerInfo struct {
	DisplayName   string     `json:"display_name,omitempty"`
	PartyID       string     `json:"party_id,omitempty"`
	IsReservation bool       `json:"is_reservation,omitempty"`
	Team          TeamIndex  `json:"team"`
	JoinTime      int64      `json:"join_time_ms,omitempty"` // The time on the round clock that the player joined
	RatingMu      float64    `json:"rating_mu,omitempty"`
	RatingSigma   float64    `json:"rating_sigma,omitempty"`
	RatingScore   int        `json:"rating_score,omitempty"`
	Username      string     `json:"username,omitempty"`
	DiscordID     string     `json:"discord_id,omitempty"`
	UserID        string     `json:"user_id,omitempty"`
	EvrID         evr.EvrId  `json:"evr_id,omitempty"`
	ClientIP      string     `json:"client_ip,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	GeoHash       string     `json:"geohash,omitempty"`
	PingMillis    int        `json:"ping_ms,omitempty"` // The latency as measured from the ping check.
	MatchmakingAt *time.Time `json:"matchmaking_at,omitempty"`
}

// The player joined after the round clock started
func (p *PlayerInfo) IsBackfill() bool {
	return p.JoinTime > 0.0
}

// The player is on blue or orange team
func (p *PlayerInfo) IsCompetitor() bool {
	return p.Team == BlueTeam || p.Team == OrangeTeam
}

func (p *PlayerInfo) UUID() uuid.UUID {
	return uuid.FromStringOrNil(p.UserID)
}

func (p *PlayerInfo) Rating() types.Rating {
	return NewRating(0, p.RatingMu, p.RatingSigma)
}
