package app

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/logger"
	payment "github.com/dujiao-next/internal/modules/payment/application"
	"time"
)

// Separate lifecycle workers ensure a broken network cannot block manual hash
// reconciliation or another network. Context cancellation reaches every RPC.
type chainDiscoveryService struct {
	payments *payment.PaymentService
	network  string
	done     chan struct{}
}

func (s *chainDiscoveryService) Name() string { return "selfhost-discovery-" + s.network }
func (s *chainDiscoveryService) Stop(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *chainDiscoveryService) Start(ctx context.Context) error {
	defer close(s.done)
	var lastState string
	var lastEmit time.Time
	for {
		report, err := s.payments.RunChainDiscovery(ctx, s.network)
		if ctx.Err() != nil {
			return nil
		}
		state := fmt.Sprintf("%s:%t", report.Code, err != nil || report.Failed > 0)
		if state != lastState || time.Since(lastEmit) >= time.Minute {
			lastState, lastEmit = state, time.Now()
			if err != nil || report.Failed > 0 {
				logger.Warnw("selfhost_discovery", "network", s.network, "state", report.Code, "scanned", report.Scanned, "failed", report.Failed, "error", err)
			} else {
				logger.Infow("selfhost_discovery", "network", s.network, "state", report.Code, "scanned", report.Scanned)
			}
		}
		// Delay after completion, not a queued ticker: slow scans cannot trigger
		// a burst. Back off the whole network on failures, including fresh rows.
		delay := 2 * time.Second
		if err != nil || report.Failed > 0 {
			delay = 30 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
