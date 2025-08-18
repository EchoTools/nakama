package service

import (
	"fmt"
	"math"

	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/stat"
)

const (
	ServerScoreDefaultMaxRTT    = 150
	ServerScoreDefaultMinRTT    = 10
	ServerScoreDefaultThreshold = 100
)

var (
	ServerScorePointsDistribution = []float64{
		30, // difference in sum of pings between teams
		30, // within-team variance
		30, // overall server variance
		10, // overall high/low pings for server
	}

	ErrorIncorrectTeamCount = fmt.Errorf("must have exactly 2 teams")
	ErrorUnBalancedTeams    = fmt.Errorf("teams must have the same number of players")
	ErrorPingTooHigh        = fmt.Errorf("ping too high")
)

func CalculateServerScore(rttByPlayerByTeam [][]int, rttMin, rttMax, rttThreshold int) (float64, error) {

	if rttMin == 0 {
		rttMin = ServerScoreDefaultMinRTT
	}

	if rttMax == 0 {
		rttMax = ServerScoreDefaultMaxRTT
	}

	if rttThreshold == 0 {
		rttThreshold = ServerScoreDefaultMaxRTT
	}

	if len(rttByPlayerByTeam) != 2 {
		return 0, ErrorIncorrectTeamCount
	}

	if len(rttByPlayerByTeam[0]) != len(rttByPlayerByTeam[1]) {
		return 0, ErrorUnBalancedTeams
	}

	for i := range rttByPlayerByTeam {
		for j := range rttByPlayerByTeam[i] {
			if rttByPlayerByTeam[i][j] > rttThreshold {
				return 0, ErrorPingTooHigh
			}
		}
	}

	return calculateServerScore(rttByPlayerByTeam[0], rttByPlayerByTeam[1])
}
func calculateServerScore(bluePings, orangePings []int) (float64, error) {
	if bluePings == nil || orangePings == nil {
		return 0, fmt.Errorf("nil pings")
	}

	ppt := len(bluePings) // players per team
	minPing := float64(10)
	maxPing := float64(150)
	pingThreshold := float64(100)
	pointsDistribution := []float64{30, 30, 30, 10}

	bPings := make([]float64, len(bluePings))
	oPings := make([]float64, len(orangePings))
	for i := range bluePings {
		bPings[i] = float64(bluePings[i])
		oPings[i] = float64(orangePings[i])
	}
	// Sanity checks
	switch {
	case ppt < 4:
		return 0, fmt.Errorf("number of players per team is less than 4")
	case ppt > 5:
		return 0, fmt.Errorf("number of players per team is greater than 5")
	case ppt != len(oPings):
		return 0, fmt.Errorf("number of players in blue team does not match number of players in orange team")
	case floats.Max(bPings) > maxPing || floats.Max(oPings) > maxPing:
		return 0, fmt.Errorf("ping exceeds maximum allowed value")
	}

	// Calculate max variances and sum diff for normalization
	maxServerVar := stat.Variance(repeat(minPing, maxPing, ppt*2), nil)
	a := repeat(minPing, maxPing, ppt/2)
	b := stat.Variance(repeat(minPing, maxPing, (ppt+1)/2), nil)
	maxTeamVar := stat.Variance(append(a, b), nil)
	maxSumDiff := float64(ppt) * (maxPing - minPing)

	// Sum difference points
	blueSum, orangeSum := floats.Sum(bPings), floats.Sum(oPings)
	sumDiff := math.Abs(blueSum - orangeSum)
	sumPoints := (1 - (sumDiff / maxSumDiff)) * pointsDistribution[0]

	// Team variance points
	meanVar := average(stat.Variance(bPings, nil), stat.Variance(oPings, nil))
	teamPoints := (1 - (meanVar / maxTeamVar)) * pointsDistribution[1]

	// Server variance points
	bothPings := append(bPings, oPings...)
	serverVar := stat.Variance(bothPings, nil)
	serverPoints := (1 - (serverVar / maxServerVar)) * pointsDistribution[2]

	// High/low ping points
	hilo := float64((blueSum+orangeSum)-(minPing*float64(ppt)*2)) / float64((pingThreshold*float64(ppt)*2)-(minPing*float64(ppt)*2))
	hiloPoints := (1 - hilo) * pointsDistribution[3]

	// Final score
	finalScore := floats.Sum([]float64{sumPoints, teamPoints, serverPoints, hiloPoints})
	return finalScore, nil
}

func repeat[T int | float64](min T, max T, count int) []T {
	result := make([]T, count)
	for i := 0; i < count; i++ {
		result[i] = min
	}
	for i := count / 2; i < count; i++ {
		result[i] = max
	}
	return result
}

func average(values ...float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return floats.Sum(values) / float64(len(values))
}

// This s a port of the ServerScore function from github.com/ntsfranz/spark
func VRMLServerScore(latencies [][]float64, minRTT, maxRTT, thresholdRTT float64, pointsDistro []float64) float64 {

	teamSize := len(latencies[0])

	// Calculate the maximum variance of the server
	rtts := make([]float64, teamSize*2)
	for i := 0; i < teamSize; i++ {
		rtts[i] = minRTT
		rtts[i+teamSize] = maxRTT
	}

	maxServerVar := stat.Variance(rtts, nil)

	// calculate the maximum variance within a team

	rtts = make([]float64, teamSize)
	for i := 0; i < (teamSize+1)/2; i++ {
		// first half of the team has minRTT
		rtts[i] = minRTT

		// second half of the team has maxRTT
		rtts[i+(teamSize+1)/2] = maxRTT
	}

	maxTeamVariance := stat.Variance(rtts, nil)

	maxSumDiff := (maxRTT - minRTT) * float64(teamSize)

	// Sum difference points
	blueSum, orangeSum := floats.Sum(latencies[0]), floats.Sum(latencies[1])
	sumDiff := math.Abs(blueSum - orangeSum)
	sumPoints := (1 - (sumDiff / maxSumDiff)) * pointsDistro[0]

	allRTTs := append(latencies[0], latencies[1]...)

	// Team variance points
	meanVar := floats.Sum(allRTTs) / float64(len(allRTTs))

	teamPoints := (1 - (meanVar / maxTeamVariance)) * pointsDistro[1]

	serverVar := stat.Variance(allRTTs, nil)
	serverPoints := (1 - (serverVar / maxServerVar)) * pointsDistro[2]

	lobbySize := float64(teamSize) * 2

	// High/low ping points
	hilo := float64((blueSum + orangeSum) - (minRTT*lobbySize)/float64((thresholdRTT*lobbySize)-(minRTT*lobbySize)))
	hiloPoints := (1 - hilo) * pointsDistro[3]

	// Final score
	finalScore := floats.Sum([]float64{sumPoints, teamPoints, serverPoints, hiloPoints})
	return finalScore
}
