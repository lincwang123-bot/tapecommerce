package selfhost_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	app "github.com/dujiao-next/internal/modules/payment/application"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	store "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentCreateReusesInvoiceAndFailedCreateReservesNothing(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	other := *f.channel
	other.ID = 0
	other.Name = "same address second channel"
	if e := f.db.Create(&other).Error; e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: p.OrderID, ChannelID: other.ID})
			if e != nil {
				t.Error(e)
			} else if r.Payment.ID != p.ID {
				t.Error("same invoice was not reused")
			}
		}()
	}
	wg.Wait()
	var n int64
	f.db.Model(&domain.ChainIntent{}).Count(&n)
	if n != 1 {
		t.Fatalf("reserved %d amounts", n)
	}
	o := &orderdomain.Order{OrderNo: "rollback-invoice", UserID: f.user.ID, Status: constants.OrderStatusPendingPayment, Currency: "USDT", TotalAmount: money.FromDecimal(decimal.NewFromInt(20))}
	f.db.Create(o)
	if e := f.db.Exec("CREATE TRIGGER fail_invoice BEFORE UPDATE ON orders WHEN NEW.order_no='rollback-invoice' BEGIN SELECT RAISE(ABORT,'synthetic order write failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID}); e == nil {
		t.Fatal("write fault accepted")
	}
	f.db.Model(&domain.ChainIntent{}).Count(&n)
	if n != 1 {
		t.Fatal("rolled back invoice reserved an amount")
	}
	f.db.Model(&domain.Payment{}).Where("order_id=?", o.ID).Count(&n)
	if n != 0 {
		t.Fatal("rolled back invoice left payment")
	}
}

func TestExpiredAmountsNeverRecycleAndAddressRotationRecoversCapacity(t *testing.T) {
	f := setup(t)
	network, _ := f.verifier.Network(f.channel.ChannelType)
	past := time.Now().Add(-24 * time.Hour)
	rows := make([]domain.ChainIntent, 9999)
	for i := range rows {
		units := decimal.NewFromInt(20).Add(decimal.NewFromInt(int64(i + 1)).Shift(-6)).Shift(18).StringFixed(0)
		rows[i] = domain.ChainIntent{PaymentID: uint(i + 100000), Network: network.Network, TokenContract: network.TokenContract, Recipient: f.channel.ConfigJSON["receiving_address"].(string), ExpectedUnits: units, VerificationStatus: "rejected", ExpiresAt: past}
	}
	if e := f.db.CreateInBatches(rows, 100).Error; e != nil {
		t.Fatal(e)
	}
	o := &orderdomain.Order{OrderNo: "exhausted-fixture", UserID: f.user.ID, Status: constants.OrderStatusPendingPayment, Currency: "USDT", TotalAmount: money.FromDecimal(decimal.NewFromInt(20))}
	f.db.Create(o)
	_, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID})
	if !errors.Is(e, contract.ErrChainAmountExhausted) {
		t.Fatalf("exhaustion not explicit: %v", e)
	}
	var n int64
	f.db.Model(&domain.Payment{}).Count(&n)
	if n != 0 {
		t.Fatal("exhausted allocation left invoice")
	}
	f.channel.ConfigJSON = jsonmap.JSON{"receiving_address": "0x" + strings.Repeat("2", 40)}
	f.db.Save(f.channel)
	if _, e = f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID}); e != nil {
		t.Fatal(e)
	}
	f.db.Model(&domain.ChainIntent{}).Count(&n)
	if n != 10000 {
		t.Fatalf("old reservations lost: %d", n)
	}
}

func TestWalletFailureRollsBackReceiptAndLeaseHonorsBackoff(t *testing.T) {
	f := setup(t)
	r, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(20)), Currency: "USDT"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.db.Exec("CREATE TRIGGER fail_wallet BEFORE INSERT ON wallet_transactions BEGIN SELECT RAISE(ABORT,'synthetic ledger fault'); END").Error; e != nil {
		t.Fatal(e)
	}
	hash := "0x" + strings.Repeat("3", 64)
	if _, e = submit(f, r.Payment, hash); e == nil {
		t.Fatal("ledger fault accepted")
	}
	assertUnpaid(t, f, r.Payment)
	var n int64
	f.db.Model(&domain.ChainReceipt{}).Count(&n)
	if n != 0 {
		t.Fatal("ledger rollback consumed receipt")
	}
	var account walletdomain.Account
	f.db.First(&account, "user_id=?", f.user.ID)
	if !account.Balance.Decimal.IsZero() {
		t.Fatal("ledger rollback changed balance")
	}
	repo := store.New(f.db, "synthetic-guest-secret-only").ChainPayments()
	ok, e := repo.Acquire(r.Payment.ID, "rogue", time.Now(), time.Now().Add(time.Minute))
	if e != nil || ok {
		t.Fatal("retry bypassed backoff", e)
	}
	f.db.Exec("DROP TRIGGER fail_wallet")
	due(f, r.Payment.ID)
	if e = f.service.RunChainChecks(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.db.Model(&walletdomain.Transaction{}).Count(&n)
	if n != 1 {
		t.Fatalf("recovery credits=%d", n)
	}
	ok, e = repo.Acquire(r.Payment.ID, "rogue", time.Now(), time.Now().Add(time.Minute))
	if e != nil || ok {
		t.Fatal("terminal invoice leased", e)
	}
}

func TestChannelRotationBetweenHeadReadAndReservationIsRejected(t *testing.T) {
	f := setup(t)
	o := &orderdomain.Order{OrderNo: "head-rotation-fixture", UserID: f.user.ID, Status: constants.OrderStatusPendingPayment, Currency: "USDT", TotalAmount: money.FromDecimal(decimal.NewFromInt(20))}
	if e := f.db.Create(o).Error; e != nil {
		t.Fatal(e)
	}
	f.verifier.onHead = func() {
		if e := f.db.Model(f.channel).Update("config_json", jsonmap.JSON{"receiving_address": "0x" + strings.Repeat("4", 40)}).Error; e != nil {
			t.Fatal(e)
		}
	}
	if _, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID}); !errors.Is(e, app.ErrPaymentGatewayRequestFailed) {
		t.Fatalf("channel rotation was not rejected: %v", e)
	}
	var n int64
	f.db.Model(&domain.ChainIntent{}).Count(&n)
	if n != 0 {
		t.Fatal("mismatched head reserved invoice")
	}
	f.verifier.onHead = nil
	if _, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID}); e != nil {
		t.Fatal(e)
	}
}

// Rotation affects new invoices only; late receipts retain their original recipient.
func TestRotationKeepsOldInvoicePayableAndReplayIdempotent(t *testing.T) {
	f := setup(t)
	old := f.order(t)
	before := intentFor(t, f, old.ID)
	f.channel.ConfigJSON = jsonmap.JSON{"receiving_address": "0x" + strings.Repeat("2", 40)}
	if e := f.db.Save(f.channel).Error; e != nil {
		t.Fatal(e)
	}
	next := f.order(t)
	after := intentFor(t, f, next.ID)
	if before.Recipient == after.Recipient {
		t.Fatal("new invoice did not rotate")
	}
	hash := "0x" + strings.Repeat("8", 64)
	for range 5 {
		if _, e := submit(f, old, hash); e != nil {
			t.Fatal(e)
		}
	}
	retained := intentFor(t, f, old.ID)
	if retained.Recipient != before.Recipient || retained.ExpectedUnits != before.ExpectedUnits || retained.VerificationStatus != "verified" {
		t.Fatalf("old invoice changed or unpaid: %+v", retained)
	}
	var n int64
	f.db.Model(&domain.ChainReceipt{}).Where("payment_id = ?", old.ID).Count(&n)
	if n != 1 {
		t.Fatalf("receipt replay count=%d", n)
	}
	f.db.Model(&domain.ChainIntent{}).Count(&n)
	if n != 2 {
		t.Fatalf("reservation history count=%d", n)
	}
	assertUnpaid(t, f, next)
}

func TestDifferentConcurrentOrdersReserveDistinctPermanentAmounts(t *testing.T) {
	f := setup(t)
	orders := make([]orderdomain.Order, 8)
	for i := range orders {
		orders[i] = orderdomain.Order{OrderNo: fmt.Sprintf("concurrent-unique-%d", i), UserID: f.user.ID, Status: constants.OrderStatusPendingPayment, Currency: "USDT", TotalAmount: money.FromDecimal(decimal.NewFromInt(20))}
		if e := f.db.Create(&orders[i]).Error; e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	for _, o := range orders {
		wg.Add(1)
		go func(o orderdomain.Order) {
			defer wg.Done()
			if _, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID}); e != nil {
				t.Error(e)
			}
		}(o)
	}
	wg.Wait()
	var rows []domain.ChainIntent
	if e := f.db.Find(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if len(rows) != len(orders) {
		t.Fatalf("reserved %d of %d", len(rows), len(orders))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.ExpectedUnits] {
			t.Fatal("duplicate permanent amount")
		}
		seen[row.ExpectedUnits] = true
	}
}
