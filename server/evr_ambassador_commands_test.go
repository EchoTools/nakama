package server

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func findSlashCommand(name string) *discordgo.ApplicationCommand {
	for _, c := range mainSlashCommands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestAmbassadorCommandsAreRegisteredWithTheRightShape(t *testing.T) {
	type opt struct {
		name string
		typ  discordgo.ApplicationCommandOptionType
	}
	want := map[string][]opt{
		"set-ambassador":     {{"user", discordgo.ApplicationCommandOptionUser}},
		"ambassador-remove":  {{"user_id", discordgo.ApplicationCommandOptionString}, {"reason", discordgo.ApplicationCommandOptionString}},
		"ambassador-toggle":  nil,
		"ambassador-ready":   {{"user_id", discordgo.ApplicationCommandOptionString}},
		"level-self-promote": nil,
		"level-demote":       {{"user_id", discordgo.ApplicationCommandOptionString}},
	}
	for name, opts := range want {
		c := findSlashCommand(name)
		if c == nil {
			t.Errorf("command %q is not registered", name)
			continue
		}
		if len(c.Options) != len(opts) {
			t.Errorf("%s has %d options, want %d", name, len(c.Options), len(opts))
			continue
		}
		for i, o := range opts {
			got := c.Options[i]
			if got.Name != o.name || got.Type != o.typ || !got.Required {
				t.Errorf("%s option %d = %s/%v required=%v, want %s/%v required", name, i, got.Name, got.Type, got.Required, o.name, o.typ)
			}
		}
		if c.Contexts == nil || len(*c.Contexts) != 1 || (*c.Contexts)[0] != discordgo.InteractionContextGuild {
			t.Errorf("%s is not guild-only", name)
		}
	}
	if len(ambassadorCommandNames) != len(want) {
		t.Errorf("ambassadorCommandNames lists %d, want %d", len(ambassadorCommandNames), len(want))
	}
}

// A player can only ever promote themself: the command has no target option,
// so there is nothing to type that names another account.
func TestLevelSelfPromoteHasNoTargetOption(t *testing.T) {
	c := findSlashCommand("level-self-promote")
	if c == nil || len(c.Options) != 0 {
		t.Fatalf("level-self-promote = %+v, want no options", c)
	}
}

func TestAmbassadorRemoveReasonOptionIsBounded(t *testing.T) {
	c := findSlashCommand("ambassador-remove")
	if c == nil {
		t.Fatal("not registered")
	}
	if got := c.Options[1].MaxLength; got != AmbassadorRemovalReasonMaxLen {
		t.Errorf("reason MaxLength = %d, want %d", got, AmbassadorRemovalReasonMaxLen)
	}
}

func TestParseDiscordUserID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"123456789012345678", "123456789012345678", true},
		{"  123456789012345678 ", "123456789012345678", true},
		{"12345678901234567", "12345678901234567", true},
		{"12345678901234567890", "12345678901234567890", true},
		{"", "", false},
		{"Newbie", "", false},
		{"<@123456789012345678>", "", false},
		{"@someone", "", false},
		{"1234567890123456", "", false},      // too short
		{"123456789012345678901", "", false}, // too long
		{"12345678901234567a", "", false},
		{"-12345678901234567", "", false},
		{"١٢٣٤٥٦٧٨٩٠١٢٣٤٥٦٧٨", "", false}, // non-ASCII digits
	} {
		got, ok := parseDiscordUserID(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseDiscordUserID(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAmbassadorStaffCapacity(t *testing.T) {
	guild := func(enforcerRole string, enforcers ...string) *GuildGroup {
		gg := &GuildGroup{State: &GuildGroupState{RoleCache: map[string]map[string]bool{}}}
		gg.RoleMap.Enforcer = "role-enforcer"
		gg.RoleMap.Auditor = "role-auditor"
		if len(enforcers) > 0 {
			gg.State.RoleCache[enforcerRole] = map[string]bool{}
			for _, e := range enforcers {
				gg.State.RoleCache[enforcerRole][e] = true
			}
		}
		return gg
	}
	if got := ambassadorStaffCapacity(true, nil, tModUID); got != capacityOperator {
		t.Errorf("global operator = %q, want operator", got)
	}
	if got := ambassadorStaffCapacity(false, guild("role-enforcer", tModUID), tModUID); got != capacityEnforcer {
		t.Errorf("enforcer of this guild = %q, want enforcer", got)
	}
	// Auditors are not mods for these commands.
	if got := ambassadorStaffCapacity(false, guild("role-auditor", tModUID), tModUID); got != "" {
		t.Errorf("auditor = %q, want no capacity", got)
	}
	// Someone else's enforcer role does not carry over.
	if got := ambassadorStaffCapacity(false, guild("role-enforcer", tOtherUID), tModUID); got != "" {
		t.Errorf("not an enforcer of this guild = %q, want no capacity", got)
	}
	// Fail closed: no registered guild, no capacity.
	if got := ambassadorStaffCapacity(false, nil, tModUID); got != "" {
		t.Errorf("no guild = %q, want no capacity", got)
	}
}

// The reply wording is the approved replies.md. Pinned verbatim so it cannot
// drift without a failing test.
func TestAmbassadorReplyWording(t *testing.T) {
	const who = "<@100000000000000003>"
	for _, tc := range []struct{ name, got, want string }{
		{"grant ok", replyForGrant(ambassadorResult{Outcome: outcomeOK}, who), who + " is now an ambassador. They start in normal matchmaking; they switch with /ambassador-toggle."},
		{"grant already", replyForGrant(ambassadorResult{Outcome: outcomeNoop}, who), who + " is already an ambassador. Nothing changed."},
		{"grant self", replyForGrant(ambassadorResult{Outcome: outcomeSelfTarget}, who), "You can't grant yourself the ambassador role."},
		{"grant denied", replyForGrant(ambassadorResult{Outcome: outcomeDenied}, who), "Only moderators can use this command."},
		{"grant failed", replyForGrant(ambassadorResult{Outcome: outcomeError}, who), "Could not save the change. Nothing was changed; try again."},
		{"remove ok", replyForRemove(ambassadorResult{Outcome: outcomeOK}, who), who + " is no longer an ambassador and has been told why."},
		{"remove dm failed", replyForRemove(ambassadorResult{Outcome: outcomeOK, DMFailed: true}, who), who + " is no longer an ambassador, but I couldn't DM them."},
		{"remove noop", replyForRemove(ambassadorResult{Outcome: outcomeNoop}, who), who + " is not an ambassador. Nothing changed."},
		{"remove self", replyForRemove(ambassadorResult{Outcome: outcomeSelfTarget}, who), "You can't remove your own ambassador role."},
		{"remove no reason", replyForRemove(ambassadorResult{Outcome: outcomeReasonRequired}, who), "A reason is required."},
		{"remove long", replyForRemove(ambassadorResult{Outcome: outcomeReasonTooLong}, who), "The reason is too long (max 500 characters)."},
		{"remove denied", replyForRemove(ambassadorResult{Outcome: outcomeDenied}, who), "Only moderators can use this command."},
		{"toggle on", replyForToggle(ambassadorResult{Outcome: outcomeOK, On: true}), "Ambassador mode is ON. Public arena and combat matches you enter now won't change your rating or stats."},
		{"toggle off", replyForToggle(ambassadorResult{Outcome: outcomeOK}), "Ambassador mode is OFF. You're back in normal matchmaking and your games count."},
		{"toggle denied", replyForToggle(ambassadorResult{Outcome: outcomeDenied}), "You're not an ambassador."},
		{"ready ok", replyForReady(ambassadorResult{Outcome: outcomeOK}, who), who + " is marked ready and has been told their level was raised."},
		{"ready dm failed", replyForReady(ambassadorResult{Outcome: outcomeOK, DMFailed: true}, who), who + " is marked ready, but I couldn't DM them."},
		{"ready already", replyForReady(ambassadorResult{Outcome: outcomeNoop}, who), who + " is already at the raised level. Nothing changed."},
		{"ready self", replyForReady(ambassadorResult{Outcome: outcomeSelfTarget}, who), "You can't mark yourself ready."},
		{"ready denied", replyForReady(ambassadorResult{Outcome: outcomeDenied}, who), "Only ambassadors can use this command."},
		{"self ok", replyForSelfPromote(ambassadorResult{Outcome: outcomeOK}), "Your matchmaking level has been raised. You'll get a DM, and it applies from your next login."},
		{"self dm failed", replyForSelfPromote(ambassadorResult{Outcome: outcomeOK, DMFailed: true}), "Your matchmaking level has been raised and applies from your next login. I couldn't DM you."},
		{"self already", replyForSelfPromote(ambassadorResult{Outcome: outcomeNoop}), "Your level is already raised. Nothing changed."},
		{"demote ok", replyForDemote(ambassadorResult{Outcome: outcomeOK}, who), who + " was moved down a tier and has been told."},
		{"demote dm failed", replyForDemote(ambassadorResult{Outcome: outcomeOK, DMFailed: true}, who), who + " was moved down a tier, but I couldn't DM them."},
		{"demote noop", replyForDemote(ambassadorResult{Outcome: outcomeNoop}, who), who + " is not at the raised level. Nothing changed."},
		{"demote self", replyForDemote(ambassadorResult{Outcome: outcomeSelfTarget}, who), "You can't change your own level."},
		{"demote denied", replyForDemote(ambassadorResult{Outcome: outcomeDenied}, who), "Only moderators can use this command."},
		{"not an id (person)", replyNotAnID, "Enter the person's Discord user ID (the number), not their name."},
		{"not an id (player)", replyNotAnIDPlayer, "Enter the player's Discord user ID (the number), not their name."},
		{"no such player", replyNoSuchPlayer, "No linked player has that Discord ID."},
	} {
		if tc.got != tc.want {
			t.Errorf("%s reply:\n got %q\nwant %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestRemovalDM_FallsBackWhenTheNameCannotBeLooked(t *testing.T) {
	got := removalDM("", "r")
	if !strings.HasPrefix(got, "Hi there. Please read the following:\n") {
		t.Errorf("removal DM with no display name = %q", got)
	}
}
