package tapecommercebootstrap

import (
	"context"
	tapestore "github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/gormstore"
	"time"
)

func (l *Live) Name() string { return "tapecommerce-payment-reconciliation" }

// Reconcile scans durable submitted receipts fairly using a bounded cursor. It
// does not need browser credentials or contact the signer after payment.
func (l *Live) Reconcile(ctx context.Context) error {
	if e := l.localReady(); e != nil {
		return e
	}
	ids, e := tapestore.New(l.Bridge.DB).Pending(ctx, l.Config.ShopID, l.cursor, 20)
	if e != nil {
		return e
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempt, cancel := context.WithTimeout(ctx, 25*time.Second)
		_ = l.Bridge.service().DeliverOne(attempt, id)
		cancel()
		l.cursor = id
	}
	if len(ids) < 20 {
		l.cursor = ""
	}
	return nil
}
func (l *Live) Start(ctx context.Context) error {
	l.mutex.Lock()
	if l.done == nil {
		l.done = make(chan struct{})
	}
	if l.stopped {
		l.mutex.Unlock()
		return nil
	}
	if l.started {
		done := l.done
		l.mutex.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
	l.started = true
	ctx, cancel := context.WithCancel(ctx)
	l.stop = cancel
	l.mutex.Unlock()
	defer cancel()
	defer l.doneOnce.Do(func() { close(l.done) })
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if l.localReady() == nil {
			_ = l.Reconcile(ctx)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (l *Live) Stop(ctx context.Context) error {
	l.mutex.Lock()
	if l.done == nil {
		l.done = make(chan struct{})
	}
	l.stopped = true
	cancel := l.stop
	if !l.started {
		l.doneOnce.Do(func() { close(l.done) })
	}
	done := l.done
	l.mutex.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
