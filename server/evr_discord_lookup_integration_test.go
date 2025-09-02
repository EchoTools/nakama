package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// Define constants needed for the test  
const (
	WhoAmISecondaryColor = 0x00CC00 // Green
	WhoAmIBaseColor      = 0xFFA500 // Orange
	WhoAmISystemColor    = 0x800080 // Purple
)

// TestLookupEmbedSplitting tests the complete workflow of creating large profile embeds and splitting them
func TestLookupEmbedSplitting(t *testing.T) {
	// Simulate creating embeds like the lookup command would
	embeds := createMockLookupEmbeds()

	// Test the splitting functionality
	embedBatches := SplitEmbedsBySize(embeds)

	t.Logf("Original embeds: %d, Split into %d batches", len(embeds), len(embedBatches))

	// Verify all batches are within Discord limits
	for i, batch := range embedBatches {
		if len(batch) > DiscordMessageMaxEmbeds {
			t.Errorf("Batch %d has %d embeds, exceeds limit of %d", i, len(batch), DiscordMessageMaxEmbeds)
		}

		totalBatchSize := 0
		for j, embed := range batch {
			info := CalculateEmbedSize(embed)
			totalBatchSize += info.TotalSize

			if info.ExceedsLimits {
				t.Errorf("Batch %d, embed %d still exceeds limits: %+v", i, j, info)
			}

			t.Logf("Batch %d, Embed %d (%s): Size=%d, Fields=%d", i, j, embed.Title, info.TotalSize, info.FieldCount)
		}

		t.Logf("Batch %d total size: %d characters", i, totalBatchSize)
	}

	// Ensure we haven't lost any information
	totalOriginalEmbeds := len(embeds)
	totalSplitEmbeds := 0
	for _, batch := range embedBatches {
		totalSplitEmbeds += len(batch)
	}

	if totalSplitEmbeds < totalOriginalEmbeds {
		t.Errorf("Lost embeds during splitting: original=%d, split=%d", totalOriginalEmbeds, totalSplitEmbeds)
	}
}

// createMockLookupEmbeds creates embeds similar to what the lookup command would generate
func createMockLookupEmbeds() []*discordgo.MessageEmbed {
	var embeds []*discordgo.MessageEmbed

	// Account Details Embed
	accountEmbed := &discordgo.MessageEmbed{
		Title:       "Account Details",
		Description: "<@123456789>",
		Color:       WhoAmIBaseColor,
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Username", Value: "TestPlayer", Inline: true},
			{Name: "Discord ID", Value: "123456789", Inline: true},
			{Name: "Created", Value: "<t:1640995200:R>", Inline: true},
			{Name: "Last Seen", Value: "Now", Inline: true},
			{Name: "Public Matchmaking Guild", Value: "Test Guild", Inline: true},
			{Name: "Party Group", Value: "not set", Inline: true},
			{Name: "Display Names", Value: "TestGuild: `TestPlayer`\nAnotherGuild: `AltName`", Inline: false},
			{Name: "Guild Memberships", Value: "TestGuild (owner, enforcer)\nAnotherGuild (member)", Inline: false},
		},
	}
	embeds = append(embeds, accountEmbed)

	// Large Suspensions Embed with many guilds
	suspensionsEmbed := &discordgo.MessageEmbed{
		Title: "Suspensions",
		Color: 0xCC0000,
		Fields: make([]*discordgo.MessageEmbedField, 20), // Many suspension records
	}

	for i := 0; i < 20; i++ {
		guildName := "Guild" + string(rune(i+'A'))
		suspensionDetails := "**Active Suspension**\n" +
			"<t:1640995200:R> by <@987654321> (expires <t:1672531200:R>): Inappropriate behavior\n" +
			"*Additional notes: Player was warned multiple times before suspension.*\n\n" +
			"**Previous Suspension**\n" +
			"<t:1609459200:R> by <@123456789> (expired <t:1612137600:R>): Team killing\n" +
			"*Notes: First offense, shorter suspension applied.*"

		suspensionsEmbed.Fields[i] = &discordgo.MessageEmbedField{
			Name:   guildName,
			Value:  suspensionDetails,
			Inline: false,
		}
	}
	embeds = append(embeds, suspensionsEmbed)

	// Alternates Embed
	alternatesEmbed := &discordgo.MessageEmbed{
		Title: "Suspected Alternate Accounts",
		Color: WhoAmISystemColor,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name: "Account / Match Items",
				Value: "<@111111111> [AltAccount1] disabled <t:1640995200:R> (suspended until <t:1672531200:R>)\n" +
					"-  `IP:192.168.1.1`\n" +
					"-  `HWID:ABC123`\n" +
					"-  `UserAgent:OculusPC`\n\n" +
					"<@222222222> [AltAccount2]  <t:1640995200:R>\n" +
					"-  `IP:192.168.1.1`\n" +
					"-  `HWID:DEF456`\n" +
					"-  `UserAgent:OculusPC`",
				Inline: false,
			},
		},
	}
	embeds = append(embeds, alternatesEmbed)

	// VRML History Embed
	vrmlEmbed := &discordgo.MessageEmbed{
		Title: "VRML History",
		Color: WhoAmISecondaryColor,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "Player Page",
				Value:  "[TestPlayer](https://vrmasterleague.com/EchoArena/Players/12345)",
				Inline: false,
			},
			{
				Name: "Match Counts",
				Value: "S1: 45\nS2: 67\nS3: 89\nS4: 123\nS5: 78\nS6: 91\nS7: 156\nS8: 203\nS9: 187\nS10: 234\n" +
					"S11: 289\nS12: 312\nS13: 267\nS14: 298\nS15: 345",
				Inline: true,
			},
		},
	}
	embeds = append(embeds, vrmlEmbed)

	// Past Display Names Embed
	pastNamesEmbed := &discordgo.MessageEmbed{
		Title: "Past Display Names",
		Color: WhoAmIBaseColor,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name: "Display Names",
				Value: "`OldName1`\n`OldName2`\n`TestPlayer`\n`TempName`\n`AnotherName`\n" +
					"`YetAnotherName`\n`FinalName`\n`CurrentName`\n`RecentName`\n`LastName`",
				Inline: false,
			},
		},
	}
	embeds = append(embeds, pastNamesEmbed)

	// Current Matches Embed
	matchmakingEmbed := &discordgo.MessageEmbed{
		Title: "Matchmaking",
		Fields: []*discordgo.MessageEmbedField{
			{
				Name: "Match List",
				Value: "TestGuild (Arena)- https://echo.taxi/spark://c/12345678-1234-1234-1234-123456789012\n" +
					"<@!123456789>, <@!987654321>, <@!456789123>",
				Inline: false,
			},
			{
				Name:   "Last Matchmaking Error",
				Value:  "No suitable matches found",
				Inline: true,
			},
		},
	}
	embeds = append(embeds, matchmakingEmbed)

	return embeds
}

// TestMassiveLookupEmbedSplitting tests with extremely large profile data that requires multiple messages
func TestMassiveLookupEmbedSplitting(t *testing.T) {
	// Create multiple very large embeds that should require multiple messages
	var embeds []*discordgo.MessageEmbed

	// Create 20 large embeds (way more than the 10 embed limit per message)
	for i := 0; i < 20; i++ {
		embed := &discordgo.MessageEmbed{
			Title: "Large Profile Section " + string(rune(i+'A')),
			Color: WhoAmIBaseColor,
			Fields: make([]*discordgo.MessageEmbedField, 15), // Many fields
		}

		// Fill with large fields
		for j := 0; j < 15; j++ {
			embed.Fields[j] = &discordgo.MessageEmbedField{
				Name: "Field " + string(rune(j+'A')),
				Value: "This is a very long field value that contains a lot of text to simulate real profile data. " +
					"It includes details about user activities, guild memberships, enforcement actions, and other " +
					"important information that would typically be shown in a user profile lookup. This text is " +
					"intentionally verbose to test the embed splitting functionality under realistic conditions " +
					"where user profiles contain substantial amounts of data that need to be displayed.",
				Inline: j%2 == 0, // Mix of inline and non-inline fields
			}
		}

		embeds = append(embeds, embed)
	}

	// Test the splitting functionality
	embedBatches := SplitEmbedsBySize(embeds)

	t.Logf("Original embeds: %d, Split into %d batches", len(embeds), len(embedBatches))

	// Should definitely require multiple batches due to the 10 embed per message limit
	if len(embedBatches) <= 1 {
		t.Errorf("Expected multiple batches for %d large embeds, got %d batches", len(embeds), len(embedBatches))
	}

	// Verify all batches are within Discord limits
	totalProcessedEmbeds := 0
	for i, batch := range embedBatches {
		if len(batch) > DiscordMessageMaxEmbeds {
			t.Errorf("Batch %d has %d embeds, exceeds limit of %d", i, len(batch), DiscordMessageMaxEmbeds)
		}

		totalBatchSize := 0
		for j, embed := range batch {
			info := CalculateEmbedSize(embed)
			totalBatchSize += info.TotalSize

			if info.ExceedsLimits {
				t.Errorf("Batch %d, embed %d still exceeds limits: %+v", i, j, info)
			}
		}

		totalProcessedEmbeds += len(batch)
		t.Logf("Batch %d: %d embeds, %d total characters", i, len(batch), totalBatchSize)
	}

	// Ensure we haven't lost any embeds (note: large embeds may be split, so we should have >= original count)
	if totalProcessedEmbeds < len(embeds) {
		t.Errorf("Lost embeds during splitting: original=%d, processed=%d", len(embeds), totalProcessedEmbeds)
	} else if totalProcessedEmbeds > len(embeds) {
		t.Logf("Large embeds were split: original=%d, processed=%d (this is expected behavior)", len(embeds), totalProcessedEmbeds)
	}
}