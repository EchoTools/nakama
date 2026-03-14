package server

import "testing"

func TestParseConfirmShutdownValue_Valid(t *testing.T) {
	matchID, disconnectServer, graceSeconds, err := parseConfirmShutdownValue("9ddf0c5b-2f44-4126-a5c9-b905f53d29d4.21f9469e:1:45")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if matchID != "9ddf0c5b-2f44-4126-a5c9-b905f53d29d4.21f9469e" {
		t.Fatalf("unexpected matchID: %q", matchID)
	}
	if !disconnectServer {
		t.Fatal("expected disconnectServer=true")
	}
	if graceSeconds != 45 {
		t.Fatalf("expected graceSeconds=45, got %d", graceSeconds)
	}
}

func TestParseConfirmShutdownValue_InvalidFormat(t *testing.T) {
	_, _, _, err := parseConfirmShutdownValue("missing:part")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestParseConfirmShutdownValue_InvalidDisconnectFlag(t *testing.T) {
	_, _, _, err := parseConfirmShutdownValue("abc:2:10")
	if err == nil {
		t.Fatal("expected error for invalid disconnect flag")
	}
}

func TestParseConfirmShutdownValue_InvalidGraceSeconds(t *testing.T) {
	_, _, _, err := parseConfirmShutdownValue("abc:0:not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid grace seconds")
	}
}

func TestParseConfirmShutdownValue_NegativeGraceSeconds(t *testing.T) {
	_, _, _, err := parseConfirmShutdownValue("abc:0:-1")
	if err == nil {
		t.Fatal("expected error for negative grace seconds")
	}
}
