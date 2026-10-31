package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"
)

type fixedMemberTransport struct {
	member *discordgo.Member
}

func (t fixedMemberTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := json.Marshal(t.member)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

type overloadedTransport struct{}

func (overloadedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, ErrDiscordRESTOverloaded
}

// newOverloadedIntegrator returns an integrator whose REST calls all fail with
// ErrDiscordRESTOverloaded and whose gateway state holds no members.
func newOverloadedIntegrator(t *testing.T) *DiscordIntegrator {
	t.Helper()
	dg, err := discordgo.New("Bot test")
	require.NoError(t, err)
	dg.StateEnabled = true
	dg.Client.Transport = overloadedTransport{}
	return &DiscordIntegrator{dg: dg, memberCache: &MapOf[string, cachedMember]{}}
}

func seedMember(d *DiscordIntegrator, guildID, discordID string, expiresAt time.Time) *discordgo.Member {
	m := &discordgo.Member{User: &discordgo.User{ID: discordID}, Roles: []string{"r-mgr"}}
	d.memberCache.Store(guildID+":"+discordID, cachedMember{member: m, expiresAt: expiresAt})
	return m
}

// canManageGuild grants from this lookup, so an expired entry must never be
// served when REST is overloaded: a removed role would keep its power.
func TestGuildMemberFresh_OverloadedDoesNotServeStale(t *testing.T) {
	d := newOverloadedIntegrator(t)
	seedMember(d, testGuildID, testManagerDisc, time.Now().Add(-time.Hour))

	member, err := d.GuildMemberFresh(testGuildID, testManagerDisc)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDiscordRESTOverloaded), "got %v", err)
	require.Nil(t, member)
}

func TestGuildMemberFresh_ServesCacheHitWithinTTL(t *testing.T) {
	d := newOverloadedIntegrator(t)
	want := seedMember(d, testGuildID, testManagerDisc, time.Now().Add(time.Minute))

	got, err := d.GuildMemberFresh(testGuildID, testManagerDisc)
	require.NoError(t, err)
	require.Same(t, want, got)
}

// GuildMember keeps its stale-on-overload behavior for login and sync callers.
func TestGuildMember_OverloadedStillServesStale(t *testing.T) {
	d := newOverloadedIntegrator(t)
	want := seedMember(d, testGuildID, testManagerDisc, time.Now().Add(-time.Hour))

	got, err := d.GuildMember(testGuildID, testManagerDisc)
	require.NoError(t, err)
	require.Same(t, want, got)
}

func TestGuildMember_UsesRESTInsteadOfStateWithoutMembersIntent(t *testing.T) {
	previous := ServiceSettings()
	updated := *previous
	updated.SyncGuildMembersWithoutIntent = true
	ServiceSettingsUpdate(&updated)
	t.Cleanup(func() { ServiceSettingsUpdate(previous) })

	dg, err := discordgo.New("Bot test")
	require.NoError(t, err)
	dg.StateEnabled = true
	dg.State = discordgo.NewState()
	stale := &discordgo.Member{User: &discordgo.User{ID: testManagerDisc, Username: "stale"}, Roles: []string{"old"}}
	require.NoError(t, dg.State.GuildAdd(&discordgo.Guild{ID: testGuildID, Members: []*discordgo.Member{stale}}))
	fresh := &discordgo.Member{User: &discordgo.User{ID: testManagerDisc, Username: "fresh"}, Roles: []string{"new"}}
	dg.Client.Transport = fixedMemberTransport{member: fresh}

	d := &DiscordIntegrator{dg: dg, memberCache: &MapOf[string, cachedMember]{}}
	got, err := d.GuildMember(testGuildID, testManagerDisc)
	require.NoError(t, err)
	require.Equal(t, []string{"new"}, got.Roles)
	stateMember, err := dg.State.Member(testGuildID, testManagerDisc)
	require.NoError(t, err)
	require.Equal(t, []string{"old"}, stateMember.Roles)
}
