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
	default:
		return DivisionMaster
	}
}

func DivisionFromName(name string) Division {
	switch name {
	case "Master":
		return DivisionMaster
	case "Diamond":
		return DivisionDiamond
	case "Platinum":
		return DivisionPlatinum
	case "Gold":
		return DivisionGold
	case "Silver":
		return DivisionSilver
	case "Bronze":
		return DivisionBronze
	default:
		return DivisionMaster
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
	} else {
		return DivisionBronze
	}
}
