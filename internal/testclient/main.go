package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	evr "github.com/echotools/nakama/v3/protocol" // Adjust the import path as necessary

	"github.com/gorilla/websocket"
)

func main() {

	// Set up channel to listen for interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Connect to the WebSocket server
	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:7347/ws", nil)
	if err != nil {
		log.Fatal("Dial error:", err)
	}
	defer conn.Close()

	go func() {
		<-sigChan
		log.Println("Received interrupt signal, closing connection...")
		os.Exit(0)
	}()

	// Create a LoginRequest message
	loginRequest := evr.LoginRequest{
		// Fill in the required fields
		XPID: evr.XPID{PlatformCode: evr.DMO, AccountId: 1234123412343421},
	}

	data, err := evr.Marshal(&loginRequest)
	if err != nil {
		log.Fatal("Marshal error:", err)
	}
	// Send the LoginRequest message

	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		log.Fatal("Write error:", err)
	}

	typ, data, err := conn.ReadMessage()
	if typ != websocket.BinaryMessage {
		log.Fatal("Received non-binary message")
	}
	if err != nil {
		log.Fatal("Read error:", err)
	}

	// Unmarshal the response
	messages, err := evr.ParsePacket(data)
	if err != nil {
		log.Fatal("Unmarshal error:", err)
	}

	jsondata, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		log.Fatal("Marshal error:", err)
	}

	fmt.Println(string(jsondata))

}
