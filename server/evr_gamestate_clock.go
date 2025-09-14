package server

import "time"

type RoundClock struct {
	GameTime      time.Duration `json:"game_time"`      // The time on the game clock
	RoundDuration time.Duration `json:"round_duration"` // The duration of the round
	UpdatedAt     time.Time     `json:"updated_at"`     // The time at which the round clock was last updated
	PausedAt      time.Time     `json:"paused_at"`      // The time at which the game was paused
	PauseDuration time.Duration `json:"pause_duration"` // The duration of the pause
}

func NewRoundClock(duration time.Duration, startAt time.Time) *RoundClock {
	if duration <= 0 {
		return nil
	}
	c := &RoundClock{
		RoundDuration: duration,
		UpdatedAt:     time.Now(),
	}
	// If startAt is zero, the game hasn't started yet
	if !startAt.IsZero() {
		c.GameTime = time.Since(startAt)
	}
	return c
}

func (r *RoundClock) LatestAsNewClock() *RoundClock {
	if r == nil {
		return nil
	}
	return &RoundClock{
		RoundDuration: r.RoundDuration,
		GameTime:      r.Elapsed(),
		UpdatedAt:     time.Now(),
		PauseDuration: r.PauseDuration,
	}
}

func (r *RoundClock) Elapsed() time.Duration {
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

func (r *RoundClock) RemainingTime() time.Duration {
	if r == nil {
		return 0
	}
	return max(0, r.RoundDuration-r.Elapsed())
}

func (r *RoundClock) IsPaused() bool {
	if r == nil {
		return false
	}
	return !r.PausedAt.IsZero() && time.Since(r.PausedAt) < r.PauseDuration
}

func (r *RoundClock) IsOver() bool {
	if r == nil {
		return false
	}
	// If there is no round, the match isn't being tracked.
	if r.RoundDuration == 0 {
		return true
	}
	return r.Elapsed() >= r.RoundDuration
}

func (r *RoundClock) Update(gameTime time.Duration) {
	r.UpdatedAt = time.Now()

	// If the elapsed time has increased, update it
	if r.Elapsed() < gameTime {
		r.GameTime = gameTime
		// Clear any pause state
		r.PauseDuration = 0
		r.PausedAt = time.Time{}
		return
	}
	// Otherwise, just update the updated at time
	r.UpdatedAt = time.Now()
	r.GameTime = gameTime
}

func (r *RoundClock) UpdateWithPause(elapsed time.Duration, pauseDuration time.Duration) {
	r.GameTime = elapsed
	r.UpdatedAt = time.Now()
	r.PauseDuration = pauseDuration
	r.PausedAt = time.Now()
}

func (r *RoundClock) Unpause(elapsed time.Duration) {
	r.GameTime = elapsed
	r.UpdatedAt = time.Now()
	r.PauseDuration = 0
	r.PausedAt = time.Time{}
}
