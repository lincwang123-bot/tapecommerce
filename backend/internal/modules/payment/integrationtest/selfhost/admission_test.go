package selfhost_test

import (
	"context"
	"errors"
	"fmt"
	app "github.com/dujiao-next/internal/modules/payment/application"
	"github.com/dujiao-next/internal/modules/payment/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func rechargeInput(f *fixture, amount int64) app.CreateWalletRechargePaymentInput {
	return app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(amount)), Currency: "USDT", Context: context.Background()}
}

func TestRechargeAdmissionConcurrentQuotaBeforeRPC(t *testing.T) {
	f := setup(t)
	var heads atomic.Int32
	f.verifier.onHead = func() { heads.Add(1) }
	var admitted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.service.CreateWalletRechargePayment(rechargeInput(f, int64(20+i)))
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, app.ErrRechargeLimit) {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if admitted.Load() != 3 || heads.Load() != 3 {
		t.Fatalf("admitted %d heads %d", admitted.Load(), heads.Load())
	}
	var count int64
	f.db.Model(&domain.ChainIntent{}).Count(&count)
	if count != 3 {
		t.Fatal(count)
	}
}

func TestRechargeAdmissionDuplicatePreparingAndReuse(t *testing.T) {
	f := setup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var heads atomic.Int32
	f.verifier.onHead = func() {
		if heads.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	result := make(chan *app.CreateWalletRechargePaymentResult, 1)
	failures := make(chan error, 1)
	go func() {
		r, err := f.service.CreateWalletRechargePayment(rechargeInput(f, 20))
		result <- r
		failures <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preparer did not reach RPC")
	}
	// This request must finish while the first is still blocked in RPC, proving
	// the admission transaction released the DB connection before network work.
	duplicate := make(chan error, 1)
	go func() { _, err := f.service.CreateWalletRechargePayment(rechargeInput(f, 20)); duplicate <- err }()
	select {
	case err := <-duplicate:
		if !errors.Is(err, app.ErrRechargePreparing) {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("duplicate blocked behind RPC")
	}
	close(release)
	first := <-result
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	again, err := f.service.CreateWalletRechargePayment(rechargeInput(f, 20))
	if err != nil || again.Payment.ID != first.Payment.ID || heads.Load() != 1 {
		t.Fatal(again, err, heads.Load())
	}
	var count int64
	f.db.Model(&domain.ChainIntent{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
}

func TestRechargeStalePlaceholderDoesNotBlockAccount(t *testing.T) {
	f := setup(t)
	r, err := f.service.CreateWalletRechargePayment(rechargeInput(f, 20))
	if err != nil {
		t.Fatal(err)
	}
	// A crashed preparer's already-expired initiated record consumes no quota.
	past := time.Now().Add(-time.Minute)
	if err = f.db.Model(&domain.Payment{}).Where("id = ?", r.Payment.ID).Updates(map[string]any{"status": "initiated", "expired_at": past, "qr_code": ""}).Error; err != nil {
		t.Fatal(err)
	}
	next, err := f.service.CreateWalletRechargePayment(rechargeInput(f, 20))
	if err != nil || next.Payment.ID == r.Payment.ID {
		t.Fatal(next, err)
	}
}

func TestOrderChainInvoiceSwitchingBounded(t *testing.T) {
	f := setup(t)
	first := f.order(t)
	var heads atomic.Int32
	f.verifier.onHead = func() { heads.Add(1) }
	var last uint
	for i := 2; i <= 4; i++ {
		ch := *f.channel
		ch.ID = 0
		ch.Name = fmt.Sprint(i)
		ch.ConfigJSON = jsonmap.JSON{"receiving_address": fmt.Sprintf("0x%040d", i)}
		if err := f.db.Create(&ch).Error; err != nil {
			t.Fatal(err)
		}
		_, err := f.service.CreatePayment(app.CreatePaymentInput{OrderID: first.OrderID, ChannelID: ch.ID})
		if i < 4 {
			if err != nil {
				t.Fatal(err)
			}
			last = ch.ID
		} else if !errors.Is(err, app.ErrChainInvoiceLimit) {
			t.Fatal(err)
		}
	}
	if heads.Load() != 2 {
		t.Fatalf("rejected request used RPC: %d", heads.Load())
	}
	if _, err := f.service.CreatePayment(app.CreatePaymentInput{OrderID: first.OrderID, ChannelID: last}); err != nil {
		t.Fatal("existing invoice not reusable", err)
	}
	var count int64
	f.db.Model(&domain.ChainIntent{}).Count(&count)
	if count != 3 {
		t.Fatal(count)
	}
}
