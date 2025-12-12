package server

import (
	"math"
	"reflect"

	"slices"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/thriftrw/ptr"
)

type (
	RatedMatch [2]RatedEntryTeam
)

type RatedEntry struct {
	Entry  *MatchmakerEntry
	Rating types.Rating
}

type RatedEntryTeam []RatedEntry

func (t RatedEntryTeam) Len() int {
	return len(t)
}

func (t RatedEntryTeam) Strength() float64 {
	s := 0.0
	for _, p := range t {
		s += p.Rating.Mu
	}
	return s
}

type RatedTeam []types.Rating

func (t RatedTeam) Strength() float64 {
	s := 0.0
	for _, p := range t {
		s += p.Mu
	}
	return s
}

func (t RatedTeam) Rating() types.Rating {
	if len(t) == 0 {
		return NewDefaultRating()
	}
	meanMu := t.Strength() / float64(len(t))
	sumSigmaSquared := 0.0
	for _, p := range t {
		sumSigmaSquared += p.Sigma * p.Sigma
	}
	averageSigmaSquared := sumSigmaSquared / float64(len(t))
	rmsSigma := math.Sqrt(averageSigmaSquared)

	return NewRating(0, meanMu, rmsSigma)
}

func (t RatedTeam) Ordinal() float64 {
	return rating.Ordinal(t.Rating())
}

// NewRating creates a new rating with the specified parameters.
// If z is 0, it uses the default Z value from service settings.
// If mu is 0 or negative, it uses the default Mu value.
// If sigma is 0 or negative, it calculates sigma as mu/z.
func NewRating[T int | int64 | float64](z, mu, sigma T) types.Rating {
	r := NewDefaultRating()
	if zInt, ok := any(z).(int); ok && zInt > 0 {
		r.Z = zInt
	}
	if muFloat := float64(mu); muFloat > 0 {
		r.Mu = muFloat
	}
	if sigmaFloat := float64(sigma); sigmaFloat > 0 {
		r.Sigma = sigmaFloat
	} else {
		r.Sigma = r.Mu / float64(r.Z)
	}
	return r
}

// NewRatingFromConfig creates a new rating using values from service settings.
func NewRatingFromConfig(z int, mu, sigma float64, config *SkillRatingSettings) types.Rating {
	defaults := GetRatingDefaults(config)
	r := types.Rating{
		Z:     defaults.Z,
		Mu:    defaults.Mu,
		Sigma: defaults.Sigma,
	}
	if z > 0 {
		r.Z = z
	}
	if mu > 0 {
		r.Mu = mu
	}
	if sigma > 0 {
		r.Sigma = sigma
	} else if mu > 0 {
		r.Sigma = mu / float64(r.Z)
	}
	return r
}

// GetRatingDefaults returns the rating defaults from service settings, or hardcoded defaults if not available.
func GetRatingDefaults(config *SkillRatingSettings) RatingDefaults {
	if config != nil && config.Defaults.Z > 0 {
		return config.Defaults
	}
	// Fallback to hardcoded defaults if service settings not available
	return RatingDefaults{
		Z:     3,
		Mu:    10.0,
		Sigma: 10.0 / 3.0,
		Tau:   0.3,
	}
}

// NewDefaultRating creates a rating with default values from service settings.
func NewDefaultRating() types.Rating {
	settings := ServiceSettings()
	if settings != nil {
		defaults := GetRatingDefaults(&settings.SkillRating)
		return types.Rating{
			Z:     defaults.Z,
			Mu:    defaults.Mu,
			Sigma: defaults.Sigma,
		}
	}
	// Fallback to hardcoded defaults
	return types.Rating{
		Z:     3,
		Mu:    10.0,
		Sigma: 10.0 / 3.0,
	}
}

// NewDefaultRatingWithConfig creates a rating with default values from provided config.
func NewDefaultRatingWithConfig(config *SkillRatingSettings) types.Rating {
	defaults := GetRatingDefaults(config)
	return types.Rating{
		Z:     defaults.Z,
		Mu:    defaults.Mu,
		Sigma: defaults.Sigma,
	}
}

// calculateScoreFromStats calculates a player's score from their match stats using the provided multipliers.
func calculateScoreFromStats(stats evr.MatchTypeStats, multipliers map[string]float64) float64 {
	if len(multipliers) == 0 {
		return 0
	}

	score := 0.0
	statsValue := reflect.ValueOf(stats)
	statsType := statsValue.Type()

	for i := 0; i < statsType.NumField(); i++ {
		field := statsType.Field(i)
		// Get JSON tag name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" {
			continue
		}
		// Extract field name from json tag (handle ",omitempty" etc)
		fieldName := jsonTag
		if idx := len(jsonTag); idx > 0 {
			for j, c := range jsonTag {
				if c == ',' {
					fieldName = jsonTag[:j]
					break
				}
			}
		}

		if multiplier, ok := multipliers[fieldName]; ok {
			fieldValue := statsValue.Field(i)
			switch fieldValue.Kind() {
			case reflect.Int64:
				score += float64(fieldValue.Int()) * multiplier
			case reflect.Float64:
				score += fieldValue.Float() * multiplier
			}
		}
	}

	return score
}

// CalculateNewTeamRatings calculates the new team-based player ratings based on the match outcome.
// This considers team performance and uses the TeamStatMultipliers from service settings.
func CalculateNewTeamRatings(playerInfos []PlayerInfo, playerStats map[evr.EvrId]evr.MatchTypeStats, blueWins bool) map[string]types.Rating {
	return CalculateNewTeamRatingsWithConfig(playerInfos, playerStats, blueWins, nil)
}

// CalculateNewTeamRatingsWithConfig calculates new team-based player ratings with an optional config override.
func CalculateNewTeamRatingsWithConfig(playerInfos []PlayerInfo, playerStats map[evr.EvrId]evr.MatchTypeStats, blueWins bool, config *SkillRatingSettings) map[string]types.Rating {
	// Get config from service settings if not provided
	if config == nil {
		if settings := ServiceSettings(); settings != nil {
			config = &settings.SkillRating
		}
	}

	// Get defaults
	var tau float64 = 0.3
	var winningTeamBonus float64 = 4.0
	var multipliers map[string]float64

	if config != nil {
		tau = config.Defaults.Tau
		if tau == 0 {
			tau = 0.3
		}
		winningTeamBonus = config.WinningTeamBonus
		if winningTeamBonus == 0 {
			winningTeamBonus = 4.0
		}
		multipliers = config.TeamStatMultipliers
	}

	// Use default multipliers if none provided
	if len(multipliers) == 0 {
		multipliers = map[string]float64{
			"Points":      1.0,
			"Assists":     2.0,
			"Saves":       3.0,
			"Passes":      1.0,
			"ShotsOnGoal": -1.0,
		}
	}

	winningTeam := BlueTeam
	if !blueWins {
		winningTeam = OrangeTeam
	}

	// copy the players slice so as to not modify the original
	playerInfos = slices.Clone(playerInfos)

	// Remove players that are not on blue/orange
	for i := 0; i < len(playerInfos); i++ {
		if !playerInfos[i].IsCompetitor() {
			playerInfos = slices.Delete(playerInfos, i, i+1)
			i--
			continue
		}
	}

	// Create a map of player scores
	playerScores := make(map[string]int, len(playerInfos))
	for _, p := range playerInfos {
		score := 0.0
		if stats, ok := playerStats[p.EvrID]; ok {
			score = calculateScoreFromStats(stats, multipliers)
		}
		if p.Team == winningTeam {
			score += winningTeamBonus
		}
		playerScores[p.SessionID] = int(score)
	}

	// Sort the players by score
	slices.SortStableFunc(playerInfos, func(a, b PlayerInfo) int {
		// Sort winning team first
		if a.Team == winningTeam && b.Team != winningTeam {
			return -1
		}
		if a.Team != winningTeam && b.Team == winningTeam {
			return 1
		}
		// Sort by score (descending)
		return playerScores[b.SessionID] - playerScores[a.SessionID]
	})

	// Split the roster by teamRatings (all players are separated on their own team)
	teamRatings := make([]types.Team, len(playerInfos))
	for i, p := range playerInfos {
		teamRatings[i] = types.Team{p.Rating()}
	}

	// Create a map of player scores; used to weight the new ratings
	scores := make([]int, len(playerInfos))
	for i, p := range playerInfos {
		scores[i] = playerScores[p.SessionID]
	}

	// Calculate the new ratings
	teamRatings = rating.Rate(teamRatings, &types.OpenSkillOptions{
		Score: scores,
		Tau:   ptr.Float64(tau),
	})

	ratingMap := make(map[string]types.Rating, len(playerInfos))
	for i, team := range teamRatings {
		sID := playerInfos[i].SessionID
		ratingMap[sID] = team[0]
	}

	return ratingMap
}

// CalculateNewPlayerRatings calculates the new individual player ratings based on personal performance.
// This uses the PlayerStatMultipliers from service settings (Points, Assists, Saves by default).
// Deprecated: Use CalculateNewTeamRatings for team-based ratings or CalculateNewIndividualRatings for individual ratings.
func CalculateNewPlayerRatings(playerInfos []PlayerInfo, playerStats map[evr.EvrId]evr.MatchTypeStats, blueWins bool) map[string]types.Rating {
	// For backwards compatibility, delegate to team ratings
	return CalculateNewTeamRatings(playerInfos, playerStats, blueWins)
}

// CalculateNewIndividualRatings calculates individual player ratings based on personal performance only.
// This uses the PlayerStatMultipliers from service settings, which focus on individual contribution
// (Points, Assists, Saves) rather than team-based metrics.
func CalculateNewIndividualRatings(playerInfos []PlayerInfo, playerStats map[evr.EvrId]evr.MatchTypeStats, blueWins bool) map[string]types.Rating {
	return CalculateNewIndividualRatingsWithConfig(playerInfos, playerStats, blueWins, nil)
}

// CalculateNewIndividualRatingsWithConfig calculates individual player ratings with an optional config override.
func CalculateNewIndividualRatingsWithConfig(playerInfos []PlayerInfo, playerStats map[evr.EvrId]evr.MatchTypeStats, blueWins bool, config *SkillRatingSettings) map[string]types.Rating {
	// Get config from service settings if not provided
	if config == nil {
		if settings := ServiceSettings(); settings != nil {
			config = &settings.SkillRating
		}
	}

	// Get defaults
	var tau float64 = 0.3
	var multipliers map[string]float64

	if config != nil {
		tau = config.Defaults.Tau
		if tau == 0 {
			tau = 0.3
		}
		multipliers = config.PlayerStatMultipliers
	}

	// Use default player multipliers if none provided
	if len(multipliers) == 0 {
		multipliers = map[string]float64{
			"Points":  1.0,
			"Assists": 1.0,
			"Saves":   2.0,
		}
	}

	winningTeam := BlueTeam
	if !blueWins {
		winningTeam = OrangeTeam
	}

	// copy the players slice so as to not modify the original
	playerInfos = slices.Clone(playerInfos)

	// Remove players that are not on blue/orange
	for i := 0; i < len(playerInfos); i++ {
		if !playerInfos[i].IsCompetitor() {
			playerInfos = slices.Delete(playerInfos, i, i+1)
			i--
			continue
		}
	}

	// Create a map of player scores based on individual performance only
	// Note: Individual ratings don't include winning team bonus to focus purely on personal contribution
	playerScores := make(map[string]int, len(playerInfos))
	for _, p := range playerInfos {
		score := 0.0
		if stats, ok := playerStats[p.EvrID]; ok {
			score = calculateScoreFromStats(stats, multipliers)
		}
		playerScores[p.SessionID] = int(score)
	}

	// Sort the players by score (winning team still sorted first for rank ordering)
	slices.SortStableFunc(playerInfos, func(a, b PlayerInfo) int {
		// Sort winning team first
		if a.Team == winningTeam && b.Team != winningTeam {
			return -1
		}
		if a.Team != winningTeam && b.Team == winningTeam {
			return 1
		}
		// Sort by score (descending)
		return playerScores[b.SessionID] - playerScores[a.SessionID]
	})

	// Split the roster by teamRatings (all players are separated on their own team)
	teamRatings := make([]types.Team, len(playerInfos))
	for i, p := range playerInfos {
		teamRatings[i] = types.Team{p.Rating()}
	}

	// Create a map of player scores; used to weight the new ratings
	scores := make([]int, len(playerInfos))
	for i, p := range playerInfos {
		scores[i] = playerScores[p.SessionID]
	}

	// Calculate the new ratings
	teamRatings = rating.Rate(teamRatings, &types.OpenSkillOptions{
		Score: scores,
		Tau:   ptr.Float64(tau),
	})

	ratingMap := make(map[string]types.Rating, len(playerInfos))
	for i, team := range teamRatings {
		sID := playerInfos[i].SessionID
		ratingMap[sID] = team[0]
	}

	return ratingMap
}
