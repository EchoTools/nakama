package server

import (
	"github.com/bwmarrin/discordgo"
)

// splitFieldValue splits a string into chunks of maxLen, preferring to split on newline boundaries.
func splitFieldValue(s string, maxLen int) []string {
	if len(s) <= maxLen {
		return []string{s}
	}

	var out []string
	runes := []rune(s)

	for i := 0; i < len(runes); {
		end := min(i+maxLen, len(runes))

		// If not at the end of the string, try to find a newline to split on
		if end < len(runes) {
			// Look backwards from the end position for a newline
			for j := end - 1; j > i; j-- {
				if runes[j] == '\n' {
					end = j + 1 // Include the newline in the current chunk
					break
				}
			}
		}

		out = append(out, string(runes[i:end]))
		i = end
	}

	return out
}

func PaginateEmbeds(embeds []*discordgo.MessageEmbed, maxFields, maxFieldLength int) []*discordgo.MessageEmbed {

	// Use the first embed as a template for the new embeds.
	// Prepare new fields with splitting as needed.
	var newEmbeds []*discordgo.MessageEmbed
	for embedIdx, embed := range embeds {
		// Prepare new fields with splitting as needed.
		var paginatedFields []*discordgo.MessageEmbedField
		for _, field := range embeds[embedIdx].Fields {
			// Check if this field needs pagination.
			if len(field.Value) > maxFieldLength {
				chunks := splitFieldValue(field.Value, maxFieldLength)
				for i, chunk := range chunks {
					name := field.Name
					if i > 0 {
						name = name + " (cont.)"
					}
					// Create a new field for each chunk.
					paginatedFields = append(paginatedFields, &discordgo.MessageEmbedField{
						Name:   name,
						Value:  chunk,
						Inline: false,
					})
				}
			} else {
				// No pagination needed, keep the field as is.
				paginatedFields = append(paginatedFields, field)
			}
		}
		// Paginate fields into multiple embeds if necessary.
		for i := 0; i < len(paginatedFields); i += maxFields {
			end := min(i+maxFields, len(paginatedFields))
			newEmbed := &discordgo.MessageEmbed{
				Title:       embed.Title,
				Description: embed.Description,
				URL:         embed.URL,
				Color:       embed.Color,
				Footer:      embed.Footer,
				Image:       embed.Image,
				Thumbnail:   embed.Thumbnail,
				Video:       embed.Video,
				Provider:    embed.Provider,
				Fields:      paginatedFields[i:end],
			}
			newEmbeds = append(newEmbeds, newEmbed)
		}
	}
	return newEmbeds
}
