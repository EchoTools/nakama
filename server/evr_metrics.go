package server

var _ = Metrics(&EVRMetrics{})

type EVRMetrics struct {
	Metrics
}

func NewEVRMetrics(metrics Metrics) *EVRMetrics {
	return &EVRMetrics{
		Metrics: metrics,
	}
}

func (m *EVRMetrics) CountWebsocketOpened(delta int64) {
	m.CountWebsocketOpened(1)
	m.CustomCounter("session_evr_closed", nil, 1)
}

func (m *EVRMetrics) CountWebsocketClosed(delta int64) {
	m.CountWebsocketClosed(1)
	m.CustomCounter("session_evr_closed", nil, 1)
}
