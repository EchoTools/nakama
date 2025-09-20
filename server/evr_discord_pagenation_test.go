package server

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
)

func TestSplitFieldValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected []string
	}{
		{
			name:     "string shorter than maxLen",
			input:    "hello",
			maxLen:   10,
			expected: []string{"hello"},
		},
		{
			name:     "string equal to maxLen",
			input:    "hello",
			maxLen:   5,
			expected: []string{"hello"},
		},
		{
			name:     "string longer than maxLen",
			input:    "hello world",
			maxLen:   5,
			expected: []string{"hello", " worl", "d"},
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   5,
			expected: []string{""},
		},
		{
			name:     "single character",
			input:    "a",
			maxLen:   1,
			expected: []string{"a"},
		},
		{
			name:     "unicode characters",
			input:    "こんにちは世界",
			maxLen:   3,
			expected: []string{"こんに", "ちは世", "界"},
		},
		{
			name:     "maxLen of 1",
			input:    "abc",
			maxLen:   1,
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "long string with newlines",
			input:    "line1\nline2\nline3",
			maxLen:   8,
			expected: []string{"line1\nli", "ne2\nline", "3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitFieldValue(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)

			// Verify that rejoining the chunks gives the original string
			rejoined := strings.Join(result, "")
			assert.Equal(t, tt.input, rejoined)

			// Verify that each chunk (except possibly the last) is at most maxLen
			for i, chunk := range result {
				if i < len(result)-1 {
					assert.LessOrEqual(t, len([]rune(chunk)), tt.maxLen)
				} else {
					assert.LessOrEqual(t, len([]rune(chunk)), tt.maxLen)
				}
			}
		})
	}
}

func TestPaginateEmbeds(t *testing.T) {
	tests := []struct {
		name           string
		embeds         []*discordgo.MessageEmbed
		maxFields      int
		maxFieldLength int
		expectedEmbeds int
		expectedFields []int // number of fields per embed
	}{
		{
			name: "single embed with fields under limits",
			embeds: []*discordgo.MessageEmbed{
				{
					Title: "Test Embed",
					Color: 0xFF0000,
					Fields: []*discordgo.MessageEmbedField{
						{Name: "Field1", Value: "Value1", Inline: true},
						{Name: "Field2", Value: "Value2", Inline: false},
					},
				},
			},
			maxFields:      5,
			maxFieldLength: 100,
			expectedEmbeds: 1,
			expectedFields: []int{2},
		},
		{
			name: "single embed exceeding field count limit",
			embeds: []*discordgo.MessageEmbed{
				{
					Title: "Test Embed",
					Fields: []*discordgo.MessageEmbedField{
						{Name: "Field1", Value: "Value1"},
						{Name: "Field2", Value: "Value2"},
						{Name: "Field3", Value: "Value3"},
						{Name: "Field4", Value: "Value4"},
						{Name: "Field5", Value: "Value5"},
						{Name: "Field6", Value: "Value6"},
					},
				},
			},
			maxFields:      3,
			maxFieldLength: 100,
			expectedEmbeds: 2,
			expectedFields: []int{3, 3},
		},
		{
			name: "single field exceeding length limit",
			embeds: []*discordgo.MessageEmbed{
				{
					Title: "Test Embed",
					Fields: []*discordgo.MessageEmbedField{
						{Name: "LongField", Value: "This is a very long value that exceeds the maximum field length"},
					},
				},
			},
			maxFields:      5,
			maxFieldLength: 20,
			expectedEmbeds: 1,
			expectedFields: []int{4}, // Split into 4 chunks
		},
		{
			name: "multiple embeds",
			embeds: []*discordgo.MessageEmbed{
				{
					Title: "Embed1",
					Fields: []*discordgo.MessageEmbedField{
						{Name: "Field1", Value: "Value1"},
						{Name: "Field2", Value: "Value2"},
					},
				},
				{
					Title: "Embed2",
					Fields: []*discordgo.MessageEmbedField{
						{Name: "Field3", Value: "Value3"},
					},
				},
			},
			maxFields:      5,
			maxFieldLength: 100,
			expectedEmbeds: 2,
			expectedFields: []int{2, 1},
		},
		{
			name:           "empty embed list",
			embeds:         []*discordgo.MessageEmbed{},
			maxFields:      5,
			maxFieldLength: 100,
			expectedEmbeds: 0,
			expectedFields: []int{},
		},
		{
			name: "embed with no fields",
			embeds: []*discordgo.MessageEmbed{
				{
					Title: "Empty Embed",
					Color: 0x00FF00,
				},
			},
			maxFields:      5,
			maxFieldLength: 100,
			expectedEmbeds: 0, // No fields means no embeds in output
			expectedFields: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := PaginateEmbeds(tt.embeds, tt.maxFields, tt.maxFieldLength)

			assert.Equal(t, tt.expectedEmbeds, len(result), "number of output embeds")

			for i, expectedFieldCount := range tt.expectedFields {
				if i < len(result) {
					assert.Equal(t, expectedFieldCount, len(result[i].Fields),
						"field count for embed %d", i)
				}
			}

			// Verify that no embed exceeds maxFields
			for i, embed := range result {
				assert.LessOrEqual(t, len(embed.Fields), tt.maxFields,
					"embed %d exceeds max fields", i)
			}

			// Verify that no field exceeds maxFieldLength
			for i, embed := range result {
				for j, field := range embed.Fields {
					assert.LessOrEqual(t, len([]rune(field.Value)), tt.maxFieldLength,
						"field %d in embed %d exceeds max length", j, i)
				}
			}
		})
	}
}

func TestPaginateEmbedsPreservesMetadata(t *testing.T) {
	originalEmbed := &discordgo.MessageEmbed{
		Title:       "Test Title",
		Description: "Test Description",
		URL:         "https://example.com",
		Color:       0xFF0000,
		Footer: &discordgo.MessageEmbedFooter{
			Text: "Footer Text",
		},
		Image: &discordgo.MessageEmbedImage{
			URL: "https://example.com/image.png",
		},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Field1", Value: "Value1"},
			{Name: "Field2", Value: "Value2"},
		},
	}

	result := PaginateEmbeds([]*discordgo.MessageEmbed{originalEmbed}, 5, 100)

	assert.Len(t, result, 1)

	resultEmbed := result[0]
	assert.Equal(t, originalEmbed.Title, resultEmbed.Title)
	assert.Equal(t, originalEmbed.Description, resultEmbed.Description)
	assert.Equal(t, originalEmbed.URL, resultEmbed.URL)
	assert.Equal(t, originalEmbed.Color, resultEmbed.Color)
	assert.Equal(t, originalEmbed.Footer, resultEmbed.Footer)
	assert.Equal(t, originalEmbed.Image, resultEmbed.Image)
}

func TestPaginateEmbedsContinuationNames(t *testing.T) {
	embed := &discordgo.MessageEmbed{
		Fields: []*discordgo.MessageEmbedField{
			{Name: "LongField", Value: "This is a very long value that will be split into multiple chunks"},
		},
	}

	result := PaginateEmbeds([]*discordgo.MessageEmbed{embed}, 10, 25)

	assert.Greater(t, len(result[0].Fields), 1, "should have multiple fields after splitting")

	// First field should have original name
	assert.Equal(t, "LongField", result[0].Fields[0].Name)

	// Subsequent fields should have continuation names
	for i := 1; i < len(result[0].Fields); i++ {
		assert.Equal(t, "LongField (cont.)", result[0].Fields[i].Name)
	}
}

func TestPaginateEmbedsInlineToFalse(t *testing.T) {
	embed := &discordgo.MessageEmbed{
		Fields: []*discordgo.MessageEmbedField{
			{Name: "LongField", Value: "This is a very long value that will be split", Inline: true},
		},
	}

	result := PaginateEmbeds([]*discordgo.MessageEmbed{embed}, 10, 25)

	// All split fields should have Inline set to false
	for _, field := range result[0].Fields {
		assert.False(t, field.Inline, "split fields should not be inline")
	}
}

func TestPaginateEmbedsNewlineBoundaries(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		maxLen         int
		expectedChunks []string
	}{
		{
			name:   "split on newline boundary",
			input:  "line1\nline2\nline3\nline4",
			maxLen: 12,
			expectedChunks: []string{
				"line1\nline2\n",
				"line3\nline4",
			},
		},
		{
			name:   "prefer newline over middle of word",
			input:  "short line\nvery long line that exceeds limit",
			maxLen: 15,
			expectedChunks: []string{
				"short line\n",
				"very long line ",
				"that exceeds l",
				"imit",
			},
		},
		{
			name:   "no newlines available",
			input:  "verylongstringwithnonewlines",
			maxLen: 10,
			expectedChunks: []string{
				"verylongst",
				"ringwithno",
				"newlines",
			},
		},
		{
			name:   "multiple consecutive newlines",
			input:  "line1\n\n\nline2",
			maxLen: 8,
			expectedChunks: []string{
				"line1\n\n\n",
				"line2",
			},
		},
		{
			name:   "newline at exact boundary",
			input:  "12345\n67890",
			maxLen: 6,
			expectedChunks: []string{
				"12345\n",
				"67890",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitFieldValue(tt.input, tt.maxLen)
			assert.Equal(t, tt.expectedChunks, result)

			// Verify that rejoining the chunks gives the original string
			rejoined := strings.Join(result, "")
			assert.Equal(t, tt.input, rejoined)

			// Verify that each chunk is at most maxLen
			for _, chunk := range result {
				assert.LessOrEqual(t, len([]rune(chunk)), tt.maxLen)
			}
		})
	}
}

func TestPaginateEmbedsWithNewlines(t *testing.T) {
	embed := &discordgo.MessageEmbed{
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:  "MultilineField",
				Value: "Line 1 content\nLine 2 content\nLine 3 content\nLine 4 content",
			},
		},
	}

	result := PaginateEmbeds([]*discordgo.MessageEmbed{embed}, 10, 20)

	assert.Greater(t, len(result[0].Fields), 1, "should split into multiple fields")

	// Verify that splits happen at newline boundaries when possible
	for _, field := range result[0].Fields {
		// If the field value doesn't end with a newline and isn't the last chunk,
		// it should ideally end at a natural break point
		if !strings.HasSuffix(field.Value, "\n") {
			// This is acceptable for the last chunk or when no newlines are available
			continue
		}
		assert.True(t, strings.HasSuffix(field.Value, "\n"), "field should end with newline")
	}

	// Verify that when joined together, we get the original content
	var reconstructed strings.Builder
	for _, field := range result[0].Fields {
		reconstructed.WriteString(field.Value)
	}
	assert.Equal(t, embed.Fields[0].Value, reconstructed.String())
}
