package server

import (
	"encoding/json"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
)

func TestUnmarshalEventFactory_CreatesFreshInstances(t *testing.T) {
	d := &EventDispatcher{}
	unmarshal := d.unmarshalEventFactory([]Event{&EventMatchDataJournal{}})

	journalPayload, _ := json.Marshal(&EventMatchDataJournal{
		MatchID: "m1",
		Journal: &MatchDataJournal{MatchID: "m1"},
	})
	entryPayload, _ := json.Marshal(&EventMatchDataJournal{
		MatchID: "m1",
		Entry:   &MatchDataJournalEntry{},
	})

	e1, err := unmarshal(&api.Event{Name: "*server.EventMatchDataJournal", Properties: map[string]string{"payload": string(journalPayload)}})
	if err != nil {
		t.Fatalf("unexpected error unmarshalling first event: %v", err)
	}
	e2, err := unmarshal(&api.Event{Name: "*server.EventMatchDataJournal", Properties: map[string]string{"payload": string(entryPayload)}})
	if err != nil {
		t.Fatalf("unexpected error unmarshalling second event: %v", err)
	}

	m1 := e1.(*EventMatchDataJournal)
	m2 := e2.(*EventMatchDataJournal)

	if m1 == m2 {
		t.Fatal("expected distinct event instances, got same pointer")
	}
	if m2.Journal != nil {
		t.Fatal("expected second event Journal to be nil, got stale Journal from previous event")
	}
	if m2.Entry == nil {
		t.Fatal("expected second event Entry to be set")
	}
}

func TestUnmarshalEventFactory_HandlesMatchSummary(t *testing.T) {
	d := &EventDispatcher{}
	unmarshal := d.unmarshalEventFactory([]Event{&EventMatchSummary{}})

	payload, _ := json.Marshal(&EventMatchSummary{
		Match: MatchSummaryState{MatchID: "match-1", MatchOver: true},
	})

	e, err := unmarshal(&api.Event{Name: "*server.EventMatchSummary", Properties: map[string]string{"payload": string(payload)}})
	if err != nil {
		t.Fatalf("unexpected error unmarshalling match summary: %v", err)
	}

	ms, ok := e.(*EventMatchSummary)
	if !ok {
		t.Fatalf("expected *EventMatchSummary, got %T", e)
	}
	if ms.Match.MatchID != "match-1" {
		t.Fatalf("expected match id match-1, got %s", ms.Match.MatchID)
	}
}
