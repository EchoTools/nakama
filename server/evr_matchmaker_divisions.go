package server

type Division int

const (
	DivisionGreen Division = iota
	DivisionBronze
	DivisionSilver
	DivisionGold
	DivisionPlatinum
	DivisionDiamond
	DivisionMaster
)

func (d Division) String() string {
	return [...]string{"green", "bronze", "silver", "gold", "platinum", "diamond", "master"}[d]
}

func (d Division) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

func (d *Division) UnmarshalText(text []byte) error {
	*d = DivisionFromName(string(text))
	return nil
}

func DivisionFromName(name string) Division {
	switch name {
	case "green":
		return DivisionGreen
	case "bronze":
		return DivisionBronze
	case "silver":
		return DivisionSilver
	case "gold":
		return DivisionGold
	case "platinum":
		return DivisionPlatinum
	case "diamond":
		return DivisionDiamond
	case "master":
		return DivisionMaster
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
