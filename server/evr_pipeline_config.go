package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/echotools/nakama/v3/server/evr"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/zap"
)

func (p *EvrPipeline) configRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.ConfigRequest)

	// Retrieve the requested object.
	objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: "Config:" + message.Type,
			Key:        message.Type,
			UserId:     uuid.Nil.String(),
		},
	})

	// Send an error if the object could not be retrieved.
	if err != nil {
		logger.Warn("failed to read objects", zap.Error(err))
		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("failed to read objects: %w", err)
	}

	var jsonResource string
	if len(objs.Objects) != 0 {
		// Use the retrieved object.
		jsonResource = objs.Objects[0].Value
	} else {
		// Attempt to pull a default config resource.
		jsonResource = evr.GetDefaultConfigResource(message.Type, message.ID)
	}
	if jsonResource == "" {
		logger.Warn("resource not found")
		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("resource not found: %s", message.ID)
	}

	// Parse the JSON resource
	resource := make(map[string]interface{})
	if err := json.Unmarshal([]byte(jsonResource), &resource); err != nil {

		session.SendEvrUnrequire(evr.NewConfigFailure(message.Type, message.ID))
		return fmt.Errorf("failed to parse %s json: %w", message.ID, err)
	}

	// Send the resource to the client.
	if err := session.SendEvrUnrequire(evr.NewConfigSuccess(message.Type, message.ID, resource)); err != nil {
		return fmt.Errorf("failed to send SNSConfigSuccess: %w", err)
	}
	return nil
}
