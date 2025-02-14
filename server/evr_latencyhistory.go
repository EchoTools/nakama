package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type LabelWithLatency struct {
	Label *MatchLabel
	RTT   int
}

func (l LabelWithLatency) AsDuration() time.Duration {
	return time.Duration(l.RTT) * time.Millisecond
}

// map[externalIP]map[timestamp]latency
type LatencyHistory map[string]map[int64]int // map[externalIP]map[timestamp]latency

func NewLatencyHistory() LatencyHistory {
	return make(LatencyHistory)
}

func (h LatencyHistory) String() string {
	data, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(data)
}

func (h LatencyHistory) Add(extIP string, rtt int) {
	if _, ok := h[extIP]; !ok {
		h[extIP] = make(map[int64]int)
	}
	h[extIP][time.Now().UTC().Unix()] = rtt
}

func (h LatencyHistory) LatestRTTs() map[string]int {
	latestRTTs := make(map[string]int)
	for extIP, history := range h {
		latestRTTs[extIP] = 0
		latestTS := int64(0)
		for ts, rtt := range history {
			if rtt == 0 || rtt == 999 {
				rtt = 999
			}
			if ts > latestTS {
				latestTS = ts
				latestRTTs[extIP] = rtt
			}
		}
	}
	return latestRTTs
}

// Return the average rtt for a single external IP
func (h LatencyHistory) AverageRTT(extIP string, roundRTT bool) int {
	if history, ok := h[extIP]; ok {
		if len(history) == 0 {
			return 999
		}
		average := 0
		for _, l := range history {
			average += l
		}
		average /= len(history)

		if roundRTT {
			average = (average + 5) / 10 * 10
		}

		return average
	}
	return 999
}

// RTTs by external IP
func (h LatencyHistory) AverageRTTs(roundRTTs, includeUnreachable bool) map[string]int {
	rttByIP := make(map[string][]int)
	averageRTTs := make(map[string]int)

	for extIP, history := range h {

		for _, rtt := range history {
			if rtt == 0 || rtt == 999 {
				if !includeUnreachable {
					continue
				}
				rtt = 999
			}

			rttByIP[extIP] = append(rttByIP[extIP], rtt)
		}
	}

	for extIP, rtt := range rttByIP {
		if len(rtt) == 0 {
			if !includeUnreachable {
				continue
			}
			averageRTTs[extIP] = 999
			continue
		}
		average := 0
		for _, l := range rtt {
			average += l
		}
		average /= len(rtt)

		if roundRTTs {
			average = (average + 5) / 10 * 10
		}

		averageRTTs[extIP] = average
	}

	return averageRTTs
}

func (h LatencyHistory) LabelsByAverageRTT(labels []*MatchLabel) []LabelWithLatency {
	if len(labels) == 0 {
		return make([]LabelWithLatency, 0)
	}

	labelRTTs := make([]LabelWithLatency, 0, len(labels))
	for _, label := range labels {
		if history, ok := h[label.GameServer.Endpoint.GetExternalIP()]; ok {
			if len(history) == 0 {
				labelRTTs = append(labelRTTs, LabelWithLatency{Label: label, RTT: 999})
				continue
			}
			average := 0
			for _, l := range history {
				average += l
			}
			average /= len(history)
			labelRTTs = append(labelRTTs, LabelWithLatency{Label: label, RTT: average})
		} else {
			labelRTTs = append(labelRTTs, LabelWithLatency{Label: label, RTT: 999})
		}
	}

	slices.SortStableFunc(labelRTTs, func(a, b LabelWithLatency) int {
		if a.RTT == 0 || a.RTT > 250 {
			return 999
		}
		return a.RTT - b.RTT
	})
	return labelRTTs
}

// map[externalIP]map[timestamp]latency
func LoadLatencyHistory(ctx context.Context, logger *zap.Logger, db *sql.DB, userID uuid.UUID) (LatencyHistory, error) {

	result, err := StorageReadObjects(ctx, logger, db, userID, []*api.ReadStorageObjectId{
		{
			Collection: LatencyHistoryStorageCollection,
			Key:        LatencyHistoryStorageKey,
			UserId:     userID.String(),
		},
	})
	if err != nil {
		return nil, err
	}
	objs := result.GetObjects()
	if len(objs) == 0 {
		return make(LatencyHistory), nil
	}
	var latencyHistory LatencyHistory
	if err := json.Unmarshal([]byte(objs[0].GetValue()), &latencyHistory); err != nil {
		return nil, err
	}

	return latencyHistory, nil
}

func StoreLatencyHistory(ctx context.Context, logger *zap.Logger, db *sql.DB, metrics Metrics, storageIndex StorageIndex, userID uuid.UUID, latencyHistory LatencyHistory) error {
	// Delete any records older than 1 week
	twoWeeksAgo := time.Now().AddDate(0, 0, -7).Unix()
	for _, history := range latencyHistory {
		for ts := range history {
			if ts < twoWeeksAgo {
				delete(history, ts)
			}
		}
	}

	for ip, history := range latencyHistory {
		if len(history) == 0 {
			delete(latencyHistory, ip)
		}
	}

	ops := StorageOpWrites{&StorageOpWrite{
		OwnerID: userID.String(),
		Object: &api.WriteStorageObject{
			Collection:      LatencyHistoryStorageCollection,
			Key:             LatencyHistoryStorageKey,
			Value:           latencyHistory.String(),
			PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
			PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
		},
	}}
	if _, _, err := StorageWriteObjects(ctx, logger, db, metrics, storageIndex, true, ops); err != nil {
		return err
	}
	return nil
}
