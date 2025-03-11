package server

type Division int

const (
	DivisionMaster Division = iota
	DivisionDiamond
	DivisionPlatinum
	DivisionGold
	DivisionSilver
	DivisionBronze
	DivisionGreen
)

func (d Division) String() string {
	return [...]string{"master", "diamond", "platinum", "gold", "silver", "bronze", "green"}[d]
}

func (d Division) Value() int {
	return int(d)
}

func DivisionFromValue(value int) Division {
	switch value {
	case 0:
		return DivisionMaster
	case 1:
		return DivisionDiamond
	case 2:
		return DivisionPlatinum
	case 3:
		return DivisionGold
	case 4:
		return DivisionSilver
	case 5:
		return DivisionBronze
	case 6:
		return DivisionGreen
	default:
		return DivisionGreen
	}
}
func DivisionFromName(name string) Division {
	switch name {
	case "master":
		return DivisionMaster
	case "diamond":
		return DivisionDiamond
	case "platinum":
		return DivisionPlatinum
	case "gold":
		return DivisionGold
	case "silver":
		return DivisionSilver
	case "bronze":
		return DivisionBronze
	case "green":
		return DivisionGreen
	default:
		return DivisionGreen
	}
}

func DivisionFromScore(rankPercentile float64) Division {
	if rankPercentile >= 0.99 {
		return DivisionMaster
	} else if rankPercentile >= 0.95 {
		return DivisionDiamond
	} else if rankPercentile >= 0.85 {
		return DivisionPlatinum
	} else if rankPercentile >= 0.70 {
		return DivisionGold
	} else if rankPercentile >= 0.50 {
		return DivisionSilver
	} else if rankPercentile >= 0.25 {
		return DivisionBronze
	} else {
		return DivisionGreen
	}
}
