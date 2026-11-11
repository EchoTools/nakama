package socialauth

import (
	"net/url"
	"strings"
)

// safeRedirectPath returns the request URI to redirect to after login, and
// whether raw is acceptable: a same-site absolute path only. Scheme and Host
// being empty is not enough: browsers treat a backslash as a slash, so
// "/\evil.com" is "//evil.com" to them. Reject any backslash or control
// character (raw or percent-decoded) and any path that starts with "//".
func safeRedirectPath(raw string) (string, bool) {
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "", false
	}
	for _, s := range []string{raw, u.Path} {
		if strings.ContainsAny(s, `\`) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", false
		}
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "", false
	}
	return u.RequestURI(), true
}
