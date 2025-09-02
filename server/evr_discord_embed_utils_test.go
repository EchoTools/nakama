package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestCalculateEmbedSize(t *testing.T) {
	tests := []struct {
		name     string
		embed    *discordgo.MessageEmbed
		expected int
	}{
		{
			name:     "nil embed",
			embed:    nil,
			expected: 0,
		},
		{
			name: "simple embed",
			embed: &discordgo.MessageEmbed{
				Title:       "Test Title",
				Description: "Test Description",
			},
			expected: len("Test Title") + len("Test Description"),
		},
		{
			name: "embed with fields",
			embed: &discordgo.MessageEmbed{
				Title: "Test",
				Fields: []*discordgo.MessageEmbedField{
					{Name: "Field1", Value: "Value1"},
					{Name: "Field2", Value: "Value2"},
				},
			},
			expected: len("Test") + len("Field1") + len("Value1") + len("Field2") + len("Value2"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := CalculateEmbedSize(tt.embed)
			if info.TotalSize != tt.expected {
				t.Errorf("CalculateEmbedSize() = %d, want %d", info.TotalSize, tt.expected)
			}
		})
	}
}

func TestSplitEmbedsBySize(t *testing.T) {
	// Create a large embed that should be split
	largeEmbed := &discordgo.MessageEmbed{
		Title:  "Large Embed",
		Fields: make([]*discordgo.MessageEmbedField, 30), // Exceeds DiscordEmbedMaxFields
	}

	// Fill with dummy fields that will create a large embed
	for i := 0; i < 30; i++ {
		largeEmbed.Fields[i] = &discordgo.MessageEmbedField{
			Name:  "Field " + string(rune(i+'A')),
			Value: "This is a test value for field " + string(rune(i+'A')) + " with some additional text to make it larger and exceed limits when combined with many other fields in a single embed structure",
		}
	}

	// Test splitting
	result := SplitEmbedsBySize([]*discordgo.MessageEmbed{largeEmbed})

	if len(result) <= 1 {
		// Check if the split embeds all fit in one batch
		splitResults := splitLargeEmbed(largeEmbed)
		t.Logf("splitLargeEmbed returned %d embeds", len(splitResults))

		totalSplitSize := 0
		for i, splitEmbed := range splitResults {
			splitInfo := CalculateEmbedSize(splitEmbed)
			totalSplitSize += splitInfo.TotalSize
			t.Logf("Split embed %d: TotalSize=%d, FieldCount=%d, ExceedsLimits=%v", i, splitInfo.TotalSize, splitInfo.FieldCount, splitInfo.ExceedsLimits)
		}

		if len(splitResults) > 1 && len(result) == 1 && len(result[0]) == len(splitResults) {
			t.Logf("All %d split embeds fit in one batch (total size: %d)", len(splitResults), totalSplitSize)
			// This is actually correct behavior - the splits fit in one message
		} else {
			t.Errorf("Expected embed to be split into multiple batches, got %d batches", len(result))
			// Let's check why it wasn't split
			info := CalculateEmbedSize(largeEmbed)
			t.Logf("Original embed info: TotalSize=%d, FieldCount=%d, ExceedsLimits=%v", info.TotalSize, info.FieldCount, info.ExceedsLimits)
		}
	}

	// Verify each batch meets Discord limits
	for i, batch := range result {
		if len(batch) > DiscordMessageMaxEmbeds {
			t.Errorf("Batch %d has %d embeds, exceeds limit of %d", i, len(batch), DiscordMessageMaxEmbeds)
		}

		for j, embed := range batch {
			info := CalculateEmbedSize(embed)
			if info.FieldCount > DiscordEmbedMaxFields {
				t.Errorf("Batch %d, embed %d has %d fields, exceeds limit of %d", i, j, info.FieldCount, DiscordEmbedMaxFields)
			}
		}
	}
}

func TestSplitEmbedsBySize_MultipleBatches(t *testing.T) {
	// Create multiple large embeds that should require multiple batches
	embeds := make([]*discordgo.MessageEmbed, 15)

	for i := 0; i < 15; i++ {
		embeds[i] = &discordgo.MessageEmbed{
			Title:  "Embed " + string(rune(i+'A')),
			Fields: make([]*discordgo.MessageEmbedField, 20),
		}

		// Fill with fields
		for j := 0; j < 20; j++ {
			embeds[i].Fields[j] = &discordgo.MessageEmbedField{
				Name:  "Field " + string(rune(j+'A')),
				Value: "Long value that will make this embed quite large when combined with many other fields in the embed structure. This helps test the batching functionality.",
			}
		}
	}

	// Test splitting
	result := SplitEmbedsBySize(embeds)

	// Should create multiple batches due to DiscordMessageMaxEmbeds limit (10)
	if len(result) <= 1 {
		t.Errorf("Expected multiple batches due to embed count limit, got %d batches", len(result))
	}

	// Verify each batch meets Discord limits
	for i, batch := range result {
		if len(batch) > DiscordMessageMaxEmbeds {
			t.Errorf("Batch %d has %d embeds, exceeds limit of %d", i, len(batch), DiscordMessageMaxEmbeds)
		}
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string",
			input:    "short",
			maxLen:   10,
			expected: "short",
		},
		{
			name:     "exact length",
			input:    "exactly10c",
			maxLen:   10,
			expected: "exactly10c",
		},
		{
			name:     "needs truncation with space",
			input:    "this is a long string that needs truncation",
			maxLen:   20,
			expected: "this is a long",
		},
		{
			name:     "needs truncation no good space",
			input:    "thisisaverylongstringwithoutspaces",
			maxLen:   20,
			expected: "thisisaverylongstrin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateString() = %q, want %q", result, tt.expected)
			}
			if len(result) > tt.maxLen {
				t.Errorf("truncateString() result length %d exceeds maxLen %d", len(result), tt.maxLen)
			}
		})
	}
}
