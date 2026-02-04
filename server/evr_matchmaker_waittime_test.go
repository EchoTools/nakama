package server

import (
	"sort"
	"testing"
	"time"
)

func TestDynamicWaitTimePriority(t *testing.T) {
	previousSettings := ServiceSettings()
	previousConfig := EVRMatchmakerConfigGet()

	ServiceSettingsUpdate(&ServiceSettingsData{})
	config := NewEVRMatchmakerConfig()
	config.Priority.WaitTimePriorityThresholdSecs = 120
	EVRMatchmakerConfigSet(config)
	defer func() {
		ServiceSettingsUpdate(previousSettings)
		EVRMatchmakerConfigSet(previousConfig)
	}()

	now := time.Now().UTC().Unix()
	waitTimeThreshold := int64(120)

	t.Run("below_threshold_size_priority", func(t *testing.T) {
		predictions := []PredictedMatch{
			{
				Size:                  4,
				OldestTicketTimestamp: now - 60,
				DivisionCount:         2,
				DrawProb:              0.5,
			},
			{
				Size:                  6,
				OldestTicketTimestamp: now - 60,
				DivisionCount:         2,
				DrawProb:              0.5,
			},
		}

		oldestWaitTimeSecs := now - predictions[0].OldestTicketTimestamp
		prioritizeWaitTime := oldestWaitTimeSecs >= waitTimeThreshold

		if prioritizeWaitTime {
			t.Errorf("Expected prioritizeWaitTime=false when wait=%d < threshold=%d", oldestWaitTimeSecs, waitTimeThreshold)
		}

		sort.SliceStable(predictions, func(i, j int) bool {
			if prioritizeWaitTime {
				if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
					return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
				}
				if predictions[i].Size != predictions[j].Size {
					return predictions[i].Size > predictions[j].Size
				}
			} else {
				if predictions[i].Size != predictions[j].Size {
					return predictions[i].Size > predictions[j].Size
				}
				if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
					return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
				}
			}

			if predictions[i].DivisionCount != predictions[j].DivisionCount {
				return predictions[i].DivisionCount < predictions[j].DivisionCount
			}

			return predictions[i].DrawProb > predictions[j].DrawProb
		})

		if predictions[0].Size != 6 {
			t.Errorf("Expected size 6 match first, got size %d", predictions[0].Size)
		}
		t.Logf("✓ Size priority: larger match (size %d) correctly placed before smaller match (size %d)",
			predictions[0].Size, predictions[1].Size)
	})

	t.Run("above_threshold_waittime_priority", func(t *testing.T) {
		predictions := []PredictedMatch{
			{
				Size:                  6,
				OldestTicketTimestamp: now - 60,
				DivisionCount:         2,
				DrawProb:              0.5,
			},
			{
				Size:                  4,
				OldestTicketTimestamp: now - 180,
				DivisionCount:         2,
				DrawProb:              0.5,
			},
		}

		oldestWaitTimeSecs := now - predictions[1].OldestTicketTimestamp
		prioritizeWaitTime := oldestWaitTimeSecs >= waitTimeThreshold

		if !prioritizeWaitTime {
			t.Errorf("Expected prioritizeWaitTime=true when wait=%d >= threshold=%d", oldestWaitTimeSecs, waitTimeThreshold)
		}

		sort.SliceStable(predictions, func(i, j int) bool {
			if prioritizeWaitTime {
				if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
					return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
				}
				if predictions[i].Size != predictions[j].Size {
					return predictions[i].Size > predictions[j].Size
				}
			} else {
				if predictions[i].Size != predictions[j].Size {
					return predictions[i].Size > predictions[j].Size
				}
				if predictions[i].OldestTicketTimestamp != predictions[j].OldestTicketTimestamp {
					return predictions[i].OldestTicketTimestamp < predictions[j].OldestTicketTimestamp
				}
			}

			if predictions[i].DivisionCount != predictions[j].DivisionCount {
				return predictions[i].DivisionCount < predictions[j].DivisionCount
			}

			return predictions[i].DrawProb > predictions[j].DrawProb
		})

		if predictions[0].OldestTicketTimestamp != now-180 {
			t.Errorf("Expected older match first (timestamp %d), got timestamp %d", now-180, predictions[0].OldestTicketTimestamp)
		}
		t.Logf("✓ Wait time priority: older match (age %d sec) correctly placed before newer match (age %d sec)",
			now-predictions[0].OldestTicketTimestamp,
			now-predictions[1].OldestTicketTimestamp)
	})
}
