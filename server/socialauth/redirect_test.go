package socialauth

import "testing"

// Browsers read a backslash in a path as a slash, so "/\evil.com" is
// "//evil.com" to them: a protocol-relative redirect off-site. Scheme and Host
// are both empty after url.Parse, which is all the old check looked at.
func TestSafeRedirectPath(t *testing.T) {
	ok := map[string]string{
		"/":            "/",
		"/account":     "/account",
		"/a/b?x=1&y=2": "/a/b?x=1&y=2",
	}
	for in, want := range ok {
		got, valid := safeRedirectPath(in)
		if !valid || got != want {
			t.Errorf("safeRedirectPath(%q) = %q, %v; want %q, true", in, got, valid, want)
		}
	}
	for _, in := range []string{
		`/\evil.com`, `/\/evil.com`, `\\evil.com`, `\evil.com`, "//evil.com",
		"https://evil.com", "javascript:alert(1)", "evil.com", "/%5Cevil.com/..",
		"/\t/evil.com", "/\n/evil.com", "/%09/evil.com", "",
	} {
		if got, valid := safeRedirectPath(in); valid {
			t.Errorf("safeRedirectPath(%q) = %q, true; want rejected", in, got)
		}
	}
}
