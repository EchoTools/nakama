package server

import (
	"context"
	"fmt"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

const purchaseItemsRefusal = "items are not for sale"

// purchaseItems refuses every SNSPurchaseItems. The client's store waits for the
// reply with no timer and refuses all further purchases until it arrives, so an
// unanswered request leaves it stuck. The server sells nothing, so the answer
// is always a failure; a success would make the client treat the item as bought.
func (p *EvrPipeline) purchaseItems(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request, ok := in.(*evr.SNSPurchaseItems)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPurchaseItems, got %T", in)
	}

	logger = logger.With(
		zap.String("evrid", request.EvrID.String()),
		zap.String("login_sid", request.LoginSessionID.String()),
		zap.Int64("transaction_id", request.TransactionID),
		zap.ByteString("item_skus", request.Items.ItemSKUs),
	)
	if request.ParseErr != nil {
		logger.Warn("Purchase request could not be fully parsed; refusing it anyway", zap.Error(request.ParseErr))
	}

	if err := session.SendEvr(evr.NewSNSPurchaseItemsFailure(request, purchaseItemsRefusal)); err != nil {
		return fmt.Errorf("send purchase refusal: %w", err)
	}
	logger.Info("Refused purchase", zap.String("reason", purchaseItemsRefusal))
	return nil
}
