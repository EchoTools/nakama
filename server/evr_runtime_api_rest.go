package server

import (
	"encoding/json"
	"net/http"
)

func RESTError(w http.ResponseWriter, message any, responseCode int) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	http.Error(w, string(data), responseCode)
	return nil
}
