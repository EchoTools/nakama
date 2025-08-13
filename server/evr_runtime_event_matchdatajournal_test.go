package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEventMatchDataJournal_NewEvent(t *testing.T) {
	matchID := "test-match-id"
	entry := &MatchDataJournalEntry{
		CreatedAt: time.Now().UTC(),
		Data:      "test data",
	}

	event := NewEventMatchDataJournal(matchID, entry)

	assert.NotNil(t, event)
	assert.Equal(t, matchID, event.MatchID)
	assert.Equal(t, entry, event.Entry)
	assert.Nil(t, event.Journal)
}

func TestEventMatchDataJournal_NewPersistEvent(t *testing.T) {
	journal := &MatchDataJournal{
		MatchID:   "test-match-id",
		Events:    []*MatchDataJournalEntry{},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	event := NewEventMatchDataJournalPersist(journal)

	assert.NotNil(t, event)
	assert.Equal(t, journal.MatchID, event.MatchID)
	assert.Equal(t, journal, event.Journal)
	assert.Nil(t, event.Entry)
}

func TestMatchDataJournalEntry_Creation(t *testing.T) {
	entry := &MatchDataJournalEntry{
		CreatedAt: time.Now().UTC(),
		Data:      map[string]interface{}{"test": "data", "value": 123},
	}

	assert.NotNil(t, entry)
	assert.NotZero(t, entry.CreatedAt)
	assert.NotNil(t, entry.Data)
	
	data, ok := entry.Data.(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, "data", data["test"])
	assert.Equal(t, 123, data["value"])
}