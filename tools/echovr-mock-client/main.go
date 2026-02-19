package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

var (
	flagAddr        = flag.String("addr", "ws://127.0.0.1:7349", "Nakama WebSocket address")
	flagEvrID       = flag.String("evrid", "DMO-0", "EvrID string (e.g. OVR-ORG-123412341234)")
	flagDisplayName = flag.String("displayname", "MockClient", "Display name")
	flagBuild       = flag.Int64("build", int64(evr.StandaloneBuildNumber), "Build number")
	flagAction      = flag.String("action", "find", "Lobby action: find | create | join")
	flagLobbyID     = flag.String("lobbyid", uuid.Nil.String(), "Target lobby UUID (for -action=join)")
	flagMode        = flag.String("mode", "echo_arena", "Game mode symbol")
	flagLevel       = flag.String("level", "mpl_arena_a", "Level symbol")
	flagChannel     = flag.String("channel", uuid.Nil.String(), "Group/channel UUID")
)

var logger = log.New(os.Stdout, "[mock-client] ", log.LstdFlags|log.Lmsgprefix)

func dial(addr string) (*websocket.Conn, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("parse addr: %w", err)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{"binary"},
	}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return conn, nil
}

func send(conn *websocket.Conn, msgs ...evr.Message) error {
	data, err := evr.Marshal(msgs...)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return conn.WriteMessage(websocket.BinaryMessage, data)
}

func recv(conn *websocket.Conn) ([]evr.Message, error) {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	msgs, err := evr.ParsePacket(data)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return msgs, nil
}

func recvUntilDisconnect(conn *websocket.Conn) ([]evr.Message, error) {
	var all []evr.Message
	for {
		msgs, err := recv(conn)
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				return all, nil
			}
			return all, err
		}
		for _, m := range msgs {
			logger.Printf("← %T %v", m, m)
		}
		all = append(all, msgs...)
		for _, m := range msgs {
			if _, ok := m.(*evr.STcpConnectionUnrequireEvent); ok {
				return all, nil
			}
		}
	}
}

func mustParseUUID(s string) uuid.UUID {
	u, err := uuid.FromString(s)
	if err != nil {
		logger.Fatalf("invalid UUID %q: %v", s, err)
	}
	return u
}

func versionLockFromBuild(build int64) evr.Symbol {
	return evr.Symbol(uint64(build))
}

func platformSymbolFromEvrID(id evr.EvrId) evr.Symbol {
	return evr.Symbol(id.PlatformCode)
}

func runConfigPhase(addr string) error {
	logger.Println("=== Phase 1: Config WS ===")

	conn, err := dial(addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	logger.Println("→ ConfigRequest{main_menu}")
	if err := send(conn, &evr.ConfigRequest{Type: "main_menu", ID: "main_menu"}); err != nil {
		return fmt.Errorf("send ConfigRequest: %w", err)
	}

	msgs, err := recvUntilDisconnect(conn)
	if err != nil {
		return fmt.Errorf("config recv: %w", err)
	}

	for _, m := range msgs {
		switch v := m.(type) {
		case *evr.ConfigSuccess:
			logger.Printf("✓ ConfigSuccess type=%s id=%s", v.Type, v.Id)
		case *evr.STcpConnectionUnrequireEvent:
			logger.Println("✓ STcpConnectionUnrequireEvent – closing config WS")
		}
	}

	return conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func runLoginPhase(addr string, evrID evr.EvrId, profile evr.LoginProfile) (*websocket.Conn, uuid.UUID, error) {
	logger.Println("=== Phase 2: Login WS ===")

	conn, err := dial(addr)
	if err != nil {
		return nil, uuid.Nil, err
	}

	req := &evr.LoginRequest{
		PreviousSessionID: uuid.Nil,
		XPID:              evrID,
		Payload:           profile,
	}
	logger.Printf("→ LoginRequest XPID=%s build=%d", evrID, profile.BuildNumber)
	if err := send(conn, req); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("send LoginRequest: %w", err)
	}

	var loginSessionID uuid.UUID
	msgs, err := recvUntilDisconnect(conn)
	if err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("login recv: %w", err)
	}
	for _, m := range msgs {
		if ls, ok := m.(*evr.LoginSuccess); ok {
			loginSessionID = ls.Session
			logger.Printf("✓ LoginSuccess session=%s", loginSessionID)
		}
	}
	if loginSessionID == uuid.Nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("no LoginSuccess received")
	}

	logger.Printf("→ LoggedInUserProfileRequest session=%s", loginSessionID)
	if err := send(conn, &evr.LoggedInUserProfileRequest{
		Session: loginSessionID,
		EvrID:   evrID,
	}); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("send LoggedInUserProfileRequest: %w", err)
	}
	if msgs, err = recvUntilDisconnect(conn); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("profile recv: %w", err)
	}
	for _, m := range msgs {
		if _, ok := m.(*evr.LoggedInUserProfileSuccess); ok {
			logger.Println("✓ LoggedInUserProfileSuccess")
		}
	}

	logger.Println("→ DocumentRequest lang=en type=eula")
	if err := send(conn, &evr.DocumentRequest{Language: "en", Type: "eula"}); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("send DocumentRequest: %w", err)
	}
	if msgs, err = recvUntilDisconnect(conn); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("document recv: %w", err)
	}
	for _, m := range msgs {
		if _, ok := m.(*evr.DocumentSuccess); ok {
			logger.Println("✓ DocumentSuccess")
		}
	}

	logger.Println("→ ChannelInfoRequest")
	if err := send(conn, &evr.ChannelInfoRequest{}); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("send ChannelInfoRequest: %w", err)
	}
	if msgs, err = recvUntilDisconnect(conn); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("channel info recv: %w", err)
	}
	for _, m := range msgs {
		if _, ok := m.(*evr.ChannelInfoResponse); ok {
			logger.Println("✓ ChannelInfoResponse")
		}
	}

	logger.Printf("→ UpdateClientProfile session=%s evrid=%s", loginSessionID, evrID)
	if err := send(conn, &evr.UpdateClientProfile{
		LoginSessionID: loginSessionID,
		XPID:           evrID,
		Payload: evr.ClientProfile{
			DisplayName: profile.DisplayName,
		},
	}); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("send UpdateClientProfile: %w", err)
	}
	if msgs, err = recvUntilDisconnect(conn); err != nil {
		conn.Close()
		return nil, uuid.Nil, fmt.Errorf("update profile recv: %w", err)
	}
	for _, m := range msgs {
		if _, ok := m.(*evr.UpdateProfileSuccess); ok {
			logger.Println("✓ UpdateProfileSuccess")
		}
	}

	return conn, loginSessionID, nil
}

func runLobbyPhase(conn *websocket.Conn, action string, evrID evr.EvrId, loginSessionID uuid.UUID, build int64) error {
	logger.Printf("=== Phase 3: Lobby (%s) ===", action)

	mode := evr.ToSymbol(*flagMode)
	level := evr.ToSymbol(*flagLevel)
	channelID := mustParseUUID(*flagChannel)
	platform := platformSymbolFromEvrID(evrID)
	versionLock := versionLockFromBuild(build)

	settings := evr.LobbySessionSettings{
		AppID: "1369078409873402",
		Mode:  mode.Int64(),
		Level: level.Int64(),
	}
	entrants := []evr.Entrant{
		{EvrID: evrID, Role: int8(evr.TeamUnassigned)},
	}

	var msg evr.Message
	switch action {
	case "find":
		req := evr.NewLobbyFindSessionRequest(
			versionLock, mode, level, platform,
			loginSessionID, true,
			uuid.Nil, channelID, settings, entrants,
		)
		msg = &req
	case "create":
		msg = &evr.LobbyCreateSessionRequest{
			Region:           evr.DefaultRegion,
			VersionLock:      versionLock,
			Mode:             mode,
			Level:            level,
			Platform:         platform,
			LoginSessionID:   loginSessionID,
			CrossPlayEnabled: true,
			LobbyType:        evr.PublicLobby,
			GroupID:          channelID,
			SessionSettings:  settings,
			Entrants:         entrants,
		}
	case "join":
		lobbyID := mustParseUUID(*flagLobbyID)
		msg = &evr.LobbyJoinSessionRequest{
			LobbyID:          lobbyID,
			VersionLock:      build,
			Platform:         platform,
			LoginSessionID:   loginSessionID,
			CrossPlayEnabled: true,
			SessionSettings:  settings,
			Entrants:         entrants,
		}
	default:
		return fmt.Errorf("unknown action %q; use find, create, or join", action)
	}

	logger.Printf("→ %T", msg)
	if err := send(conn, msg); err != nil {
		return fmt.Errorf("send lobby req: %w", err)
	}

	msgs, err := recvUntilDisconnect(conn)
	if err != nil {
		return fmt.Errorf("lobby recv: %w", err)
	}
	for _, m := range msgs {
		switch v := m.(type) {
		case *evr.LobbySessionSuccessv4:
			logger.Printf("✓ LobbySessionSuccessv4 lobby=%s", v.LobbyID)
		case *evr.LobbySessionSuccessv5:
			logger.Printf("✓ LobbySessionSuccessv5 lobby=%s", v.LobbyID)
		case *evr.LobbySessionFailurev1:
			logger.Printf("✗ LobbySessionFailure code=%d", v.ErrorCode)
		case *evr.LobbySessionFailurev2:
			logger.Printf("✗ LobbySessionFailure code=%d", v.ErrorCode)
		case *evr.LobbySessionFailurev3:
			logger.Printf("✗ LobbySessionFailure code=%d", v.ErrorCode)
		case *evr.LobbySessionFailurev4:
			logger.Printf("✗ LobbySessionFailure code=%d", v.ErrorCode)
		}
	}

	return nil
}

func main() {
	flag.Parse()

	evrID, err := evr.ParseEvrId(*flagEvrID)
	if err != nil {
		logger.Fatalf("invalid evrid %q: %v", *flagEvrID, err)
	}

	profile := evr.LoginProfile{
		AccountId:   evrID.AccountId,
		DisplayName: *flagDisplayName,
		BuildNumber: evr.BuildNumber(*flagBuild),
		AppId:       1369078409873402,
		SystemInfo: evr.SystemInfo{
			HeadsetType:      "Meta Quest 3",
			DriverVersion:    "1.0",
			NetworkType:      "Ethernet",
			VideoCard:        "Mock GPU",
			CPUModel:         "Mock CPU",
			NumPhysicalCores: 8,
			NumLogicalCores:  16,
			MemoryTotal:      16384,
			MemoryUsed:       4096,
		},
	}

	if err := runConfigPhase(*flagAddr); err != nil {
		logger.Fatalf("config phase: %v", err)
	}

	conn, loginSessionID, err := runLoginPhase(*flagAddr, *evrID, profile)
	if err != nil {
		logger.Fatalf("login phase: %v", err)
	}
	defer conn.Close()

	if err := runLobbyPhase(conn, *flagAction, *evrID, loginSessionID, *flagBuild); err != nil {
		logger.Fatalf("lobby phase: %v", err)
	}

	logger.Println("Done.")
}
