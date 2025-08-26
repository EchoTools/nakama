package evr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"testing"
)

// load the LoginFailure message test object from the test data file in tests/LoginFailure.json

func TestLoginFailure(t *testing.T) {

	name := "LoginFailure"
	var jsonMessage *LoginFailure

	binaryMessage := &LoginFailure{
		XPID:         XPID{},
		StatusCode:   0,
		ErrorMessage: "",
	}

	jsonBytes, err := os.ReadFile(fmt.Sprintf("tests/%s.json", name))
	if err != nil {
		return
	}
	binaryBytes, err := os.ReadFile(fmt.Sprintf("tests/%s.bin", name))
	if err != nil {
		return
	}

	// parse the json Data

	err = json.Unmarshal(jsonBytes, &jsonMessage)
	if err != nil {
		return
	}
	structMessage := NewLoginFailure(XPID{
		PlatformCode: jsonMessage.XPID.PlatformCode,
		AccountId:    jsonMessage.XPID.AccountId,
	}, jsonMessage.ErrorMessage)
	structMessage.StatusCode = jsonMessage.StatusCode

	err = binaryMessage.Stream(NewEasyStream(DecodeMode, binaryBytes))
	if err != nil {
		return
	}

	streamedMessage := NewEasyStream(1, []byte{})
	if err := binaryMessage.Stream(streamedMessage); err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	streamedBytes := streamedMessage.Bytes()

	// compare binary results and test
	if !bytes.Equal(binaryBytes, streamedBytes) {
		// print the binary bytes with zero padding for easier comparison
		log.Printf("binaryMessage: %v", binaryMessage)
		log.Printf("jsonMessage:   %v", jsonMessage)
		log.Printf("structMessage: %v", structMessage)
		log.Printf("")
		log.Printf("binaryBytes:   %03v", binaryBytes)
		log.Printf("streamedBytes: %03v", streamedBytes)
		// highlight the differennt bytes

		t.Errorf("Non-Matching results")
	}
}
