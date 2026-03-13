package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
	"github.com/go-ping/ping"
	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	udpPingRequestSymbol uint64 = 0x997279DE065A03B0
	udpPingAckSymbol     uint64 = 0x4F7AE556E0B77891

	seriesMaxPoints      = 120
	qualityWindowSize    = 60
	defaultUDPPort       = "6792"
	defaultUDPTargets    = "gameserver-chi1.echovrce.com:6792,gameserver-dal1.echovce.com:6792"
	defaultBase24Palette = "0,18,19,8,20,7,21,15,1,11,3,10,4,13,6,5,52,88,22,58,17,23,24,25"
	matchPollInterval    = 200 * time.Millisecond
	sessionPollInterval  = 200 * time.Millisecond
	echoReplayTimeFormat = "2006/01/02 15:04:05.000"
)

type base24Palette [24]ui.Color

type probeResult struct {
	Key      string
	Kind     string
	Target   string
	Success  bool
	RTT      time.Duration
	Err      string
	Time     time.Time
	Response string
}

type loginUpdate struct {
	Connected bool
	LoggedIn  bool
	SessionID string
	EvrID     string
	Err       string
	Time      time.Time
}

type udpTarget struct {
	Host string
	Port int
}

type config struct {
	wsURL          string
	sessionToken   string
	evRID          string
	displayName    string
	buildNumber    int64
	wsPingInterval time.Duration
	wsPongTimeout  time.Duration
	probeInterval  time.Duration
	probeTimeout   time.Duration
	icmpHost       string
	udpTargets     []udpTarget
	base24         base24Palette
	logPath        string
}

type probeStats struct {
	kind         string
	target       string
	latency      []float64
	jitter       []float64
	quality      []float64
	recentOK     []bool
	recentCount  int
	lastRTTMs    float64
	lastJitterMs float64
	avgRTTMs     float64
	hasPrevRTT   bool
	prevRTTMs    float64
	errors       int
	lastErr      string
	lastTime     time.Time
}

type model struct {
	stats       map[string]*probeStats
	wsStatus    loginUpdate
	recentErrs  []string
	seriesOrder []string
	matchState  matchState
	logger      *zap.Logger
	mu          sync.Mutex
}

type playerPingSample struct {
	DisplayName string
	EvrID       string
	ClientIP    string
	PingMillis  int
}

type matchState struct {
	TargetOnline bool
	HeadsetIP    string
	MatchID      string
	Players      []playerPingSample
	Err          string
	ReplayActive bool
	ReplayPath   string
	UpdatedAt    time.Time
}

type matchPollUpdate struct {
	TargetOnline bool
	HeadsetIP    string
	MatchID      string
	Players      []playerPingSample
	Err          string
	ReplayActive bool
	ReplayPath   string
	UpdatedAt    time.Time
}

type matchListResponse struct {
	Matches []matchListItem `json:"matches"`
}

type matchListItem struct {
	MatchID string `json:"matchId"`
	Label   string `json:"label"`
}

type matchLabelPayload struct {
	ID      string           `json:"id"`
	Players []matchLabelUser `json:"players"`
}

type matchLabelUser struct {
	DisplayName string `json:"display_name"`
	EvrID       string `json:"evr_id"`
	ClientIP    string `json:"client_ip"`
	PingMillis  int    `json:"ping_ms"`
}

type replaySignal struct {
	Active bool
	Path   string
	Err    string
}

func newModel(logger *zap.Logger, cfg config) *model {
	m := &model{
		stats:      make(map[string]*probeStats),
		recentErrs: make([]string, 0, 8),
		logger:     logger,
	}
	m.ensureProbe("websocket", "websocket", cfg.wsURL)
	m.ensureProbe("icmp", "icmp", cfg.icmpHost)
	for _, t := range cfg.udpTargets {
		key := udpKey(t)
		m.ensureProbe(key, "udp", fmt.Sprintf("%s:%d", t.Host, t.Port))
	}
	return m
}

func (m *model) ensureProbe(key, kind, target string) {
	if _, ok := m.stats[key]; ok {
		return
	}
	m.stats[key] = &probeStats{kind: kind, target: target}
	m.seriesOrder = append(m.seriesOrder, key)
}

func (m *model) addResult(r probeResult) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.stats[r.Key]
	if !ok {
		s = &probeStats{kind: r.Kind, target: r.Target}
		m.stats[r.Key] = s
		m.seriesOrder = append(m.seriesOrder, r.Key)
	}
	s.kind = r.Kind
	s.target = r.Target

	ms := float64(r.RTT.Microseconds()) / 1000.0
	if ms < 0 {
		ms = 0
	}

	if r.Success {
		s.lastRTTMs = ms
		s.avgRTTMs = recomputeAvg(s.avgRTTMs, ms, len(s.latency)+1)
		s.lastErr = ""
		if s.hasPrevRTT {
			s.lastJitterMs = math.Abs(ms - s.prevRTTMs)
		} else {
			s.lastJitterMs = 0
			s.hasPrevRTT = true
		}
		s.prevRTTMs = ms
		s.latency = appendCapped(s.latency, ms, seriesMaxPoints)
		s.jitter = appendCapped(s.jitter, s.lastJitterMs, seriesMaxPoints)
	} else {
		s.errors++
		s.lastErr = r.Err
		s.latency = appendCapped(s.latency, 0, seriesMaxPoints)
		s.jitter = appendCapped(s.jitter, s.lastJitterMs, seriesMaxPoints)
		m.pushErr(fmt.Sprintf("%s: %s", s.target, r.Err), r.Time)
	}

	s.recentOK = append(s.recentOK, r.Success)
	if len(s.recentOK) > qualityWindowSize {
		s.recentOK = s.recentOK[1:]
	}
	s.recentCount = 0
	for _, ok := range s.recentOK {
		if ok {
			s.recentCount++
		}
	}
	quality := 0.0
	if len(s.recentOK) > 0 {
		quality = (float64(s.recentCount) / float64(len(s.recentOK))) * 100.0
	}
	s.quality = appendCapped(s.quality, quality, seriesMaxPoints)
	s.lastTime = r.Time
}

func (m *model) setMatchState(st matchPollUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matchState = matchState{
		TargetOnline: st.TargetOnline,
		HeadsetIP:    st.HeadsetIP,
		MatchID:      st.MatchID,
		Players:      append([]playerPingSample(nil), st.Players...),
		Err:          st.Err,
		ReplayActive: st.ReplayActive,
		ReplayPath:   st.ReplayPath,
		UpdatedAt:    st.UpdatedAt,
	}
	if st.Err != "" {
		m.pushErr("match: "+st.Err, time.Now())
	}
}

func (m *model) setWSStatus(st loginUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wsStatus = st
	if st.Err != "" {
		m.pushErr("websocket: "+st.Err, st.Time)
	}
}

func (m *model) pushErr(msg string, at time.Time) {
	line := fmt.Sprintf("%s %s", at.Format("15:04:05"), msg)
	m.recentErrs = append(m.recentErrs, line)
	if len(m.recentErrs) > 8 {
		m.recentErrs = m.recentErrs[len(m.recentErrs)-8:]
	}
}

func appendCapped(in []float64, value float64, max int) []float64 {
	in = append(in, value)
	if len(in) > max {
		in = in[1:]
	}
	return in
}

func recomputeAvg(prev, value float64, count int) float64 {
	if count <= 1 {
		return value
	}
	return prev + (value-prev)/float64(count)
}

func udpKey(t udpTarget) string {
	return "udp:" + t.Host + ":" + strconv.Itoa(t.Port)
}

func parseConfig() (config, error) {
	var (
		wsURL          = flag.String("ws-url", "ws://127.0.0.1:7349", "Nakama websocket URL")
		sessionToken   = flag.String("session-token", "", "Session JWT token")
		evrID          = flag.String("evr-id", "", "Echo VR account ID (OVR-ORG-...; if empty, prompt at startup)")
		displayName    = flag.String("display-name", "NetMonitor", "Display name used in LoginRequest payload")
		buildNumber    = flag.Int64("build", int64(evr.StandaloneBuildNumber), "Echo VR build number")
		icmpHost       = flag.String("icmp-host", "g.echovrce.com", "ICMP target host")
		udpTargetsRaw  = flag.String("udp-targets", defaultUDPTargets, "Comma-separated UDP probe targets host[:port]")
		base24Raw      = flag.String("base24", defaultBase24Palette, "Comma-separated Base24 xterm color indices (24 values)")
		probeInterval  = flag.Duration("probe-interval", 2*time.Second, "Interval for ICMP/UDP probes")
		probeTimeout   = flag.Duration("probe-timeout", 1500*time.Millisecond, "Timeout for ICMP/UDP/WS operations")
		wsPingInterval = flag.Duration("ws-ping-interval", 5*time.Second, "Websocket ping interval")
		wsPongTimeout  = flag.Duration("ws-pong-timeout", 20*time.Second, "Max time without websocket pong before reconnect")
		logPath        = flag.String("log-file", "logs/evr-net-monitor.jsonl", "JSON log file path")
	)
	flag.Parse()

	targets, err := parseUDPTargets(*udpTargetsRaw)
	if err != nil {
		return config{}, err
	}

	evrIDValue := strings.TrimSpace(*evrID)
	if evrIDValue == "" {
		var promptErr error
		evrIDValue, promptErr = promptForEVRID()
		if promptErr != nil {
			return config{}, promptErr
		}
	}

	if _, err := evr.ParseEvrId(evrIDValue); err != nil {
		return config{}, fmt.Errorf("invalid evr id %q: %w", evrIDValue, err)
	}

	palette, err := parseBase24Palette(*base24Raw)
	if err != nil {
		return config{}, err
	}

	token := strings.TrimSpace(*sessionToken)

	return config{
		wsURL:          *wsURL,
		sessionToken:   token,
		evRID:          evrIDValue,
		displayName:    *displayName,
		buildNumber:    *buildNumber,
		wsPingInterval: *wsPingInterval,
		wsPongTimeout:  *wsPongTimeout,
		probeInterval:  *probeInterval,
		probeTimeout:   *probeTimeout,
		icmpHost:       *icmpHost,
		udpTargets:     targets,
		base24:         palette,
		logPath:        *logPath,
	}, nil
}

func parseBase24Palette(raw string) (base24Palette, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != len(base24Palette{}) {
		return base24Palette{}, fmt.Errorf("invalid --base24 value: expected 24 comma-separated numbers, got %d", len(parts))
	}

	var out base24Palette
	for i, part := range parts {
		v := strings.TrimSpace(part)
		idx, err := strconv.Atoi(v)
		if err != nil {
			return base24Palette{}, fmt.Errorf("invalid --base24 color index %q: %w", v, err)
		}
		if idx < 0 || idx > 255 {
			return base24Palette{}, fmt.Errorf("invalid --base24 color index %d: must be between 0 and 255", idx)
		}
		out[i] = ui.Color(idx)
	}

	return out, nil
}

func promptForEVRID() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("OVR-ORG- ID: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read OVR-ORG- ID: %w", err)
	}
	evrID := strings.TrimSpace(line)
	if evrID == "" {
		return "", errors.New("OVR-ORG- ID is required")
	}
	return evrID, nil
}

func parseUDPTargets(raw string) ([]udpTarget, error) {
	parts := strings.Split(raw, ",")
	out := make([]udpTarget, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		host, portStr, err := net.SplitHostPort(p)
		if err != nil {
			host = p
			portStr = defaultUDPPort
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid UDP target port %q in %q", portStr, p)
		}
		out = append(out, udpTarget{Host: host, Port: port})
	}
	if len(out) == 0 {
		return nil, errors.New("at least one UDP target is required")
	}
	return out, nil
}

func initLogger(path string) (*zap.Logger, error) {
	dir := "."
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir = path[:i]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	rotator := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    20,
		MaxBackups: 5,
		MaxAge:     14,
		Compress:   true,
	}

	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(rotator),
		zap.InfoLevel,
	)
	return zap.New(core, zap.AddCaller()), nil
}

func isTerminalFile(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func canUseTerminalUI() bool {
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return false
	}
	return isTerminalFile(os.Stdin) && isTerminalFile(os.Stdout) && isTerminalFile(os.Stderr)
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger, err := initLogger(cfg.logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger init error: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	probeCh := make(chan probeResult, 1024)
	wsStateCh := make(chan loginUpdate, 64)
	matchStateCh := make(chan matchPollUpdate, 64)

	app := newModel(logger, cfg)

	if strings.TrimSpace(cfg.sessionToken) == "" {
		app.setWSStatus(loginUpdate{
			Connected: false,
			LoggedIn:  false,
			EvrID:     cfg.evRID,
			Time:      time.Now(),
		})
		logger.Info("websocket monitor disabled because session token is empty")
	} else {
		go runWebsocketMonitor(ctx, cfg, logger, probeCh, wsStateCh)
	}
	go runICMPMonitor(ctx, cfg.icmpHost, cfg.probeInterval, cfg.probeTimeout, logger, probeCh)
	for _, t := range cfg.udpTargets {
		go runUDPMonitor(ctx, t, cfg.probeInterval, cfg.probeTimeout, logger, probeCh)
	}
	go runMatchMonitor(ctx, cfg, logger, probeCh, matchStateCh)

	uiEnabled := canUseTerminalUI()
	if uiEnabled {
		if err := ui.Init(); err != nil {
			uiEnabled = false
			logger.Warn("failed to init UI; running headless", zap.Error(err))
		} else {
			defer ui.Close()
		}
	}

	var (
		header          *widgets.Paragraph
		latencyPlot     *widgets.Plot
		jitterPlot      *widgets.Plot
		qualityPlot     *widgets.Plot
		statusTable     *widgets.Table
		errList         *widgets.List
		insightsList    *widgets.List
		playersTable    *widgets.Table
		statusColWidth  float64
		playersColWidth float64
		errorsColWidth  float64
		applyLayout     func()
		grid            *ui.Grid
		uiEvents        <-chan ui.Event
	)

	render := func() {}
	if uiEnabled {
		palette := cfg.base24
		lineColors := []ui.Color{palette[8], palette[11], palette[10], palette[13], palette[9]}
		baseText := palette[5]
		accent := palette[12]
		goodColor := palette[10]
		warnColor := palette[11]
		badColor := palette[8]
		unknownColor := palette[3]

		statusColWidth = 0.34
		playersColWidth = 0.26
		errorsColWidth = 0.20

		applyLayout = func() {
			insightsColWidth := 1.0 - statusColWidth - playersColWidth - errorsColWidth
			if insightsColWidth < 0.15 {
				insightsColWidth = 0.15
				errorsColWidth = 1.0 - statusColWidth - playersColWidth - insightsColWidth
				if errorsColWidth < 0.15 {
					errorsColWidth = 0.15
					playersColWidth = 1.0 - statusColWidth - errorsColWidth - insightsColWidth
				}
			}

			grid.Set(
				ui.NewRow(0.12, ui.NewCol(1.0, header)),
				ui.NewRow(0.28, ui.NewCol(1.0, latencyPlot)),
				ui.NewRow(0.24,
					ui.NewCol(0.5, jitterPlot),
					ui.NewCol(0.5, qualityPlot),
				),
				ui.NewRow(0.36,
					ui.NewCol(statusColWidth, statusTable),
					ui.NewCol(playersColWidth, playersTable),
					ui.NewCol(errorsColWidth, errList),
					ui.NewCol(1.0-statusColWidth-playersColWidth-errorsColWidth, insightsList),
				),
			)
		}

		header = widgets.NewParagraph()
		header.Title = "EVR Network Monitor"
		header.TextStyle = ui.NewStyle(palette[13])
		header.BorderStyle = ui.NewStyle(palette[13])
		header.TitleStyle = ui.NewStyle(palette[13], ui.ColorClear, ui.ModifierBold)

		latencyPlot = widgets.NewPlot()
		latencyPlot.Title = "Latency (ms)"
		latencyPlot.LineColors = lineColors
		latencyPlot.AxesColor = baseText
		latencyPlot.BorderStyle = ui.NewStyle(palette[12])
		latencyPlot.TitleStyle = ui.NewStyle(palette[12], ui.ColorClear, ui.ModifierBold)

		jitterPlot = widgets.NewPlot()
		jitterPlot.Title = "Jitter (ms)"
		jitterPlot.LineColors = latencyPlot.LineColors
		jitterPlot.AxesColor = baseText
		jitterPlot.BorderStyle = ui.NewStyle(palette[11])
		jitterPlot.TitleStyle = ui.NewStyle(palette[11], ui.ColorClear, ui.ModifierBold)

		qualityPlot = widgets.NewPlot()
		qualityPlot.Title = "Quality (% success, rolling)"
		qualityPlot.LineColors = latencyPlot.LineColors
		qualityPlot.AxesColor = baseText
		qualityPlot.MaxVal = 100
		qualityPlot.BorderStyle = ui.NewStyle(palette[10])
		qualityPlot.TitleStyle = ui.NewStyle(palette[10], ui.ColorClear, ui.ModifierBold)

		statusTable = widgets.NewTable()
		statusTable.Title = "Probe Status"
		statusTable.RowSeparator = false
		statusTable.TextStyle = ui.NewStyle(baseText)
		statusTable.RowStyles[0] = ui.NewStyle(accent, ui.ColorClear, ui.ModifierBold)
		statusTable.BorderStyle = ui.NewStyle(palette[14])
		statusTable.TitleStyle = ui.NewStyle(palette[14], ui.ColorClear, ui.ModifierBold)

		errList = widgets.NewList()
		errList.Title = "Recent Errors"
		errList.WrapText = false
		errList.TextStyle = ui.NewStyle(baseText)
		errList.BorderStyle = ui.NewStyle(palette[8])
		errList.TitleStyle = ui.NewStyle(palette[8], ui.ColorClear, ui.ModifierBold)

		insightsList = widgets.NewList()
		insightsList.Title = "Insights"
		insightsList.WrapText = false
		insightsList.TextStyle = ui.NewStyle(baseText)
		insightsList.BorderStyle = ui.NewStyle(palette[6])
		insightsList.TitleStyle = ui.NewStyle(palette[6], ui.ColorClear, ui.ModifierBold)

		playersTable = widgets.NewTable()
		playersTable.Title = "Match Player Pings"
		playersTable.RowSeparator = false
		playersTable.TextStyle = ui.NewStyle(baseText)
		playersTable.RowStyles[0] = ui.NewStyle(accent, ui.ColorClear, ui.ModifierBold)
		playersTable.BorderStyle = ui.NewStyle(palette[9])
		playersTable.TitleStyle = ui.NewStyle(palette[9], ui.ColorClear, ui.ModifierBold)

		grid = ui.NewGrid()
		termW, termH := ui.TerminalDimensions()
		grid.SetRect(0, 0, termW, termH)
		applyLayout()

		uiEvents = ui.PollEvents()

		render = func() {
			app.mu.Lock()
			defer app.mu.Unlock()

			header.Text = buildHeaderText(app.wsStatus, cfg, app.matchState) + " | Resize asides: 1/2 players 3/4 status 5/6 errors"
			if app.matchState.TargetOnline {
				latencyPlot.Title = "Latency (ms) [PLAYER ONLINE]"
				jitterPlot.Title = "Jitter (ms) [PLAYER ONLINE]"
				qualityPlot.Title = "Quality (% success, rolling) [PLAYER ONLINE]"
				header.TextStyle = ui.NewStyle(goodColor)
			} else {
				latencyPlot.Title = "Latency (ms)"
				jitterPlot.Title = "Jitter (ms)"
				qualityPlot.Title = "Quality (% success, rolling)"
				header.TextStyle = ui.NewStyle(unknownColor)
			}

			latencyPlot.Data = buildSeries(app, func(ps *probeStats) []float64 { return ps.latency })

			jitterPlot.Data = buildSeries(app, func(ps *probeStats) []float64 { return ps.jitter })

			qualityPlot.Data = buildSeries(app, func(ps *probeStats) []float64 { return ps.quality })

			statusRows, statusStyles := buildTableRowsStyled(app, goodColor, warnColor, badColor, unknownColor)
			statusTable.Rows = statusRows
			statusTable.RowStyles = statusStyles
			errList.Rows = append([]string(nil), app.recentErrs...)
			if len(errList.Rows) == 0 {
				errList.Rows = []string{"No errors recorded."}
			}

			insightsList.Rows = buildInsightsRows(app)
			playerRows, playerStyles := buildPlayerPingRowsStyled(app.matchState, goodColor, warnColor, badColor, unknownColor)
			playersTable.Rows = playerRows
			playersTable.RowStyles = playerStyles

			ui.Render(grid)
		}

		render()
	}

	tickInterval := time.Second
	if !uiEnabled {
		tickInterval = 5 * time.Second
	}
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case evt := <-uiEvents:
			if !uiEnabled {
				continue
			}
			switch evt.ID {
			case "q", "<C-c>":
				cancel()
				return
			case "<Resize>":
				payload := evt.Payload.(ui.Resize)
				grid.SetRect(0, 0, payload.Width, payload.Height)
				applyLayout()
				render()
			case "1":
				playersColWidth = maxFloat(0.15, playersColWidth-0.03)
				applyLayout()
				render()
			case "2":
				playersColWidth = minFloat(0.45, playersColWidth+0.03)
				applyLayout()
				render()
			case "3":
				statusColWidth = maxFloat(0.20, statusColWidth-0.03)
				applyLayout()
				render()
			case "4":
				statusColWidth = minFloat(0.55, statusColWidth+0.03)
				applyLayout()
				render()
			case "5":
				errorsColWidth = maxFloat(0.15, errorsColWidth-0.03)
				applyLayout()
				render()
			case "6":
				errorsColWidth = minFloat(0.40, errorsColWidth+0.03)
				applyLayout()
				render()
			}
		case st := <-wsStateCh:
			app.setWSStatus(st)
			logger.Info("ws_status",
				zap.Bool("connected", st.Connected),
				zap.Bool("logged_in", st.LoggedIn),
				zap.String("session_id", st.SessionID),
				zap.String("evr_id", st.EvrID),
				zap.String("error", st.Err),
			)
		case r := <-probeCh:
			app.addResult(r)
			if r.Success {
				logger.Info("probe",
					zap.String("kind", r.Kind),
					zap.String("target", r.Target),
					zap.Bool("ok", true),
					zap.Float64("rtt_ms", float64(r.RTT.Microseconds())/1000.0),
					zap.String("response", r.Response),
				)
			} else {
				logger.Warn("probe",
					zap.String("kind", r.Kind),
					zap.String("target", r.Target),
					zap.Bool("ok", false),
					zap.String("error", r.Err),
				)
			}
		case st := <-matchStateCh:
			app.setMatchState(st)
			if st.Err != "" {
				logger.Warn("match_poll", zap.String("error", st.Err))
			}
			if st.TargetOnline {
				logger.Info("target_online",
					zap.String("evr_id", cfg.evRID),
					zap.String("headset_ip", st.HeadsetIP),
					zap.String("match_id", st.MatchID),
					zap.Int("players", len(st.Players)),
					zap.Bool("replay_active", st.ReplayActive),
					zap.String("replay_path", st.ReplayPath),
				)
			}
		case <-tick.C:
			if uiEnabled {
				render()
			} else {
				logHeadlessSnapshot(app, cfg, logger)
			}
		}
	}
}

func buildHeaderText(ws loginUpdate, cfg config, ms matchState) string {
	status := "disconnected"
	if ws.Connected {
		status = "connected"
	}
	login := "pending"
	if ws.LoggedIn {
		login = "ok"
	}
	presence := "offline"
	if ms.TargetOnline {
		presence = "ONLINE"
	}
	replay := "idle"
	if ms.ReplayActive {
		replay = "capturing"
	}
	errText := ""
	if ws.Err != "" {
		errText = " | ws_err=" + ws.Err
	}
	if ms.Err != "" {
		errText += " | match_err=" + ms.Err
	}
	return fmt.Sprintf(
		"PLAYER=%s Replay=%s WS=%s Login=%s Session=%s EvrID=%s | ICMP=%s | UDP=%s | Headset=%s",
		presence,
		replay,
		status,
		login,
		emptyFallback(ws.SessionID, "-"),
		emptyFallback(ws.EvrID, "-"),
		cfg.icmpHost,
		strings.Join(formatUDPTargets(cfg.udpTargets), ", "),
		emptyFallback(ms.HeadsetIP, "-"),
	) + errText
}

func emptyFallback(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func formatUDPTargets(targets []udpTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, fmt.Sprintf("%s:%d", t.Host, t.Port))
	}
	return out
}

func buildSeries(m *model, selector func(*probeStats) []float64) [][]float64 {
	out := make([][]float64, 0, len(m.seriesOrder))
	for _, key := range m.seriesOrder {
		if ps, ok := m.stats[key]; ok {
			vals := selector(ps)
			if len(vals) == 0 {
				vals = []float64{0, 0}
			} else if len(vals) == 1 {
				vals = []float64{vals[0], vals[0]}
			}
			out = append(out, vals)
		}
	}
	return out
}

func buildTableRows(m *model) [][]string {
	rows := [][]string{{"Target", "Last(ms)", "Avg(ms)", "Jitter(ms)", "Quality(%)", "Errors", "Last Error"}}
	for _, key := range m.seriesOrder {
		ps, ok := m.stats[key]
		if !ok {
			continue
		}
		quality := 0.0
		if len(ps.recentOK) > 0 {
			quality = (float64(ps.recentCount) / float64(len(ps.recentOK))) * 100
		}
		rows = append(rows, []string{
			ps.kind + ":" + ps.target,
			fmt.Sprintf("%.1f", ps.lastRTTMs),
			fmt.Sprintf("%.1f", ps.avgRTTMs),
			fmt.Sprintf("%.1f", ps.lastJitterMs),
			fmt.Sprintf("%.1f", quality),
			strconv.Itoa(ps.errors),
			emptyFallback(ps.lastErr, "-"),
		})
	}
	return rows
}

func buildTableRowsStyled(m *model, goodColor, warnColor, badColor, unknownColor ui.Color) ([][]string, map[int]ui.Style) {
	rows := buildTableRows(m)
	styles := map[int]ui.Style{
		0: ui.NewStyle(unknownColor, ui.ColorClear, ui.ModifierBold),
	}

	for i := 1; i < len(rows); i++ {
		row := rows[i]
		if len(row) < 7 {
			styles[i] = ui.NewStyle(unknownColor)
			continue
		}

		quality, qErr := strconv.ParseFloat(row[4], 64)
		errCount, eErr := strconv.Atoi(row[5])
		if qErr != nil || eErr != nil {
			styles[i] = ui.NewStyle(unknownColor)
			continue
		}

		switch {
		case errCount > 0 || quality < 90:
			styles[i] = ui.NewStyle(badColor)
		case quality < 97:
			styles[i] = ui.NewStyle(warnColor)
		default:
			styles[i] = ui.NewStyle(goodColor)
		}
	}

	return rows, styles
}

func buildInsightsRows(m *model) []string {
	rows := []string{
		"Jitter guide (variation, ms): <=2 excellent, <=5 good, <=10 moderate, >10 unstable.",
		"Latency guide: <30ms excellent, <60ms good, <120ms fair, >=120ms poor.",
		fmt.Sprintf("Quality guide (rolling last up to %d probes): >=99%% excellent, >=95%% good, >=90%% warning, <90%% poor.", qualityWindowSize),
	}

	worstJitterTarget := ""
	worstJitter := -1.0
	lowestQualityTarget := ""
	lowestQuality := 101.0
	hasQuality := false

	for _, key := range m.seriesOrder {
		ps, ok := m.stats[key]
		if !ok {
			continue
		}

		if ps.lastJitterMs > worstJitter {
			worstJitter = ps.lastJitterMs
			worstJitterTarget = ps.kind + ":" + ps.target
		}

		quality, ok := currentQuality(ps)
		if ok && quality < lowestQuality {
			lowestQuality = quality
			lowestQualityTarget = ps.kind + ":" + ps.target
			hasQuality = true
		}
	}

	if worstJitterTarget != "" {
		rows = append(rows, fmt.Sprintf("Highest current jitter: %s at %.1fms (%s).", worstJitterTarget, worstJitter, jitterLabel(worstJitter)))
	}
	if hasQuality {
		rows = append(rows, fmt.Sprintf("Lowest quality: %s at %.1f%% (%s).", lowestQualityTarget, lowestQuality, qualityLabel(lowestQuality)))
	} else {
		rows = append(rows, "Quality summary: waiting for probe samples.")
	}

	if m.wsStatus.Err != "" {
		rows = append(rows, "Websocket note: "+m.wsStatus.Err)
	} else if !m.wsStatus.LoggedIn {
		rows = append(rows, "Websocket note: not logged in yet; UDP/ICMP still valid for path quality.")
	}

	if m.matchState.TargetOnline {
		rows = append(rows, "Target player status: ONLINE in current match (graphs marked).")
	} else {
		rows = append(rows, "Target player status: offline/not found in match list.")
	}
	if m.matchState.ReplayActive {
		rows = append(rows, "Replay capture active: "+m.matchState.ReplayPath)
	}

	return rows
}

func buildPlayerPingRows(ms matchState) [][]string {
	rows := [][]string{{"Player", "EVR ID", "Ping(ms)", "Headset IP"}}
	if len(ms.Players) == 0 {
		rows = append(rows, []string{"No players", "-", "-", "-"})
		return rows
	}

	players := append([]playerPingSample(nil), ms.Players...)
	sort.Slice(players, func(i, j int) bool {
		if players[i].PingMillis == players[j].PingMillis {
			return players[i].DisplayName < players[j].DisplayName
		}
		return players[i].PingMillis < players[j].PingMillis
	})

	for _, p := range players {
		rows = append(rows, []string{
			emptyFallback(p.DisplayName, "-"),
			emptyFallback(p.EvrID, "-"),
			fmt.Sprintf("%d", p.PingMillis),
			emptyFallback(p.ClientIP, "-"),
		})
	}
	return rows
}

func buildPlayerPingRowsStyled(ms matchState, goodColor, warnColor, badColor, unknownColor ui.Color) ([][]string, map[int]ui.Style) {
	rows := buildPlayerPingRows(ms)
	styles := map[int]ui.Style{
		0: ui.NewStyle(unknownColor, ui.ColorClear, ui.ModifierBold),
	}

	for i := 1; i < len(rows); i++ {
		row := rows[i]
		if len(row) < 3 {
			styles[i] = ui.NewStyle(unknownColor)
			continue
		}
		ping, err := strconv.Atoi(row[2])
		if err != nil {
			styles[i] = ui.NewStyle(unknownColor)
			continue
		}
		switch {
		case ping <= 40:
			styles[i] = ui.NewStyle(goodColor)
		case ping <= 80:
			styles[i] = ui.NewStyle(warnColor)
		case ping <= 120:
			styles[i] = ui.NewStyle(warnColor)
		default:
			styles[i] = ui.NewStyle(badColor)
		}
	}

	return rows, styles
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func currentQuality(ps *probeStats) (float64, bool) {
	if len(ps.recentOK) == 0 {
		return 0, false
	}
	return (float64(ps.recentCount) / float64(len(ps.recentOK))) * 100, true
}

func jitterLabel(jitterMs float64) string {
	switch {
	case jitterMs <= 2:
		return "excellent stability"
	case jitterMs <= 5:
		return "good stability"
	case jitterMs <= 10:
		return "moderate variation"
	default:
		return "unstable variation"
	}
}

func qualityLabel(quality float64) string {
	switch {
	case quality >= 99:
		return "excellent"
	case quality >= 95:
		return "good"
	case quality >= 90:
		return "warning"
	default:
		return "poor"
	}
}

func logHeadlessSnapshot(m *model, cfg config, logger *zap.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()

	rows := buildTableRows(m)
	flatRows := make([]string, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		flatRows = append(flatRows, strings.Join(rows[i], " | "))
	}

	errs := append([]string(nil), m.recentErrs...)
	logger.Info("headless_status",
		zap.String("header", buildHeaderText(m.wsStatus, cfg, m.matchState)),
		zap.Strings("rows", flatRows),
		zap.Strings("recent_errors", errs),
	)
}

func runMatchMonitor(ctx context.Context, cfg config, logger *zap.Logger, probeCh chan<- probeResult, matchStateCh chan<- matchPollUpdate) {
	state := matchPollUpdate{UpdatedAt: time.Now()}
	if strings.TrimSpace(cfg.sessionToken) == "" {
		state.Err = "session token required for /v2/match polling"
		select {
		case <-ctx.Done():
			return
		case matchStateCh <- state:
		}
		return
	}

	client := &http.Client{Timeout: 1500 * time.Millisecond}
	ticker := time.NewTicker(matchPollInterval)
	defer ticker.Stop()

	var (
		activeHeadsetIP     string
		activeHeadsetCancel context.CancelFunc
		replayCh            chan replaySignal
	)
	defer func() {
		if activeHeadsetCancel != nil {
			activeHeadsetCancel()
		}
	}()

	for {
		for {
			select {
			case sig := <-replayCh:
				state.ReplayActive = sig.Active
				if sig.Path != "" {
					state.ReplayPath = sig.Path
				}
				if sig.Err != "" {
					state.Err = sig.Err
				}
			default:
				goto poll
			}
		}

	poll:
		pollState, err := pollMatchState(ctx, client, cfg)
		if err != nil {
			state.Err = err.Error()
		} else {
			state.TargetOnline = pollState.TargetOnline
			state.HeadsetIP = pollState.HeadsetIP
			state.MatchID = pollState.MatchID
			state.Players = pollState.Players
			state.Err = ""
		}
		state.UpdatedAt = time.Now()

		if state.TargetOnline && state.HeadsetIP != "" {
			if state.HeadsetIP != activeHeadsetIP {
				if activeHeadsetCancel != nil {
					activeHeadsetCancel()
				}
				childCtx, childCancel := context.WithCancel(ctx)
				activeHeadsetCancel = childCancel
				activeHeadsetIP = state.HeadsetIP
				replayCh = make(chan replaySignal, 64)
				go runICMPMonitorWithKey(childCtx, "headset-icmp", "icmp-headset", state.HeadsetIP, matchPollInterval, cfg.probeTimeout, logger, probeCh)
				go runHeadsetReplayCapture(childCtx, state.HeadsetIP, cfg.evRID, logger, replayCh)
			}
		} else if activeHeadsetCancel != nil {
			activeHeadsetCancel()
			activeHeadsetCancel = nil
			activeHeadsetIP = ""
			replayCh = nil
			state.ReplayActive = false
		}

		select {
		case <-ctx.Done():
			return
		case matchStateCh <- state:
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func pollMatchState(ctx context.Context, client *http.Client, cfg config) (matchPollUpdate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://g.echovrce.com:7350/v2/match", nil)
	if err != nil {
		return matchPollUpdate{}, fmt.Errorf("build /v2/match request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.sessionToken)

	resp, err := client.Do(req)
	if err != nil {
		return matchPollUpdate{}, fmt.Errorf("poll /v2/match: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return matchPollUpdate{}, fmt.Errorf("read /v2/match body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return matchPollUpdate{}, fmt.Errorf("/v2/match status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var ml matchListResponse
	if err := json.Unmarshal(body, &ml); err != nil {
		return matchPollUpdate{}, fmt.Errorf("decode /v2/match JSON: %w", err)
	}

	targetID := strings.ToUpper(strings.TrimSpace(cfg.evRID))
	update := matchPollUpdate{UpdatedAt: time.Now()}

	for _, match := range ml.Matches {
		if strings.TrimSpace(match.Label) == "" {
			continue
		}

		var label matchLabelPayload
		if err := json.Unmarshal([]byte(match.Label), &label); err != nil {
			continue
		}

		players := make([]playerPingSample, 0, len(label.Players))
		targetOnline := false
		headsetIP := ""

		for _, p := range label.Players {
			sample := playerPingSample{
				DisplayName: p.DisplayName,
				EvrID:       strings.ToUpper(strings.TrimSpace(p.EvrID)),
				ClientIP:    normalizeIP(p.ClientIP),
				PingMillis:  p.PingMillis,
			}
			players = append(players, sample)
			if sample.EvrID == targetID {
				targetOnline = true
				headsetIP = sample.ClientIP
			}
		}

		if targetOnline {
			update.TargetOnline = true
			update.HeadsetIP = headsetIP
			update.MatchID = emptyFallback(label.ID, match.MatchID)
			update.Players = players
			return update, nil
		}
	}

	return update, nil
}

func normalizeIP(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(v)
	if err == nil {
		return host
	}
	return v
}

func runHeadsetReplayCapture(ctx context.Context, headsetIP string, evrID string, logger *zap.Logger, replayCh chan<- replaySignal) {
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	ticker := time.NewTicker(sessionPollInterval)
	defer ticker.Stop()

	var recorder *echoReplayWriter
	var lastSessionPayload []byte
	sendState := func(active bool, path string, err error) {
		sig := replaySignal{Active: active, Path: path}
		if err != nil {
			sig.Err = err.Error()
		}
		select {
		case <-ctx.Done():
		case replayCh <- sig:
		}
	}

	defer func() {
		if recorder != nil {
			if err := recorder.Close(); err != nil {
				logger.Warn("close replay writer failed", zap.Error(err))
			}
			sendState(false, recorder.path, nil)
		}
	}()

	url := fmt.Sprintf("http://%s:6721/session", headsetIP)
	for {
		sessionJSON, err := fetchUsefulSessionJSON(ctx, client, url)
		if err == nil {
			if bytes.Equal(lastSessionPayload, sessionJSON) {
				goto wait
			}
			lastSessionPayload = append(lastSessionPayload[:0], sessionJSON...)

			if recorder == nil {
				rec, err := newEchoReplayWriter(evrID, headsetIP)
				if err != nil {
					sendState(false, "", fmt.Errorf("create replay writer: %w", err))
				} else {
					recorder = rec
					sendState(true, recorder.path, nil)
					logger.Info("replay capture started", zap.String("path", recorder.path), zap.String("headset_ip", headsetIP))
				}
			}
			if recorder != nil {
				if err := recorder.WriteSessionFrame(time.Now(), sessionJSON); err != nil {
					sendState(true, recorder.path, fmt.Errorf("write replay frame: %w", err))
				}
			}
		}

	wait:
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func fetchUsefulSessionJSON(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("session endpoint status %d", resp.StatusCode)
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, errors.New("session payload not useful yet")
	}

	var obj map[string]any
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, err
	}
	if !isUsefulSessionObject(obj) {
		return nil, errors.New("session payload not useful yet")
	}

	compact := &bytes.Buffer{}
	if err := json.Compact(compact, trimmed); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func isUsefulSessionObject(obj map[string]any) bool {
	if len(obj) == 0 {
		return false
	}
	keys := []string{"sessionid", "sessionId", "teams", "game_status", "gameStatus", "blue_points", "orange_points"}
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

type echoReplayWriter struct {
	path      string
	file      *os.File
	zipWriter *zip.Writer
	replayW   io.Writer
}

func newEchoReplayWriter(evrID, headsetIP string) (*echoReplayWriter, error) {
	if err := os.MkdirAll("logs/replays", 0o755); err != nil {
		return nil, err
	}
	ts := time.Now().Format("20060102_150405")
	safeID := strings.ReplaceAll(strings.ToUpper(evrID), "-", "_")
	safeIP := strings.ReplaceAll(headsetIP, ":", "_")
	filename := fmt.Sprintf("%s_%s_%s.echoreplay", ts, safeID, safeIP)
	path := filepath.Join("logs/replays", filename)

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(f)
	entryName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entry, err := zw.Create(entryName)
	if err != nil {
		_ = zw.Close()
		_ = f.Close()
		return nil, err
	}

	return &echoReplayWriter{path: path, file: f, zipWriter: zw, replayW: entry}, nil
}

func (w *echoReplayWriter) WriteSessionFrame(ts time.Time, sessionJSON []byte) error {
	if w == nil || w.replayW == nil {
		return errors.New("replay writer not initialized")
	}
	if _, err := io.WriteString(w.replayW, ts.Format(echoReplayTimeFormat)); err != nil {
		return err
	}
	if _, err := io.WriteString(w.replayW, "\t"); err != nil {
		return err
	}
	if _, err := w.replayW.Write(sessionJSON); err != nil {
		return err
	}
	_, err := io.WriteString(w.replayW, "\r\n")
	return err
}

func (w *echoReplayWriter) Close() error {
	if w == nil {
		return nil
	}
	var firstErr error
	if w.zipWriter != nil {
		if err := w.zipWriter.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runWebsocketMonitor(ctx context.Context, cfg config, logger *zap.Logger, probeCh chan<- probeResult, statusCh chan<- loginUpdate) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{"binary"},
	}

	var backoff time.Duration = time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		conn, _, err := dialer.DialContext(ctx, cfg.wsURL, nil)
		if err != nil {
			now := time.Now()
			statusCh <- loginUpdate{Connected: false, LoggedIn: false, Err: err.Error(), Time: now}
			probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "dial: " + err.Error(), Time: now}
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}

		backoff = time.Second
		statusCh <- loginUpdate{Connected: true, LoggedIn: false, Time: time.Now()}

		if err := conn.SetReadDeadline(time.Now().Add(cfg.wsPongTimeout)); err != nil {
			logger.Warn("failed to set initial read deadline", zap.Error(err))
		}

		var lastPongNS atomic.Int64
		lastPongNS.Store(time.Now().UnixNano())
		conn.SetPongHandler(func(appData string) error {
			now := time.Now()
			lastPongNS.Store(now.UnixNano())
			_ = conn.SetReadDeadline(now.Add(cfg.wsPongTimeout))
			rtt := time.Duration(0)
			if sentNS, err := strconv.ParseInt(appData, 10, 64); err == nil {
				rtt = now.Sub(time.Unix(0, sentNS))
			}
			probeCh <- probeResult{
				Key:     "websocket",
				Kind:    "websocket",
				Target:  cfg.wsURL,
				Success: true,
				RTT:     rtt,
				Time:    now,
			}
			return nil
		})

		loginResultCh := make(chan loginUpdate, 1)
		readErrCh := make(chan error, 1)
		go wsReadLoop(conn, cfg.wsURL, probeCh, statusCh, loginResultCh, readErrCh)

		if err := sendLoginRequest(conn, cfg); err != nil {
			now := time.Now()
			statusCh <- loginUpdate{Connected: false, LoggedIn: false, Err: err.Error(), Time: now}
			probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "login send: " + err.Error(), Time: now}
			_ = conn.Close()
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}

		loggedIn := false
		select {
		case <-ctx.Done():
			_ = conn.Close()
			return
		case upd := <-loginResultCh:
			statusCh <- upd
			loggedIn = upd.LoggedIn
		case err := <-readErrCh:
			now := time.Now()
			statusCh <- loginUpdate{Connected: false, LoggedIn: false, Err: err.Error(), Time: now}
			probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "read before login: " + err.Error(), Time: now}
			_ = conn.Close()
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		case <-time.After(cfg.probeTimeout):
			now := time.Now()
			statusCh <- loginUpdate{Connected: true, LoggedIn: false, Err: "login response timeout", Time: now}
			probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "login response timeout", Time: now}
			_ = conn.Close()
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}

		if !loggedIn {
			_ = conn.Close()
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 30*time.Second)
			continue
		}

		pingTicker := time.NewTicker(cfg.wsPingInterval)
		livenessTicker := time.NewTicker(time.Second)

		reconnect := false
		for !reconnect {
			select {
			case <-ctx.Done():
				pingTicker.Stop()
				livenessTicker.Stop()
				_ = conn.Close()
				return
			case err := <-readErrCh:
				now := time.Now()
				statusCh <- loginUpdate{Connected: false, LoggedIn: false, Err: err.Error(), Time: now}
				probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "read: " + err.Error(), Time: now}
				reconnect = true
			case <-pingTicker.C:
				now := time.Now()
				payload := strconv.FormatInt(now.UnixNano(), 10)
				_ = conn.SetWriteDeadline(now.Add(cfg.probeTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, []byte(payload)); err != nil {
					statusCh <- loginUpdate{Connected: false, LoggedIn: false, Err: err.Error(), Time: now}
					probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "ping write: " + err.Error(), Time: now}
					reconnect = true
				}
			case <-livenessTicker.C:
				last := time.Unix(0, lastPongNS.Load())
				if time.Since(last) > cfg.wsPongTimeout {
					now := time.Now()
					statusCh <- loginUpdate{Connected: false, LoggedIn: false, Err: "pong timeout", Time: now}
					probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: cfg.wsURL, Success: false, Err: "pong timeout", Time: now}
					reconnect = true
				}
			}
		}

		pingTicker.Stop()
		livenessTicker.Stop()
		_ = conn.Close()
		if !sleepContext(ctx, backoff) {
			return
		}
		backoff = minDuration(backoff*2, 30*time.Second)
	}
}

func wsReadLoop(conn *websocket.Conn, wsTarget string, probeCh chan<- probeResult, statusCh chan<- loginUpdate, loginResultCh chan<- loginUpdate, readErrCh chan<- error) {
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			readErrCh <- err
			return
		}
		if msgType != websocket.BinaryMessage {
			continue
		}

		msgs, err := evr.ParsePacket(data)
		if err != nil {
			now := time.Now()
			probeCh <- probeResult{Key: "websocket", Kind: "websocket", Target: wsTarget, Success: false, Err: "parse packet: " + err.Error(), Time: now}
			continue
		}

		for _, m := range msgs {
			switch v := m.(type) {
			case *evr.LoginSuccess:
				upd := loginUpdate{
					Connected: true,
					LoggedIn:  true,
					SessionID: v.Session.String(),
					EvrID:     v.EvrId.String(),
					Time:      time.Now(),
				}
				select {
				case loginResultCh <- upd:
				default:
				}
				statusCh <- upd
			case *evr.LoginFailure:
				upd := loginUpdate{
					Connected: true,
					LoggedIn:  false,
					EvrID:     v.UserId.String(),
					Err:       fmt.Sprintf("status=%d message=%s", v.StatusCode, v.ErrorMessage),
					Time:      time.Now(),
				}
				select {
				case loginResultCh <- upd:
				default:
				}
				statusCh <- upd
			}
		}
	}
}

func sendLoginRequest(conn *websocket.Conn, cfg config) error {
	evID, err := evr.ParseEvrId(cfg.evRID)
	if err != nil {
		return fmt.Errorf("parse evrid: %w", err)
	}

	req := &evr.LoginRequest{
		PreviousSessionID: uuid.Nil,
		XPID:              *evID,
		Payload: evr.LoginProfile{
			AccountId:   evID.AccountId,
			DisplayName: cfg.displayName,
			AccessToken: cfg.sessionToken,
			BuildNumber: evr.BuildNumber(cfg.buildNumber),
			AppId:       1369078409873402,
			SystemInfo: evr.SystemInfo{
				HeadsetType:      "Monitor",
				DriverVersion:    "1.0",
				NetworkType:      "Ethernet",
				VideoCard:        "N/A",
				CPUModel:         "N/A",
				NumPhysicalCores: 8,
				NumLogicalCores:  16,
				MemoryTotal:      8192,
				MemoryUsed:       1024,
			},
		},
	}

	payload, err := evr.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal LoginRequest: %w", err)
	}

	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		return fmt.Errorf("write LoginRequest: %w", err)
	}
	return nil
}

func runICMPMonitor(ctx context.Context, host string, interval, timeout time.Duration, logger *zap.Logger, probeCh chan<- probeResult) {
	runICMPMonitorWithKey(ctx, "icmp", "icmp", host, interval, timeout, logger, probeCh)
}

func runICMPMonitorWithKey(ctx context.Context, key, kind, host string, interval, timeout time.Duration, logger *zap.Logger, probeCh chan<- probeResult) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		result := probeResult{Key: key, Kind: kind, Target: host, Time: time.Now()}
		rtt, err := probeICMP(host, timeout)
		if err != nil {
			result.Success = false
			result.Err = err.Error()
		} else {
			result.Success = true
			result.RTT = rtt
		}
		probeCh <- result

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func probeICMP(host string, timeout time.Duration) (time.Duration, error) {
	pinger, err := ping.NewPinger(host)
	if err != nil {
		return 0, fmt.Errorf("create pinger: %w", err)
	}
	pinger.Count = 1
	pinger.Timeout = timeout
	pinger.Interval = timeout
	pinger.SetPrivileged(false)

	if err := pinger.Run(); err != nil {
		return 0, fmt.Errorf("icmp run: %w", err)
	}

	stats := pinger.Statistics()
	if stats == nil || stats.PacketsRecv == 0 {
		return 0, errors.New("icmp timeout")
	}
	return stats.AvgRtt, nil
}

func runUDPMonitor(ctx context.Context, target udpTarget, interval, timeout time.Duration, logger *zap.Logger, probeCh chan<- probeResult) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	key := udpKey(target)
	addrText := fmt.Sprintf("%s:%d", target.Host, target.Port)

	for {
		result := probeResult{Key: key, Kind: "udp", Target: addrText, Time: time.Now()}
		rtt, response, err := probeUDPHealthcheck(ctx, target, timeout)
		if err != nil {
			result.Success = false
			result.Err = err.Error()
		} else {
			result.Success = true
			result.RTT = rtt
			result.Response = response
		}
		probeCh <- result

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func probeUDPHealthcheck(ctx context.Context, target udpTarget, timeout time.Duration) (time.Duration, string, error) {
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return 0, "", fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return 0, "", fmt.Errorf("set deadline: %w", err)
	}

	req := make([]byte, 16)
	if _, err := rand.Read(req[8:]); err != nil {
		return 0, "", fmt.Errorf("nonce generation: %w", err)
	}
	binary.LittleEndian.PutUint64(req[:8], udpPingRequestSymbol)

	start := time.Now()
	if _, err := conn.Write(req); err != nil {
		return 0, "", fmt.Errorf("write request: %w", err)
	}

	resp := make([]byte, 16)
	n, err := conn.Read(resp)
	if err != nil {
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			return 0, "", fmt.Errorf("timeout waiting for udp reply from %s", addr)
		}
		return 0, "", fmt.Errorf("read response: %w", err)
	}
	if n != 16 {
		return 0, "", fmt.Errorf("invalid response size %d from %s", n, addr)
	}

	if binary.LittleEndian.Uint64(resp[:8]) != udpPingAckSymbol {
		return 0, "", fmt.Errorf("unexpected ack symbol from %s", addr)
	}
	if binary.LittleEndian.Uint64(resp[8:]) != binary.LittleEndian.Uint64(req[8:]) {
		return 0, "", fmt.Errorf("token mismatch from %s", addr)
	}

	rtt := time.Since(start)

	select {
	case <-ctx.Done():
		return rtt, "", ctx.Err()
	default:
	}

	return rtt, "ack", nil
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
