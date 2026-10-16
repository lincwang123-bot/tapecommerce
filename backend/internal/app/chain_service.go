package app

import (
	"context"
	"github.com/dujiao-next/internal/logger"
	payment "github.com/dujiao-next/internal/modules/payment/application"
	"time"
)

// Runs read-only chain reconciliation under the existing application lifecycle.
// Each submitted invoice uses a durable database lease across API/worker nodes.
type chainService struct {
	payments *payment.PaymentService
	done     chan struct{}
}

func (s *chainService) Name() string { return "selfhost-payment-reconciliation" }
func (s *chainService) Stop(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *chainService) Start(ctx context.Context) error {
	defer close(s.done)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.payments.RunChainChecks(ctx); err != nil && ctx.Err() == nil {
			logger.Warnw("selfhost_payment_reconciliation_unavailable")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
