package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type fakeNoticeSender struct {
	channels []string
	messages []*discordgo.MessageSend
	err      error
}

func (f *fakeNoticeSender) ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.channels = append(f.channels, channelID)
	f.messages = append(f.messages, data)
	return &discordgo.Message{}, f.err
}

func kickNoticeInput() enforcementNoticeInput {
	return enforcementNoticeInput{
		ActorName:       "Mod",
		ActorUsername:   "mod_user",
		ActorDiscordID:  "111",
		TargetIGN:       "Player",
		TargetDiscordID: "222",
		UserNotice:      "be nice",
		Notes:           "secret note",
		GuildID:         "999",
		GroupID:         "group-1",
		Now:             time.Unix(1700000000, 0),
	}
}

func TestEnforcementKickRequest_PostsToSuspensionLog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   *bool
		want bool
	}{
		{"omitted defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false opts out", boolPtr(false), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (EnforcementKickRequest{PostToSuspensionLog: tt.in}).PostsToSuspensionLog(); got != tt.want {
				t.Fatalf("PostsToSuspensionLog() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendEnforcementNotice(t *testing.T) {
	t.Parallel()

	t.Run("posts to the channel", func(t *testing.T) {
		t.Parallel()
		s := &fakeNoticeSender{}
		embed, comps := buildEnforcementNotice(kickNoticeInput())
		if err := sendEnforcementNotice(context.Background(), s, "chan-1", embed, comps); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(s.channels) != 1 || s.channels[0] != "chan-1" {
			t.Fatalf("expected one post to chan-1, got %v", s.channels)
		}
		if s.messages[0].Embed == nil || s.messages[0].Embed.Title != "Kicked Player" {
			t.Fatalf("expected a Kicked Player embed, got %+v", s.messages[0].Embed)
		}
	})

	t.Run("no channel posts nothing", func(t *testing.T) {
		t.Parallel()
		s := &fakeNoticeSender{}
		if err := sendEnforcementNotice(context.Background(), s, "", nil, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(s.channels) != 0 {
			t.Fatalf("expected no post, got %v", s.channels)
		}
	})

	t.Run("send failure is returned", func(t *testing.T) {
		t.Parallel()
		s := &fakeNoticeSender{err: errors.New("discord down")}
		if err := sendEnforcementNotice(context.Background(), s, "chan-1", nil, nil); err == nil {
			t.Fatal("expected the send error to be returned")
		}
	})

	t.Run("mentions are suppressed", func(t *testing.T) {
		t.Parallel()
		s := &fakeNoticeSender{}
		_ = sendEnforcementNotice(context.Background(), s, "chan-1", nil, nil)
		am := s.messages[0].AllowedMentions
		if am == nil || len(am.Parse) != 0 || len(am.Users) != 0 || len(am.Roles) != 0 {
			t.Fatalf("expected empty AllowedMentions, got %+v", am)
		}
	})
}

func TestBuildEnforcementNotice(t *testing.T) {
	t.Parallel()

	t.Run("plain kick shows notes unless restricted", func(t *testing.T) {
		t.Parallel()
		embed, comps := buildEnforcementNotice(kickNoticeInput())
		if len(comps) != 0 {
			t.Fatalf("a kick has no buttons, got %d rows", len(comps))
		}
		if v := embed.Fields[0].Value; !strings.Contains(v, "be nice") || !strings.Contains(v, "secret note") || !strings.Contains(v, "<@!111>") {
			t.Fatalf("kick details missing pieces: %q", v)
		}
		in := kickNoticeInput()
		in.RestrictNotes = true
		embed, _ = buildEnforcementNotice(in)
		if strings.Contains(embed.Fields[0].Value, "secret note") {
			t.Fatalf("notes leaked despite restriction: %q", embed.Fields[0].Value)
		}
	})

	t.Run("suspension titles by IGN and carries Edit/Void buttons", func(t *testing.T) {
		t.Parallel()
		in := kickNoticeInput()
		in.RecordsByGroupID = map[string][]GuildEnforcementRecord{"group-1": {{
			ID: "rec-1", GroupID: "group-1", UserNoticeText: "be nice",
			CreatedAt: time.Now(), UpdatedAt: time.Now(), Expiry: time.Now().Add(time.Hour),
		}}}
		embed, comps := buildEnforcementNotice(in)
		if !strings.HasPrefix(embed.Title, "Suspension:") || !strings.Contains(embed.Title, "Player") {
			t.Fatalf("unexpected title %q", embed.Title)
		}
		if len(comps) != 1 {
			t.Fatalf("expected one button row, got %d", len(comps))
		}
		row := comps[0].(*discordgo.ActionsRow)
		edit := row.Components[0].(*discordgo.Button)
		if edit.CustomID != "enf:e:rec-1:999:222" {
			t.Fatalf("edit button id = %q", edit.CustomID)
		}
	})

	t.Run("void has no buttons", func(t *testing.T) {
		t.Parallel()
		in := kickNoticeInput()
		in.RecordsByGroupID = map[string][]GuildEnforcementRecord{"group-1": {{ID: "rec-1", GroupID: "group-1", CreatedAt: time.Now(), UpdatedAt: time.Now(), Expiry: time.Now().Add(time.Hour)}}}
		in.Voids = map[string]GuildEnforcementRecordVoid{"rec-1": {RecordID: "rec-1"}}
		embed, comps := buildEnforcementNotice(in)
		if embed.Title != "Voided Suspension(s)" || len(comps) != 0 {
			t.Fatalf("title=%q buttons=%d", embed.Title, len(comps))
		}
	})
}
