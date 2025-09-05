package fleetmanager

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama-gamelift/fleetmanager"
)

const (
	GameSessionDataKey = "GameSessionData"
	GamePropertiesKey  = "GameProperties"
	GameSessionNameKey = "GameSessionName"
)

const (
	RpcIDUpdateInstanceInfo = "update_instance_info"
	RpcIDDeleteInstanceInfo = "delete_instance_info"
)

// noinspection GoUnusedExportedFunction
func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	initStart := time.Now()

	env, ok := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	if !ok {
		return fmt.Errorf("expects env ctx value to be a map[string]string")
	}

	awsAccessKey, ok := env["GL_AWS_ACCESS_KEY"]
	if !ok {
		return runtime.NewError("missing GL_AWS_ACCESS_KEY environment variable", 3)
	}

	awsSecretAccessKey, ok := env["GL_AWS_SECRET_ACCESS_KEY"]
	if !ok {
		return runtime.NewError("missing GL_AWS_SECRET_ACCESS_KEY environment variable", 3)
	}

	awsRegion, ok := env["GL_AWS_REGION"]
	if !ok {
		return runtime.NewError("missing GL_AWS_REGION environment variable", 3)
	}

	awsAliasId, ok := env["GL_AWS_ALIAS_ID"]
	if !ok {
		return runtime.NewError("missing GL_AWS_ALIAS_ID environment variable", 3)
	}

	awsPlacementQueueName, ok := env["GL_PLACEMENT_QUEUE_NAME"]
	if !ok {
		return runtime.NewError("missing GL_PLACEMENT_QUEUE_NAME environment variable", 3)
	}

	awsGameLiftPlacementEventsQueueUrl, ok := env["GL_PLACEMENT_EVENTS_SQS_URL"]
	if !ok {
		return runtime.NewError("missing GL_PLACEMENT_EVENTS_SQS_URL environment variable", 3)
	}

	cfg := fleetmanager.NewGameLiftConfig(awsAccessKey, awsSecretAccessKey, awsRegion, awsAliasId, awsPlacementQueueName, awsGameLiftPlacementEventsQueueUrl)
	glfm, err := fleetmanager.NewGameLiftFleetManager(ctx, logger, db, initializer, nk, cfg)
	if err != nil {
		return err
	}

	if err = initializer.RegisterFleetManager(glfm); err != nil {
		logger.WithField("error", err).Error("failed to register amazon gamelift fleet manager")
		return err
	}

	logger.Info("Plugin loaded in '%d' ms", time.Now().Sub(initStart).Milliseconds())
	return nil
}
