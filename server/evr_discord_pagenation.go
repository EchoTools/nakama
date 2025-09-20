package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/bwmarrin/discordgo"
)

// discordRESTErrorResponse maps the error structure returned by discordgo.RESTError.
type discordRESTErrorResponse struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
	Errors  struct {
		Data struct {
			Embeds map[string]struct {
				Fields map[string]struct {
					Value struct {
						Errors []struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"_errors"`
					} `json:"value"`
				} `json:"fields"`
			} `json:"embeds"`
		} `json:"data"`
	} `json:"errors"`
}

// splitFieldValue splits a string into chunks of maxLen.
func splitFieldValue(s string, maxLen int) []string {
	if len(s) <= maxLen {
		return []string{s}
	}
	var out []string
	runes := []rune(s)
	for i := 0; i < len(runes); i += maxLen {
		end := min(i+maxLen, len(runes))
		out = append(out, string(runes[i:end]))
	}
	return out
}

// PaginateEmbedOnRESTError parses a discordgo.RESTError, determines required field splits,
// paginates fields, and sends a followup message using the lower-level Discord REST API.
func PaginateInteractionResponseOnRESTError(s *discordgo.Session, interaction *discordgo.Interaction, response *discordgo.InteractionResponse) error {
	if err := s.InteractionRespond(interaction, response); err != nil {
		restErr, ok := err.(*discordgo.RESTError)
		if !ok {
			return fmt.Errorf("error sending interaction response: %w", err)
		}
		return PaginateInteractionOnRESTError(s, interaction, response, restErr)
	}
	return nil
}

func PaginateInteractionOnRESTError(s *discordgo.Session, interaction *discordgo.Interaction, message *discordgo.InteractionResponse, restErr *discordgo.RESTError) error {
	// Parse the REST error response body.
	var apiErr discordRESTErrorResponse
	if err := json.Unmarshal([]byte(restErr.Message.Message), &apiErr); err != nil {
		return fmt.Errorf("error unmarshalling REST error: %w", err)
	}

	// Identify which fields need splitting.
	fieldsToPaginate := map[int]map[int]bool{}
	for embedIdxStr, embedObj := range apiErr.Errors.Data.Embeds {
		embedIdx, _ := strconv.Atoi(embedIdxStr)
		for fieldIdx, fieldObj := range embedObj.Fields {
			for _, fieldErr := range fieldObj.Value.Errors {
				if fieldErr.Code == "BASE_TYPE_MAX_LENGTH" {
					idx, _ := strconv.Atoi(fieldIdx)
					if fieldsToPaginate[embedIdx] == nil {
						fieldsToPaginate[embedIdx] = map[int]bool{}
					}
					fieldsToPaginate[embedIdx][idx] = true
				}
			}
		}
	}
	return PaginateEmbeds(s, interaction, message, fieldsToPaginate)
}
func PaginateEmbeds(s *discordgo.Session, interaction *discordgo.Interaction, message *discordgo.InteractionResponse, embedFieldIdxs map[int]map[int]bool) error {
	const FieldMaxLen = 1024
	const MaxFieldsPerEmbed = 25

	// Use the first embed as a template for the new embeds.
	if len(message.Data.Embeds) == 0 {
		return fmt.Errorf("no embeds to paginate")
	}
	// Prepare new fields with splitting as needed.
	var newEmbeds []*discordgo.MessageEmbed
	for embedIdx := range embedFieldIdxs {
		// Prepare new fields with splitting as needed.
		var paginatedFields []*discordgo.MessageEmbedField
		for _, field := range message.Data.Embeds[embedIdx].Fields {
			// Check if this field needs pagination.
			if len(field.Value) > FieldMaxLen {
				chunks := splitFieldValue(field.Value, FieldMaxLen)
				for _, chunk := range chunks {
					// Create a new field for each chunk.
					paginatedFields = append(paginatedFields, &discordgo.MessageEmbedField{
						Name:   field.Name,
						Value:  chunk,
						Inline: field.Inline,
					})
				}
			} else {
				// No pagination needed, keep the field as is.
				paginatedFields = append(paginatedFields, field)
			}
		}
		// Paginate fields into multiple embeds if necessary.
		for i := 0; i < len(paginatedFields); i += MaxFieldsPerEmbed {
			end := min(i+MaxFieldsPerEmbed, len(paginatedFields))
			newEmbed := &discordgo.MessageEmbed{
				Title:       message.Data.Embeds[embedIdx].Title,
				Description: message.Data.Embeds[embedIdx].Description,
				URL:         message.Data.Embeds[embedIdx].URL,
				Color:       message.Data.Embeds[embedIdx].Color,
				Footer:      message.Data.Embeds[embedIdx].Footer,
				Image:       message.Data.Embeds[embedIdx].Image,
				Thumbnail:   message.Data.Embeds[embedIdx].Thumbnail,
				Video:       message.Data.Embeds[embedIdx].Video,
				Provider:    message.Data.Embeds[embedIdx].Provider,
				Fields:      paginatedFields[i:end],
			}
			newEmbeds = append(newEmbeds, newEmbed)
		}
		if len(newEmbeds) > 0 {
			message.Data.Embeds = newEmbeds
		}
	}

	// Send each embed using the lower-level REST endpoint for followup messages.
	// Discord endpoint: POST /webhooks/{application.id}/{interaction.token}
	// https://discord.com/developers/docs/interactions/receiving-and-responding#followup-messages

	return sendFollowupEmbed(s, interaction, message.Data.Embeds)
}
func PaginateSingleEmbed(embed *discordgo.MessageEmbed, fieldsToPaginate map[int]bool) ([]*discordgo.MessageEmbed, error) {
	const FieldMaxLen = 1024
	const MaxFieldsPerEmbed = 25
	// Prepare new fields with splitting as needed.
	var paginatedFields []*discordgo.MessageEmbedField
	for idx, field := range embed.Fields {
		if fieldsToPaginate[idx] && len(field.Value) > FieldMaxLen {
			chunks := splitFieldValue(field.Value, FieldMaxLen)
			for _, chunk := range chunks {
				paginatedFields = append(paginatedFields, &discordgo.MessageEmbedField{
					Name:   field.Name,
					Value:  chunk,
					Inline: field.Inline,
				})
			}
		} else {
			paginatedFields = append(paginatedFields, field)
		}
	}

	// Paginate fields into multiple embeds if necessary.
	var embeds []*discordgo.MessageEmbed
	for i := 0; i < len(paginatedFields); i += MaxFieldsPerEmbed {
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
		}
		embeds = append(embeds, newEmbed)
	}
	return embeds, nil
}

// sendFollowupEmbed sends an embed using Discord's followup webhook API.
func sendFollowupEmbed(s *discordgo.Session, interaction *discordgo.Interaction, embeds []*discordgo.MessageEmbed) error {
	webhookURL := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s",
		s.State.User.ID, interaction.Token)

	payload := map[string]interface{}{
		"embeds": embeds,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", webhookURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Discordgo provides a custom HTTP client; use it if available.
	client := http.DefaultClient
	if s.Client != nil {
		client = s.Client
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook returned status %d", resp.StatusCode)
	}
	return nil
}
