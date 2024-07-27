package server

import (
	"slices"
	"time"
)

type LatencyHistoryData struct {
	History []map[string]time.Duration // []map[externalIP]rtt
}

func (l *LatencyHistoryData) AddRecord(record map[string]time.Duration, limit int) {
	if l.History == nil {
		l.History = make([]map[string]time.Duration, 0)
	}

	l.History = append(l.History, record)
	if len(l.History) > limit {
		l.History = l.History[1:]
	}
}

func (l *LatencyHistoryData) GetAveragesByIP(ips []string) []time.Duration {

	rtts := make(map[string][]time.Duration, 0)

	for _, record := range l.History {
		for ip, rtt := range record {
			if _, ok := rtts[ip]; !ok {
				rtts[ip] = make([]time.Duration, 0)
			}

			rtts[ip] = append(rtts[ip], rtt)
		}
	}

	averages := make([]time.Duration, 0)
	for _, ip := range ips {
		if rtts, ok := rtts[ip]; ok {
			var sum time.Duration
			for _, rtt := range rtts {
				sum += rtt
			}

			averages = append(averages, sum/time.Duration(len(rtts)))
		}
	}

	return averages
}

func (l *LatencyHistoryData) SortByAverageRTT(ips []string) []string {
	rtts := make(map[string][]time.Duration, 0)

	for _, record := range l.History {
		for ip, rtt := range record {
			if _, ok := rtts[ip]; !ok {
				rtts[ip] = make([]time.Duration, 0)
			}

			rtts[ip] = append(rtts[ip], rtt)
		}
	}
	averages := make(map[string]time.Duration, 0)
	for ip, rtt := range rtts {
		var sum time.Duration
		for _, rtt := range rtt {
			sum += rtt
		}
		averages[ip] = sum / time.Duration(len(rtt))
	}

	slices.SortStableFunc(ips, func(a, b string) int {
		return int(averages[a] - averages[b])
	})

	return ips
}

func (l *LatencyHistoryData) GetLeastFrequent() []string {
	frequencies := make(map[string]int, 0)

	for _, record := range l.History {
		for ip := range record {
			if _, ok := frequencies[ip]; !ok {
				frequencies[ip] = 0
			}

			frequencies[ip]++
		}
	}

	leastFrequent := make([]string, 0)
	min := 0
	for ip, frequency := range frequencies {
		if frequency < min {
			min = frequency
			leastFrequent = []string{ip}
		} else if frequency == min {
			leastFrequent = append(leastFrequent, ip)
		}
	}

	return leastFrequent

}

func (l *LatencyHistoryData) SortLeastFrequentIP(ips []string) []string {
	frequencies := make(map[string]int, len(ips))

	for _, record := range l.History {
		for ip := range record {
			if _, ok := frequencies[ip]; !ok {
				frequencies[ip] = 0
			}

			frequencies[ip]++
		}
	}

	slices.SortStableFunc(ips, func(a, b string) int {
		return frequencies[a] - frequencies[b]
	})
	return ips
}
