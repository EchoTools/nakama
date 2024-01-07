package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

var testEmbed = []discordgo.MessageEmbed{
	{
		Type:      "rich",
		Timestamp: "2024-02-23T15:07:16.759000+00:00",
		Color:     16439902,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "ID: 695081603180789771",
		},
		Author: &discordgo.MessageEmbedAuthor{
			Name:    "Case 43 | Role Persist | sprockee",
			IconURL: "https://cdn.discordapp.com/avatars/695081603180789771/0d2cf2e3e861a9491894bde54a81a54b.jpg",
		},
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "User",
				Value:  "<@695081603180789771>",
				Inline: true,
			},
			{
				Name:   "Moderator",
				Value:  "<@695081603180789771>",
				Inline: true,
			},
			{
				Name:   "Length",
				Value:  "1 minute",
				Inline: true,
			},
			{
				Name:   "Role",
				Value:  "S5D",
				Inline: true,
			},
			{
				Name:   "Reason",
				Value:  "1m minute <@695081603180789771>",
				Inline: true,
			},
		},
	},
}
var _ = testEmbed

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input       string
		expected    time.Duration
		expectedErr error
	}{
		{"10 s", 10 * time.Second, nil},
		{"15 minutes", 15 * time.Minute, nil},
		{"5 minutes", 5 * time.Minute, nil},
		{"02 hours", 2 * time.Hour, nil},
		{"3 days", 3 * 24 * time.Hour, nil},
		{"1 week", 7 * 24 * time.Hour, nil},
		{"2 hour", 2 * time.Hour, nil},
		{"invalid", 0, fmt.Errorf("invalid duration: invalid number of fields: invalid")},
		{"10", 0, fmt.Errorf("invalid duration: invalid number of fields: 10")},
		{"1 x", 0, fmt.Errorf("invalid duration: invalid unit: 1 x")},
	}

	for _, test := range tests {
		duration, err := parseDuration(test.input)

		if duration != test.expected {
			t.Errorf("parseDuration(%s) - expected: %v, got: %v", test.input, test.expected, duration)
		}

		if (err == nil && test.expectedErr != nil) || (err != nil && test.expectedErr == nil) || (err != nil && test.expectedErr != nil && err.Error() != test.expectedErr.Error()) {
			t.Errorf("parseDuration(%s) - expected error: %v, got error: %v", test.input, test.expectedErr, err)
		}
	}
}
