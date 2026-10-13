package evr

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLoginProfileCarriesTheNevrRuntimeIdentityAndSocialLevel(t *testing.T) {
	raw := []byte(`{"accountid":7,"displayname":"x","buildversion":631547,
		"nevr_identity":{"version":"4.2.0","commit":"abc","build":"v4.2.0-5-gabc","build_type":"Release"},
		"nevr_social":1}`)
	var profile LoginProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.NevrIdentity == nil || profile.NevrIdentity.Build != "v4.2.0-5-gabc" || profile.NevrIdentity.Commit != "abc" {
		t.Fatalf("nevr_identity not read: %+v", profile.NevrIdentity)
	}
	if profile.SocialLevel() != 1 {
		t.Fatalf("SocialLevel = %d, want 1", profile.SocialLevel())
	}

	var stock LoginProfile
	if err := json.Unmarshal([]byte(`{"accountid":7,"buildversion":631547}`), &stock); err != nil {
		t.Fatal(err)
	}
	if stock.NevrIdentity != nil || stock.SocialLevel() != 0 {
		t.Fatalf("a stock login declared something: %+v level=%d", stock.NevrIdentity, stock.SocialLevel())
	}
	var none *LoginProfile
	if none.SocialLevel() != 0 {
		t.Fatal("nil profile must be level 0")
	}
}

// The plugin report the nevr-runtime client declares at login (nevr-runtime#60, plugin_manifest.h): one
// object per configured plugin; "error" for an enabled one that did not load; "ver", "api", "caps" for a
// loaded one. A report that is the wrong shape never fails the login.
func TestLoginProfileCarriesThePluginReport(t *testing.T) {
	raw := []byte(`{"accountid":7,"buildversion":631547,"nevr_social":1,"nevr_plugins":[
		{"name":"example","file":"example.dll","enabled":true,"required":false,"loaded":true,"ver":"1.0.0","api":2,"caps":5},
		{"name":"session-unlocker","file":"session_unlocker.dll","enabled":true,"required":true,"loaded":false,"error":"LoadLibrary failed (126)"},
		{"name":"off","file":"off.dll","enabled":false,"required":false,"loaded":false}]}`)
	var profile LoginProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile.NevrPlugins) != 3 {
		t.Fatalf("got %d plugins, want 3: %+v", len(profile.NevrPlugins), profile.NevrPlugins)
	}
	first := profile.NevrPlugins[0]
	if first.Name != "example" || first.File != "example.dll" || !first.Enabled || first.Required || !first.Loaded ||
		first.Version != "1.0.0" || first.API != 2 || first.Caps != 5 {
		t.Fatalf("first entry read wrong: %+v", first)
	}
	second := profile.NevrPlugins[1]
	if !second.Required || second.Loaded || second.Error != "LoadLibrary failed (126)" {
		t.Fatalf("second entry read wrong: %+v", second)
	}
	if profile.NevrPlugins[2].Enabled {
		t.Fatalf("a disabled plugin read as enabled: %+v", profile.NevrPlugins[2])
	}
}

func TestLoginProfilePluginReportIsTolerant(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantLen int
	}{
		{"absent (stock client)", `{"accountid":7}`, 0},
		{"empty list (no plugins configured)", `{"accountid":7,"nevr_plugins":[]}`, 0},
		{"not an array", `{"accountid":7,"nevr_plugins":"nope"}`, 0},
		{"null", `{"accountid":7,"nevr_plugins":null}`, 0},
		{"one entry of the wrong shape, one good", `{"accountid":7,"nevr_plugins":[{"name":"bad","api":"x"},{"name":"good","file":"g.dll","enabled":true,"loaded":true}]}`, 1},
		{"an entry that is not an object", `{"accountid":7,"nevr_plugins":[7,{"name":"good","enabled":true}]}`, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var profile LoginProfile
			if err := json.Unmarshal([]byte(c.raw), &profile); err != nil {
				t.Fatalf("a plugin report of this shape failed the login payload: %v", err)
			}
			if profile.AccountId != 7 {
				t.Fatalf("the rest of the payload was lost: %+v", profile)
			}
			if len(profile.NevrPlugins) != c.wantLen {
				t.Fatalf("got %d plugins, want %d: %+v", len(profile.NevrPlugins), c.wantLen, profile.NevrPlugins)
			}
		})
	}
}

// The report is client input that is logged: it is bounded in count and in the length of each text.
func TestLoginProfilePluginReportIsBounded(t *testing.T) {
	entries := make([]string, 0, 70)
	for i := 0; i < 70; i++ {
		entries = append(entries, `{"name":"p","file":"p.dll","enabled":true}`)
	}
	raw := `{"accountid":7,"nevr_plugins":[` + strings.Join(entries, ",") + `]}`
	var profile LoginProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile.NevrPlugins) != MaxNevrPlugins {
		t.Fatalf("got %d plugins, want the cap %d", len(profile.NevrPlugins), MaxNevrPlugins)
	}

	long := strings.Repeat("€", 300) // 900 bytes, three bytes a rune: the cap falls inside one
	raw = `{"accountid":7,"nevr_plugins":[{"name":"` + long + `","file":"f","enabled":true,"error":"` + long + `"}]}`
	profile = LoginProfile{}
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		t.Fatal(err)
	}
	got := profile.NevrPlugins[0]
	for _, s := range []string{got.Name, got.Error} {
		if len(s) > MaxNevrPluginText || !utf8.ValidString(s) {
			t.Fatalf("text not bounded at a rune boundary: %d bytes, valid=%v", len(s), utf8.ValidString(s))
		}
	}
}

// An empty login payload is refused (evr_pipeline_login.go); a payload that declares only a plugin report is
// not empty, and one that declares nothing is.
func TestLoginProfileIsEmpty(t *testing.T) {
	var zero LoginProfile
	if !zero.IsEmpty() {
		t.Fatal("the zero profile must be empty")
	}
	var none *LoginProfile
	if !none.IsEmpty() {
		t.Fatal("a nil profile must be empty")
	}
	var empty LoginProfile
	if err := json.Unmarshal([]byte(`{"nevr_plugins":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if !empty.IsEmpty() {
		t.Fatalf("an empty report declares nothing: %+v", empty)
	}
	withReport := LoginProfile{NevrPlugins: NevrPlugins{{Name: "x"}}}
	if withReport.IsEmpty() {
		t.Fatal("a profile with a plugin report is not empty")
	}
	if (&LoginProfile{AccountId: 7}).IsEmpty() {
		t.Fatal("a profile with an account is not empty")
	}
}
