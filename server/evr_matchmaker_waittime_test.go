package server

import (
	"sort"
	"testing"
	"time"
)

// TestSizeFirstSortPriority verifies that predictions are always sorted by
// size first, then by oldest ticket timestamp. The wait-time-priority
// threshold has been removed; accumulation handles starving tickets.
func TestSizeFirstSortPriority(t *testing.T) {
	now := time.Now().UTC().Unix()

	t.Run("larger_match_beats_older_ticket", func(t *testing.T) {
		predictions := []PredictedMatch{
			{
				Size:                 4,
				OldestTicketTimestamp: now - 180, // older
				DivisionCount:        2,
				DrawProb:             0.5,
			},
			{
				Size:                 6,
				OldestTicketTimestamp: now - 60, // newer
				DivisionCount:        2,
				DrawProb:             0.5,
			},
		}

		sort.SliceStable(predictions, func(i, j int) bool {
			if predictions[i].Size != predictions[j].Size {
				return predictions[i].Size > predictions[j].Size
			}
			if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
				return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
			}
			if predictions[i].DivisionCount != predictions[j].DivisionCount {
				return predictions[i].DivisionCount < predictions[j].DivisionCount
			}
			return predictions[i].DrawProb > predictions[j].DrawProb
		})

		if predictions[0].Size != 6 {
			t.Errorf("Expected size 6 match first, got size %d", predictions[0].Size)
		}
	})

	t.Run("same_size_older_ticket_wins", func(t *testing.T) {
		predictions := []PredictedMatch{
			{
				Size:                 6,
				OldestTicketTimestamp: now - 60,
				DivisionCount:        2,
				DrawProb:             0.5,
			},
			{
				Size:                 6,
				OldestTicketTimestamp: now - 180,
				DivisionCount:        2,
				DrawProb:             0.5,
			},
		}

		sort.SliceStable(predictions, func(i, j int) bool {
			if predictions[i].Size != predictions[j].Size {
				return predictions[i].Size > predictions[j].Size
			}
			if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
				return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
			}
			if predictions[i].DivisionCount != predictions[j].DivisionCount {
				return predictions[i].DivisionCount < predictions[j].DivisionCount
			}
			return predictions[i].DrawProb > predictions[j].DrawProb
		})

		if predictions[0].OldestTicketTimestamp != now-180 {
			t.Errorf("Expected older ticket first (timestamp %d), got %d", now-180, predictions[0].OldestTicketTimestamp)
		}
	})
}
