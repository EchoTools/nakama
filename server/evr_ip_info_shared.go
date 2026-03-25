package server

import "strings"

var knownSharedIPProviders = []string{
	"spacex services",
	"starlink",
}

func isKnownSharedIPProvider(isp, org string) bool {
	haystack := strings.ToLower(isp + " " + org)
	for _, provider := range knownSharedIPProviders {
		if strings.Contains(haystack, provider) {
			return true
		}
	}
	return false
}
