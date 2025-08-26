package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func SendEvent(ctx context.Context, nk runtime.NakamaModule, e any) error {
	payloadBytes, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to marshal login event payload: %v", err)
	}
	nk.Event(ctx, &api.Event{
		Name: fmt.Sprintf("%T", e),
		Properties: map[string]string{
			"payload": string(payloadBytes),
		},
		External: true,
	})
	return nil
}
