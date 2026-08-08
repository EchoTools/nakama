package server

type IPInfo interface {
	DataProvider() string // IPQS, MaxMind, etc
	IsVPN() bool

	// IsDataCenter reports whether the address belongs to hosting, colocation
	// or datacenter infrastructure rather than a consumer connection.
	//
	// Distinct from IsVPN, and not a synonym for it. Both providers treat them
	// as separate facts: ip-api's `proxy` is "proxy, VPN or Tor exit" while
	// `hosting` is "hosting, colocated or data center", and IPQS reports
	// `connection_type` independently of its `vpn` flag. A rented VPS used as a
	// game proxy is commonly hosting-but-not-proxy, so IsVPN() alone does not
	// see it.
	//
	// Both providers already fetched this -- ip-api's request even names
	// `hosting` in its field mask -- and nothing read it. See #516.
	IsDataCenter() bool

	IsSharedIP() bool
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

func (r StubIPInfo) IsDataCenter() bool {
	return false
}

func (r StubIPInfo) IsSharedIP() bool {
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
