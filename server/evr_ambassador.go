package server

import (
	"context"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	StorageCollectionAmbassador = "Ambassador"
	StorageKeyAmbassador        = "state"
)

// AmbassadorState tracks a player's ambassador program participation.
// Stored per-player in Nakama storage via the Storable interface.
type AmbassadorState struct {
	IsActive                    bool      `json:"is_active"`
	LastAmbassadorMatch         time.Time `json:"last_ambassador_match"`
	TotalAmbassadorMatches      int       `json:"total_ambassador_matches"`
	MatchesSinceLastAmbassador  int       `json:"matches_since_last_ambassador"`

	version string
}

func NewAmbassadorState() *AmbassadorState {
	return &AmbassadorState{}
}

func (s *AmbassadorState) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionAmbassador,
		Key:             StorageKeyAmbassador,
		PermissionRead:  runtime.STORAGE_PERMISSION_NO_READ,
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,
		Version:         s.version,
	}
}

func (s *AmbassadorState) SetStorageMeta(meta StorableMetadata) {
	s.version = meta.Version
}

// RecordAmbassadorMatch updates state after completing an ambassador match.
func (s *AmbassadorState) RecordAmbassadorMatch() {
	s.TotalAmbassadorMatches++
	s.LastAmbassadorMatch = time.Now().UTC()
	s.MatchesSinceLastAmbassador = 0
}

// RecordNormalMatch increments the counter tracking matches since the last
// ambassador match. Called after any non-ambassador match completes.
func (s *AmbassadorState) RecordNormalMatch() {
	s.MatchesSinceLastAmbassador++
}

// IsEligibleAmbassador checks whether a player meets the minimum thresholds
// (games played and mu) to participate as an ambassador.
func IsEligibleAmbassador(gamesPlayed int, mu float64, minGames int, minMu float64) bool {
	return gamesPlayed >= minGames && mu >= minMu
}

// ShouldAmbassadorThisMatch returns true if the player's cooldown has elapsed
// and they should ambassador in their next match. A zero-value LastAmbassadorMatch
// (never ambassadored) always returns true.
func ShouldAmbassadorThisMatch(state *AmbassadorState, cooldown int) bool {
	if cooldown <= 0 {
		return true
	}
	// Never ambassadored before — always eligible.
	if state.LastAmbassadorMatch.IsZero() {
		return true
	}
	return state.MatchesSinceLastAmbassador >= cooldown
}

// GetAmbassadorDivision returns the division one rank below the player's
// current division. If the player is already in the lowest division or
// the division is unknown, it returns the lowest division or empty string.
func GetAmbassadorDivision(currentDivision string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	for i, name := range names {
		if name == currentDivision {
			if i == 0 {
				return names[0] // Already lowest
			}
			return names[i-1]
		}
	}
	return "" // Unknown division
}

// ApplyAmbassadorMuReduction reduces mu by the given amount, clamping to zero.
func ApplyAmbassadorMuReduction(mu, reduction float64) float64 {
	result := mu - reduction
	if result < 0 {
		return 0
	}
	return result
}

// handleAmbassadorCommand toggles the player's ambassador mode via /ambassador.
func (d *DiscordAppBot) handleAmbassadorCommand(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	if userID == "" {
		return editInteractionResponse(s, i, "You must link your account first.")
	}

	settings := ServiceSettings()
	if settings == nil || !settings.Matchmaking.AmbassadorProgramEnabled() {
		return editInteractionResponse(s, i, "The ambassador program is not currently enabled.")
	}

	mm := settings.Matchmaking

	// Load current ambassador state (create if missing).
	state := NewAmbassadorState()
	if err := StorableRead(ctx, d.nk, userID, state, true); err != nil {
		return fmt.Errorf("failed to load ambassador state: %w", err)
	}

	// If activating, check eligibility first.
	if !state.IsActive {
		// Load the player's games played and mu to verify eligibility.
		gamesPlayed, err := GamesPlayedLoad(ctx, d.nk, userID, groupID, evr.ModeArenaPublic)
		if err != nil {
			logger.WithField("error", err).Warn("Failed to load games played for ambassador eligibility")
			gamesPlayed = 0
		}

		rating, err := MatchmakingRatingLoad(ctx, d.nk, userID, groupID, evr.ModeArenaPublic)
		if err != nil {
			logger.WithField("error", err).Warn("Failed to load rating for ambassador eligibility")
			rating = NewDefaultRating()
		}

		if !IsEligibleAmbassador(gamesPlayed, rating.Mu, mm.AmbassadorMinGamesPlayed, mm.AmbassadorMinMu) {
			return editInteractionResponse(s, i, fmt.Sprintf(
				"You need at least %d games played and a mu of %.0f+ to be an ambassador. "+
					"Keep playing and you'll get there!",
				mm.AmbassadorMinGamesPlayed, mm.AmbassadorMinMu))
		}

		state.IsActive = true
		if err := StorableWrite(ctx, d.nk, userID, state); err != nil {
			return fmt.Errorf("failed to save ambassador state: %w", err)
		}
		return editInteractionResponse(s, i,
			"Ambassador mode activated! You'll be matched with newer players to help them learn.")
	}

	// Deactivating.
	state.IsActive = false
	if err := StorableWrite(ctx, d.nk, userID, state); err != nil {
		return fmt.Errorf("failed to save ambassador state: %w", err)
	}
	return editInteractionResponse(s, i, "Ambassador mode deactivated.")
}
