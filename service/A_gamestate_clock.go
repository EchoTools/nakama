package service

import "time"

type RoundClock struct {
	Elapsed       time.Duration `json:"elapsed"`        // The time elapsed since the round started
	Duration      time.Duration `json:"duration"`       // The duration of the round
	UpdatedAt     time.Time     `json:"updated_at"`     // The time at which the round clock was last updated
	PausedAt      time.Time     `json:"paused_at"`      // The time at which the game was paused
	PauseDuration time.Duration `json:"pause_duration"` // The duration of the pause
}

func NewRoundClock(duration time.Duration, startAt time.Time) *RoundClock {
	if startAt.IsZero() {
		return &RoundClock{
			Duration: duration,
		}
	}

	return &RoundClock{
		Duration:  duration,
		Elapsed:   time.Since(startAt),
		UpdatedAt: time.Now(),
	}
}

func (r RoundClock) LatestAsNewClock() *RoundClock {
	return &RoundClock{
		Elapsed:       r.Current(),
		Duration:      r.Duration,
		UpdatedAt:     time.Now(),
		PausedAt:      r.PausedAt,
		PauseDuration: r.PauseDuration,
	}
}

func (r *RoundClock) Start() {
	r.Elapsed = 0
	r.UpdatedAt = time.Now()
	r.PauseDuration = 0
	r.PausedAt = time.Time{}
}

func (r *RoundClock) Current() time.Duration {
	if r.IsPaused() {
		return r.Elapsed
	}
	if r.IsOver() {
		return r.Duration
	}

	return r.Elapsed + time.Since(r.UpdatedAt)
}

func (r *RoundClock) PausedUntil() time.Time {
	return r.PausedAt.Add(r.PauseDuration)
}

func (r *RoundClock) IsPaused() bool {
	return time.Now().Before(r.PausedUntil())
}

func (r *RoundClock) IsOver() bool {
	return r.Duration != 0 && (r.Elapsed+time.Since(r.UpdatedAt) > r.Duration)
}

func (r *RoundClock) Remaining() time.Duration {
	return r.Duration - r.Current()
}

func (r *RoundClock) Update(elapsed time.Duration) {
	r.UpdatedAt = time.Now()

	if !r.PausedAt.IsZero() && elapsed > r.Elapsed {
		// The game is unpaused
		r.PausedAt = time.Time{}
		r.PauseDuration = 0
	}

	r.Elapsed = elapsed

}

func (r *RoundClock) UpdateWithPause(elapsed time.Duration, pauseDuration time.Duration) {
	r.Elapsed = elapsed
	r.UpdatedAt = time.Now()
	r.PauseDuration = pauseDuration
	r.PausedAt = time.Now()
}

func (r *RoundClock) Unpause(elapsed time.Duration) {
	r.Elapsed = elapsed
	r.UpdatedAt = time.Now()
	r.PauseDuration = 0
}
