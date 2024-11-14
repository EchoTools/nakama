package server

import (
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/thriftrw/ptr"
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

// The player joined after the round clock started
func (p *PlayerInfo) IsBackfill() bool {
	return p.JoinTime >= 0.0
}

// The player is on blue or orange team
func (p *PlayerInfo) IsCompetitor() bool {
	return p.Team != BlueTeam || p.Team != OrangeTeam
}

func (p *PlayerInfo) UUID() uuid.UUID {
	return uuid.FromStringOrNil(p.UserID)
}

func (p *PlayerInfo) Rating() types.Rating {
	if p.RatingMu == 0.0 && p.RatingSigma == 0.0 {
		return NewDefaultRating()
	}

	return rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    ptr.Float64(p.RatingMu),
		Sigma: ptr.Float64(p.RatingSigma),
	})
}

func (p *PlayerInfo) SetRating(r types.Rating) {
	p.RatingMu = r.Mu
	p.RatingSigma = r.Sigma
}
