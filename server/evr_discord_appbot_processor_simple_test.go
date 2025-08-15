package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
)

// Tests for core functionality without complex dependencies

func TestModernCommandProcessor_CanHandleCommand(t *testing.T) {
	processor := NewModernCommandProcessor()
	
	// Test supported commands
	assert.True(t, processor.CanHandleCommand("throw-settings"))
	assert.True(t, processor.CanHandleCommand("whoami"))
	
	// Test unsupported commands
	assert.False(t, processor.CanHandleCommand("unknown-command"))
	assert.False(t, processor.CanHandleCommand(""))
}

func TestExtractOptions(t *testing.T) {
	options := []CommandDataOption{
		{Name: "string_opt", Type: 3, Value: "test_value"},
		{Name: "int_opt", Type: 4, Value: int64(42)},
		{Name: "bool_opt", Type: 5, Value: true},
		{Name: "float_opt", Type: 10, Value: 3.14},
	}
	
	// Test string extraction
	assert.Equal(t, "test_value", extractStringOption(options, "string_opt"))
	assert.Equal(t, "", extractStringOption(options, "nonexistent"))
	
	// Test int extraction
	assert.Equal(t, int64(42), extractIntOption(options, "int_opt"))
	assert.Equal(t, int64(3), extractIntOption(options, "float_opt")) // float should convert to int
	assert.Equal(t, int64(0), extractIntOption(options, "nonexistent"))
	
	// Test bool extraction
	assert.Equal(t, true, extractBoolOption(options, "bool_opt"))
	assert.Equal(t, false, extractBoolOption(options, "nonexistent"))
}

func TestConvertOptions(t *testing.T) {
	// Test nil options
	result := convertOptions(nil)
	assert.Nil(t, result)
	
	// Test empty options 
	result = convertOptions([]*discordgo.ApplicationCommandInteractionDataOption{})
	assert.NotNil(t, result)
	assert.Len(t, result, 0)
	
	// Test with actual options
	discordOptions := []*discordgo.ApplicationCommandInteractionDataOption{
		{
			Name:  "test_option",
			Type:  discordgo.ApplicationCommandOptionString,
			Value: "test_value",
		},
	}
	
	result = convertOptions(discordOptions)
	assert.NotNil(t, result)
	assert.Len(t, result, 1)
	assert.Equal(t, "test_option", result[0].Name)
	assert.Equal(t, int(discordgo.ApplicationCommandOptionString), result[0].Type)
	assert.Equal(t, "test_value", result[0].Value)
}