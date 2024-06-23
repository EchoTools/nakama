package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) configRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.ConfigRequest)

	// Retrieve the requested object.
	objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: "Config:" + message.ConfigInfo.Type,
			Key:        message.ConfigInfo.Type,
			UserId:     uuid.Nil.String(),
		},
	})

	// Send an error if the object could not be retrieved.
	if err != nil {
		logger.Warn("SNSConfigRequestHandler: failed to read objects", zap.Error(err))
		errorInfo := evr.ConfigErrorInfo{
			Type:       message.ConfigInfo.Type,
			Identifier: message.ConfigInfo.Id,
			ErrorCode:  0x00000001,
			Error:      "failed to read objects",
		}
		session.SendEvr(evr.NewSNSConfigFailure(errorInfo))
		return fmt.Errorf("failed to read objects: %w", err)
	}

	var jsonResource string
	if len(objs.Objects) != 0 {
		// Use the retrieved object.
		jsonResource = objs.Objects[0].Value
	} else {
		// Attempt to pull a default config resource.
		jsonResource = evr.GetDefaultConfigResource(message.ConfigInfo.Type, message.ConfigInfo.Id)
	}
	if jsonResource == "" {
		logger.Warn("SNSConfigRequestHandler: resource not found")
		errorInfo := evr.ConfigErrorInfo{
			Type:       message.ConfigInfo.Type,
			Identifier: message.ConfigInfo.Id,
			ErrorCode:  0x00000001,
			Error:      "resource not found",
		}
		session.SendEvr(evr.NewSNSConfigFailure(errorInfo))
		return fmt.Errorf("resource not found: %s", message.ConfigInfo.Id)
	}

	// Parse the JSON resource
	resource := make(map[string]interface{})
	if err := json.Unmarshal([]byte(jsonResource), &resource); err != nil {

		errorInfo := evr.ConfigErrorInfo{
			Type:       message.ConfigInfo.Type,
			Identifier: message.ConfigInfo.Id,
			ErrorCode:  0x00000001,
			Error:      "failed to parse json",
		}
		session.SendEvr(evr.NewSNSConfigFailure(errorInfo))
		return fmt.Errorf("failed to parse %s json: %w", message.ConfigInfo.Id, err)
	}

	// Send the resource to the client.
	if err := session.SendEvr(
		evr.NewConfigSuccess(message.ConfigInfo.Type, message.ConfigInfo.Id, resource),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
		return fmt.Errorf("failed to send SNSConfigSuccess: %w", err)
	}
	return nil
}
