package server

import (
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Discord API limits
const (
	// Maximum size of an entire embed in characters
	DiscordEmbedMaxSize = 6000
	// Maximum number of fields per embed
	DiscordEmbedMaxFields = 25
	// Maximum number of embeds per message
	DiscordMessageMaxEmbeds = 10
	// Maximum field value length
	DiscordFieldMaxValue = 1024
	// Maximum field name length
	DiscordFieldMaxName = 256
	// Maximum title length
	DiscordEmbedMaxTitle = 256
	// Maximum description length
	DiscordEmbedMaxDescription = 4096
	// Maximum footer text length
	DiscordEmbedMaxFooter = 2048
	// Maximum author name length
	DiscordEmbedMaxAuthor = 256
)

// EmbedSizeInfo holds size information about an embed
type EmbedSizeInfo struct {
	TotalSize     int
	FieldCount    int
	Title         int
	Description   int
	Footer        int
	Author        int
	FieldSizes    []int
	ExceedsLimits bool
}

// CalculateEmbedSize returns the approximate character count of a Discord embed
func CalculateEmbedSize(embed *discordgo.MessageEmbed) *EmbedSizeInfo {
	if embed == nil {
		return &EmbedSizeInfo{}
	}

	info := &EmbedSizeInfo{
		FieldSizes: make([]int, len(embed.Fields)),
	}

	// Calculate title size
	if embed.Title != "" {
		info.Title = len(embed.Title)
		info.TotalSize += info.Title
	}

	// Calculate description size
	if embed.Description != "" {
		info.Description = len(embed.Description)
		info.TotalSize += info.Description
	}

	// Calculate footer size
	if embed.Footer != nil && embed.Footer.Text != "" {
		info.Footer = len(embed.Footer.Text)
		info.TotalSize += info.Footer
	}

	// Calculate author size
	if embed.Author != nil && embed.Author.Name != "" {
		info.Author = len(embed.Author.Name)
		info.TotalSize += info.Author
	}

	// Calculate fields size
	info.FieldCount = len(embed.Fields)
	for i, field := range embed.Fields {
		if field == nil {
			continue
		}
		fieldSize := len(field.Name) + len(field.Value)
		info.FieldSizes[i] = fieldSize
		info.TotalSize += fieldSize
	}

	// Check if embed exceeds limits
	info.ExceedsLimits = info.TotalSize > DiscordEmbedMaxSize ||
		info.FieldCount > DiscordEmbedMaxFields ||
		info.Title > DiscordEmbedMaxTitle ||
		info.Description > DiscordEmbedMaxDescription ||
		info.Footer > DiscordEmbedMaxFooter ||
		info.Author > DiscordEmbedMaxAuthor

	// Check individual field limits
	for _, field := range embed.Fields {
		if field == nil {
			continue
		}
		if len(field.Name) > DiscordFieldMaxName || len(field.Value) > DiscordFieldMaxValue {
			info.ExceedsLimits = true
			break
		}
	}

	return info
}

// SplitEmbedsBySize splits a slice of embeds into multiple messages based on Discord limits
func SplitEmbedsBySize(embeds []*discordgo.MessageEmbed) [][]*discordgo.MessageEmbed {
	if len(embeds) == 0 {
		return nil
	}

	var result [][]*discordgo.MessageEmbed
	var currentBatch []*discordgo.MessageEmbed
	var currentBatchSize int

	for _, embed := range embeds {
		if embed == nil {
			continue
		}

		info := CalculateEmbedSize(embed)
		
		// If a single embed exceeds limits, try to split its fields
		if info.ExceedsLimits {
			if splitEmbeds := splitLargeEmbed(embed); len(splitEmbeds) > 0 {
				// Process each split embed
				for _, splitEmbed := range splitEmbeds {
					splitInfo := CalculateEmbedSize(splitEmbed)
					
					// Check if we can add this split embed to current batch
					if len(currentBatch) > 0 && 
						(len(currentBatch)+1 > DiscordMessageMaxEmbeds || 
						 currentBatchSize+splitInfo.TotalSize > DiscordEmbedMaxSize*len(currentBatch)) {
						
						// Start new batch
						result = append(result, currentBatch)
						currentBatch = []*discordgo.MessageEmbed{splitEmbed}
						currentBatchSize = splitInfo.TotalSize
					} else {
						// Add to current batch
						currentBatch = append(currentBatch, splitEmbed)
						currentBatchSize += splitInfo.TotalSize
					}
				}
				continue
			}
		}

		// Check if we can add this embed to current batch
		if len(currentBatch) > 0 && 
			(len(currentBatch)+1 > DiscordMessageMaxEmbeds || 
			 currentBatchSize+info.TotalSize > DiscordEmbedMaxSize*len(currentBatch)) {
			
			// Start new batch
			result = append(result, currentBatch)
			currentBatch = []*discordgo.MessageEmbed{embed}
			currentBatchSize = info.TotalSize
		} else {
			// Add to current batch
			currentBatch = append(currentBatch, embed)
			currentBatchSize += info.TotalSize
		}
	}

	// Add final batch if not empty
	if len(currentBatch) > 0 {
		result = append(result, currentBatch)
	}

	return result
}

// splitLargeEmbed attempts to split a large embed into smaller ones by breaking up fields
func splitLargeEmbed(embed *discordgo.MessageEmbed) []*discordgo.MessageEmbed {
	if embed == nil || len(embed.Fields) == 0 {
		return []*discordgo.MessageEmbed{embed}
	}

	var result []*discordgo.MessageEmbed
	
	// Create base embed structure (without fields)
	baseEmbed := &discordgo.MessageEmbed{
		Title:       embed.Title,
		Description: embed.Description,
		Color:       embed.Color,
		Footer:      embed.Footer,
		Author:      embed.Author,
		Thumbnail:   embed.Thumbnail,
		Image:       embed.Image,
		Timestamp:   embed.Timestamp,
		URL:         embed.URL,
	}

	// Calculate base size without fields
	baseInfo := CalculateEmbedSize(baseEmbed)
	
	// If base embed itself exceeds limits, we need to truncate
	if baseInfo.ExceedsLimits {
		// Truncate description if too long
		if baseInfo.Description > DiscordEmbedMaxDescription {
			baseEmbed.Description = truncateString(baseEmbed.Description, DiscordEmbedMaxDescription-100) + "... (truncated)"
		}
		// Truncate title if too long
		if baseInfo.Title > DiscordEmbedMaxTitle {
			baseEmbed.Title = truncateString(baseEmbed.Title, DiscordEmbedMaxTitle-10) + "..."
		}
		baseInfo = CalculateEmbedSize(baseEmbed)
	}

	// Split fields across multiple embeds
	currentEmbed := *baseEmbed // Copy base embed
	currentEmbed.Fields = nil
	currentSize := baseInfo.TotalSize
	fieldCount := 0

	for _, field := range embed.Fields {
		if field == nil {
			continue
		}

		// Truncate field value if too long
		fieldValue := field.Value
		if len(fieldValue) > DiscordFieldMaxValue {
			fieldValue = truncateString(fieldValue, DiscordFieldMaxValue-100) + "... (truncated)"
		}

		// Truncate field name if too long
		fieldName := field.Name
		if len(fieldName) > DiscordFieldMaxName {
			fieldName = truncateString(fieldName, DiscordFieldMaxName-10) + "..."
		}

		fieldSize := len(fieldName) + len(fieldValue)

		// Check if adding this field would exceed limits
		if fieldCount > 0 && 
			(fieldCount+1 > DiscordEmbedMaxFields || 
			 currentSize+fieldSize > DiscordEmbedMaxSize) {
			
			// Finalize current embed and start a new one
			result = append(result, &currentEmbed)
			
			// Start new embed
			currentEmbed = *baseEmbed // Copy base embed
			currentEmbed.Fields = nil
			currentSize = baseInfo.TotalSize
			fieldCount = 0
			
			// Add continuation indicator to title if this is not the first split
			if len(result) == 1 {
				if currentEmbed.Title != "" {
					currentEmbed.Title += " (continued)"
				} else {
					currentEmbed.Title = "Profile Information (continued)"
				}
			} else {
				if currentEmbed.Title != "" {
					currentEmbed.Title += " (cont.)"
				} else {
					currentEmbed.Title = "Profile Information (cont.)"
				}
			}
		}

		// Add field to current embed
		currentEmbed.Fields = append(currentEmbed.Fields, &discordgo.MessageEmbedField{
			Name:   fieldName,
			Value:  fieldValue,
			Inline: field.Inline,
		})
		currentSize += fieldSize
		fieldCount++
	}

	// Add final embed if it has fields
	if len(currentEmbed.Fields) > 0 {
		result = append(result, &currentEmbed)
	}

	// If no splitting was needed, return original
	if len(result) == 0 {
		return []*discordgo.MessageEmbed{embed}
	}

	return result
}

// truncateString truncates a string to a maximum length at word boundaries when possible
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	// Find last space before maxLen
	truncated := s[:maxLen]
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxLen/2 {
		return s[:lastSpace]
	}

	// If no good space found, just truncate at maxLen
	return truncated
}

// CalculateMessageSize estimates the total size of a message with embeds
func CalculateMessageSize(content string, embeds []*discordgo.MessageEmbed) int {
	size := len(content)
	for _, embed := range embeds {
		info := CalculateEmbedSize(embed)
		size += info.TotalSize
	}
	return size
}

// ValidateEmbeds checks if embeds meet Discord limits
func ValidateEmbeds(embeds []*discordgo.MessageEmbed) error {
	if len(embeds) > DiscordMessageMaxEmbeds {
		return ErrTooManyEmbeds
	}

	for i, embed := range embeds {
		if embed == nil {
			continue
		}
		
		info := CalculateEmbedSize(embed)
		if info.ExceedsLimits {
			return ErrEmbedTooLarge.WithIndex(i)
		}
	}

	return nil
}

// Custom errors
type EmbedError struct {
	Message string
	Index   int
}

func (e EmbedError) Error() string {
	if e.Index >= 0 {
		return e.Message + " (embed " + strconv.Itoa(e.Index) + ")"
	}
	return e.Message
}

func (e EmbedError) WithIndex(index int) EmbedError {
	e.Index = index
	return e
}

var (
	ErrTooManyEmbeds = EmbedError{Message: "too many embeds in message", Index: -1}
	ErrEmbedTooLarge = EmbedError{Message: "embed exceeds size limits", Index: -1}
)