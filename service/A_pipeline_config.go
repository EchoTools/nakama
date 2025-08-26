package service

import (
	"context"
	"encoding/json"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func (p *EvrPipeline) configRequest(ctx context.Context, logger *zap.Logger, s *serviceWS, in evr.Message) error {
	message := in.(*evr.ConfigRequest)

	objs, err := p.nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: "Config:" + message.Type,
		Key:        message.Type,
		UserID:     uuid.Nil.String(),
	}})

	// Send an error if the object could not be retrieved.
	if err != nil {
		logger.Warn("failed to read objects", zap.Error(err))
		return s.SendEVR(Envelope{
			ServiceType: ServiceTypeConfig,
			Messages: []evr.Message{
				evr.NewConfigFailure(message.Type, message.ID),
			},
			State: RequireStateUnrequired,
		})
	}

	var jsonResource string
	if len(objs) != 0 {
		// Use the retrieved object.
		jsonResource = objs[0].Value
	} else {
		// Attempt to pull a default config resource.
		jsonResource = evr.GetDefaultConfigResource(message.Type, message.ID)
	}
	if jsonResource == "" {
		logger.Warn("resource not found")
		return s.SendEVR(Envelope{
			ServiceType: ServiceTypeConfig,
			Messages: []evr.Message{
				evr.NewConfigFailure(message.Type, message.ID),
			},
			State: RequireStateUnrequired,
		})

	}

	// Parse the JSON resource
	resource := make(map[string]interface{})
	if err := json.Unmarshal([]byte(jsonResource), &resource); err != nil {
		logger.Warn("failed to unmarshal resource", zap.Error(err))
		return s.SendEVR(Envelope{
			ServiceType: ServiceTypeConfig,
			Messages: []evr.Message{
				evr.NewConfigFailure(message.Type, message.ID),
			},
			State: RequireStateUnrequired,
		})

	}

	return s.SendEVR(Envelope{
		ServiceType: ServiceTypeConfig,
		Messages: []evr.Message{
			evr.NewConfigSuccess(message.Type, message.ID, resource),
		},
		State: RequireStateUnrequired,
	})
}
