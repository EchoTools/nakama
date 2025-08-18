package server

import (
	"context"

	evr "github.com/echotools/nakama/v3/protocol"
	"go.uber.org/zap"
)

// ReconcileIAP reconciles an in-app purchase. This is a stub implementation.
func (p *EvrPipeline) reconcileIAP(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.ReconcileIAP)

	if err := session.SendEvr(
		evr.NewReconcileIAPResult(request.EvrId),
	); err != nil {
		return err
	}
	return nil
}
