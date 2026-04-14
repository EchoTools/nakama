package server

import (
	"context"
	"fmt"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// ReconcileIAP reconciles an in-app purchase. This is a stub implementation.
func (p *EvrPipeline) reconcileIAP(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request, ok := in.(*evr.ReconcileIAP)
	if !ok {
		return fmt.Errorf("expected *evr.ReconcileIAP, got %T", in)
	}

	if err := session.SendEvr(
		evr.NewReconcileIAPResult(request.EvrId),
	); err != nil {
		return err
	}
	return nil
}
