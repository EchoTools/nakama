package server

import "time"

type SessionScoreboard struct {
	GameTime      time.Duration `json:"game_time_ns"`                // The time on the game clock in milliseconds
	RoundDuration time.Duration `json:"round_duration_ns"`           // The duration of the round in milliseconds
	UpdatedAt     time.Time     `json:"updated_at"`                  // The time at which the scoreboard was last updated
	PausedAt      *time.Time    `json:"paused_at,omitempty"`         // The time at which the game was paused
	PauseDuration time.Duration `json:"pause_duration_ns,omitempty"` // The duration of the pause in milliseconds
}

func NewSessionScoreboard(duration time.Duration, startAt time.Time) *SessionScoreboard {
	if duration <= 0 {
		return nil
	}
	c := &SessionScoreboard{
		RoundDuration: duration,
		UpdatedAt:     time.Now(),
	}
	// If startAt is zero, the game hasn't started yet
	if !startAt.IsZero() {
		c.GameTime = time.Since(startAt)
	}
	return c
}

func (r *SessionScoreboard) LatestAsNewScoreboard() *SessionScoreboard {
	if r == nil {
		return nil
	}
	return &SessionScoreboard{
		RoundDuration: r.RoundDuration,
		GameTime:      r.Elapsed(),
		UpdatedAt:     time.Now(),
		PauseDuration: r.PauseDuration,
	}
}

func (r *SessionScoreboard) Elapsed() time.Duration {
	// If the game is over return the round duration
	if r.IsOver() {
		return r.RoundDuration
	}
	// If the game is paused, return the game time at the moment of pausing
	if r.IsPaused() {
		return r.GameTime
	}

	// Otherwise return the game time plus the time since the last update
	return r.GameTime + time.Since(r.UpdatedAt)
}

func (r *SessionScoreboard) RemainingTime() time.Duration {
	if r == nil {
		return 0
	}
	return max(0, r.RoundDuration-r.Elapsed())
}

func (r *SessionScoreboard) IsPaused() bool {
	if r == nil {
		return false
	}
	return r.PausedAt != nil && !r.PausedAt.IsZero() && time.Since(*r.PausedAt) < r.PauseDuration
}

func (r *SessionScoreboard) IsOver() bool {
	if r == nil {
		return false
	}
	// If there is no round, the match isn't being tracked.
	if r.RoundDuration == 0 {
		return true
	}
	elapsed := r.GameTime + time.Since(r.UpdatedAt)
	if r.IsPaused() {
		elapsed = r.GameTime
	}

	// Otherwise return the game time plus the time since the last update

	return elapsed >= r.RoundDuration
}

func (r *SessionScoreboard) Update(gameTime time.Duration) {
	r.UpdatedAt = time.Now()

	// If the elapsed time has increased, update it
	if r.Elapsed() < gameTime {
		r.GameTime = gameTime
		// Clear any pause state
		r.PauseDuration = 0
		r.PausedAt = nil
		return
	}
	// Otherwise, just update the updated at time
	r.GameTime = gameTime
}

func (r *SessionScoreboard) UpdateWithPause(elapsed time.Duration, pauseDuration time.Duration) {
	r.GameTime = elapsed
	r.UpdatedAt = time.Now()
	r.PauseDuration = pauseDuration
	now := time.Now()
	r.PausedAt = &now
}

func (r *SessionScoreboard) Unpause(elapsed time.Duration) {
	r.GameTime = elapsed
	r.UpdatedAt = time.Now()
	r.PauseDuration = 0
	r.PausedAt = nil
}
