package integrationtest

import (
	"context"
	"github.com/dujiao-next/internal/shared/mailbrand"
	"sync"
	"sync/atomic"
	"testing"
)

type concurrentMailSender struct{ calls atomic.Int32 }

func (s *concurrentMailSender) SendVerifyCode(_, _, _, _ string, _ mailbrand.Brand) error {
	s.calls.Add(1)
	return nil
}
func TestConcurrentVerificationRequestsDeliverOnce(t *testing.T) {
	sender := &concurrentMailSender{}
	svc, _, db := newRegistrationDomainPolicyAuthServiceWithSender(t, sender)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	var wg sync.WaitGroup
	start := make(chan struct{})
	var accepted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if svc.SendVerifyCode(context.Background(), "same@example.test", "register", "zh-CN") == nil {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if sender.calls.Load() != 1 || accepted.Load() != 1 {
		t.Fatalf("deliveries=%d accepted=%d", sender.calls.Load(), accepted.Load())
	}
}
