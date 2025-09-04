package service

import (
	"context"
	"encoding/json"
	"fmt"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	StorageCollectionConfigDocument = "ConfigDocument"
	StorageIndexConfigDocument      = "config_lookup_index"
)

type ConfigDocumentData struct{}

// StorageIndexes returns the storage indexes for ConfigDocumentData
func (c *ConfigDocumentData) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{
		{
			Name:       StorageIndexConfigDocument,
			Collection: StorageCollectionConfigDocument,
			Key:        "", // Empty key means index applies to all keys in collection
			Fields:     []string{"type", "id"},
			MaxEntries: 1000,
			IndexOnly:  false,
		},
	}
}

func (p *Pipeline) configRequest(ctx context.Context, logger *zap.Logger, s *serviceWS, in evr.Message) error {
	message := in.(*evr.ConfigRequest)

	// Try to find the config document in storage via index
	query := fmt.Sprintf("+value.type:%s +value.id:%s", message.Type, message.ID)
	objs, _, err := p.nevr.StorageIndexList(ctx, SystemUserID, StorageIndexConfigDocument, query, 1, nil, "")
	if err != nil {
		logger.Warn("failed to list config document index", zap.Error(err))
	}

	var resource json.RawMessage

	if len(objs.GetObjects()) > 0 {
		// Found a config document in storage
		resource = json.RawMessage(objs.Objects[0].GetValue())
	} else {
		// Attempt to pull a default config resource.
		defaultResource := evr.GetDefaultConfigResource(message.Type, message.ID)
		if defaultResource == "" {
			logger.Warn("resource not found")
			return s.SendEVR(Envelope{
				ServiceType: ServiceTypeConfig,
				Messages: []evr.Message{
					evr.NewConfigFailure(message.Type, message.ID),
				},
				State: RequireStateUnrequired,
			})
		}
		// Write the default resource to storage for future use
		ops := []*runtime.StorageWrite{{
			Collection:      StorageCollectionConfigDocument,
			Key:             fmt.Sprintf("%s:%s", message.Type, message.ID),
			UserID:          SystemUserID,
			Value:           defaultResource,
			Version:         "",
			PermissionRead:  2,
			PermissionWrite: 0,
		}}
		if _, err := p.nevr.StorageWrite(ctx, ops); err != nil {
			logger.Warn("failed to write default config document", zap.Error(err))
		} else {
			logger.Info("wrote default config document", zap.String("type", message.Type), zap.String("id", message.ID))
		}
		// Use the default resource
		resource = json.RawMessage(defaultResource)
	}

	return s.SendEVR(Envelope{
		ServiceType: ServiceTypeConfig,
		Messages: []evr.Message{
			evr.NewConfigSuccess(message.Type, message.ID, resource),
		},
		State: RequireStateUnrequired,
	})
}
