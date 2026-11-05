package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestResolveSessionLevel_InstalledLevels(t *testing.T) {
	custom := evr.ToSymbol("mpl_combat_custom_host_level")
	withCustom := &GameServerPresence{InstalledLevels: []evr.Symbol{evr.LevelDyson, custom}}
	withoutCustom := &GameServerPresence{InstalledLevels: []evr.Symbol{evr.LevelDyson}}
	reportsNone := &GameServerPresence{}

	t.Run("server listing the custom level accepts it", func(t *testing.T) {
		got, err := resolveSessionLevel(evr.ModeCombatPrivate, custom, withCustom)
		if err != nil || got != custom {
			t.Fatalf("got (%v, %v), want (%v, nil)", got, err, custom)
		}
	})
	t.Run("server not listing the custom level rejects it", func(t *testing.T) {
		for name, gs := range map[string]*GameServerPresence{"other list": withoutCustom, "no list": reportsNone, "nil server": nil} {
			if _, err := resolveSessionLevel(evr.ModeCombatPrivate, custom, gs); err == nil {
				t.Errorf("%s: custom level accepted", name)
			}
		}
	})
	t.Run("shipped level works on any server", func(t *testing.T) {
		for name, gs := range map[string]*GameServerPresence{"with": withCustom, "without": withoutCustom, "no list": reportsNone, "nil server": nil} {
			got, err := resolveSessionLevel(evr.ModeCombatPrivate, evr.LevelGauss, gs)
			if err != nil || got != evr.LevelGauss {
				t.Errorf("%s: got (%v, %v)", name, got, err)
			}
		}
	})
	t.Run("unknown mode is still refused", func(t *testing.T) {
		if _, err := resolveSessionLevel(evr.ToSymbol("not_a_mode"), custom, withCustom); err == nil {
			t.Fatal("unknown mode accepted")
		}
	})
	t.Run("installed level does not leak across servers or into shipped list", func(t *testing.T) {
		if _, err := resolveSessionLevel(evr.ModeArenaPrivate, evr.LevelFission, withCustom); err == nil {
			t.Fatal("combat level not listed on server accepted for arena")
		}
	})
	t.Run("unset level picks a shipped level", func(t *testing.T) {
		got, err := resolveSessionLevel(evr.ModeSocialPrivate, evr.LevelUnspecified, withCustom)
		if err != nil || got != evr.LevelSocial {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
}
