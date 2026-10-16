package application_test

import (
	coupongorm "github.com/dujiao-next/internal/modules/coupon/infrastructure/gormstore"
	"github.com/dujiao-next/internal/modules/order/domain"
	"strings"
	"sync"
	"testing"
)

func TestClientOrderIDConcurrentReplayAndConflict(t *testing.T) {
	f := newCouponLimitRaceFixture(t, "idempotency", 0, 0)
	svc := f.service(coupongorm.New(f.db), coupongorm.NewUsageStore(f.db))
	input := f.input(1)
	input.CouponCode = ""
	input.ClientRequestID = "api:1:external-order-100"
	var wg sync.WaitGroup
	ids := make(chan uint, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := svc.CreateOrder(input)
			if e != nil {
				errs <- e
				return
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	var first uint
	for id := range ids {
		if first == 0 {
			first = id
		}
		if id != first {
			t.Fatal("different orders", id, first)
		}
	}
	var count int64
	f.db.Model(&domain.Order{}).Where("parent_id IS NULL").Count(&count)
	if count != 1 {
		t.Fatal("duplicate orders", count)
	}
	input.Items[0].Quantity = 2
	if _, e := svc.CreateOrder(input); e == nil || !strings.Contains(e.Error(), "idempotency_conflict") {
		t.Fatal("conflict accepted", e)
	}
	input.Items[0].Quantity = 1
	restarted := f.service(coupongorm.New(f.db), coupongorm.NewUsageStore(f.db))
	o, e := restarted.CreateOrder(input)
	if e != nil || o.ID != first {
		t.Fatal("restart replay failed", e)
	}
}
