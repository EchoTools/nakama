package evr

import (
	"encoding/json"
	"testing"
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
