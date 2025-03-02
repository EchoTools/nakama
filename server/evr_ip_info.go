package server

type IPInfo interface {
	DataProvider() string // IPQS, MaxMind, etc
	IsVPN() bool
	Latitude() float64
	Longitude() float64
	City() string
	Region() string
	CountryCode() string
	GeoHash(geoPrecision uint) string
	ASN() int
	FraudScore() int // 0 to 100
	ISP() string
	Organization() string
}

var _ = IPInfo(&StubIPInfo{})

type StubIPInfo struct{}

func (r StubIPInfo) DataProvider() string {
	return "Dummy"
}

func (r StubIPInfo) IsVPN() bool {
	return false
}

func (r StubIPInfo) Latitude() float64 {
	return 0
}

func (r StubIPInfo) Longitude() float64 {
	return 0
}

func (r StubIPInfo) City() string {
	return ""
}

func (r StubIPInfo) Region() string {
	return ""
}

func (r StubIPInfo) CountryCode() string {
	return ""
}

func (r StubIPInfo) GeoHash(geoPrecision uint) string {
	return ""
}

func (r StubIPInfo) ASN() int {
	return 0
}

func (r StubIPInfo) FraudScore() int {
	return 0
}

func (r StubIPInfo) ISP() string {
	return "N/A"
}

func (r StubIPInfo) Organization() string {
	return ""
}
