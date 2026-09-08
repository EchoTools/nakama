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
	JoinTime      int64      `json:"join_time_ns,omitempty"` // The time on the round clock that the player joined
	RatingMu      float64    `json:"rating_mu,omitempty"`
	RatingSigma   float64    `json:"rating_sigma,omitempty"`
	Username      string     `json:"username,omitempty"`
	DiscordID     string     `json:"discord_id,omitempty"`
	UserID        string     `json:"user_id,omitempty"`
	EvrID         evr.EvrId  `json:"evr_id,omitempty"`
	ClientIP      string     `json:"client_ip,omitempty"`
	SessionID     string     `json:"session_id,omitempty"`
	GeoHash       string     `json:"geohash,omitempty"`
	PingMillis    int        `json:"ping_ms,omitempty"` // The latency as measured from the ping check.
	MatchmakingAt *time.Time `json:"matchmaking_at,omitempty"`

	// Reservation provenance. Set only when IsReservation is true.
	//
	// IsReservation alone cannot tell a party slot being held from a crashed
	// player's seat being held, and it carries no expiry — so a rejected join
	// could not say whether the server had made this player a promise it was
	// now breaking. These three fields are what make that legible on the
	// rejection line, and they travel on the match label, which is the only
	// channel MatchJoinAttempt already returns to the caller. See #585.
	ReservationKind   string     `json:"reservation_kind,omitempty"` // ReservationKindSlot or ReservationKindReconnect
	ReservationExpiry *time.Time `json:"reservation_expiry,omitempty"`
	ReservationID     string     `json:"reservation_id,omitempty"` // correlates crash -> reservation -> rejoin (reconnect only)
}

const (
	// ReservationKindSlot is a party slot held for a member who has not
	// connected yet.
	ReservationKindSlot = "slot"
	// ReservationKindReconnect is a seat held for a player who crashed out of
	// this match, for the crash-recovery window.
	ReservationKindReconnect = "reconnect"
)

// ReservationTimeToExpiry reports how long this held seat has left. The second
// return is false when the player is not a reservation or carries no expiry.
func (p *PlayerInfo) ReservationTimeToExpiry(now time.Time) (time.Duration, bool) {
	if !p.IsReservation || p.ReservationExpiry == nil {
		return 0, false
	}
	return p.ReservationExpiry.Sub(now), true
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
