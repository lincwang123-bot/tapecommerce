package selfhost_test

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	app "github.com/dujiao-next/internal/modules/payment/application"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"strings"
	"sync"
	"testing"
	"time"
)

func due(f *fixture, id uint) {
	past := time.Now().Add(-time.Second)
	f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", id).Updates(map[string]any{"next_check_at": past, "lease_until": nil})
}
func intentFor(t *testing.T, f *fixture, id uint) domain.ChainIntent {
	t.Helper()
	var v domain.ChainIntent
	if e := f.db.First(&v, id).Error; e != nil {
		t.Fatal(e)
	}
	return v
}
func assertUnpaid(t *testing.T, f *fixture, p *domain.Payment) {
	t.Helper()
	fresh, e := f.service.GetPayment(p.ID)
	if e != nil || fresh.Status == constants.PaymentStatusSuccess {
		t.Fatalf("unexpected paid: %v %v", fresh, e)
	}
	var n int64
	f.db.Model(&walletdomain.Transaction{}).Count(&n)
	if n != 0 {
		t.Fatalf("unexpected wallet credit %d", n)
	}
}

func TestPendingRPCFailureAndRestartRecovery(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	hash := "0x" + strings.Repeat("c", 64)
	f.verifier.kind = "pending"
	if _, e := submit(f, p, hash); e != nil {
		t.Fatal(e)
	}
	assertUnpaid(t, f, p)
	v := intentFor(t, f, p.ID)
	if v.SubmittedHash != hash || v.NextCheckAt == nil {
		t.Fatal("pending submission not durable")
	}
	f.verifier.kind = "unavailable"
	due(f, p.ID)
	if e := f.service.RunChainChecks(context.Background()); e != nil {
		t.Fatal(e)
	}
	assertUnpaid(t, f, p)
	if intentFor(t, f, p.ID).VerificationStatus != "unavailable" {
		t.Fatal("rpc failure lost")
	}
	f.verifier.kind = ""
	due(f, p.ID)
	if e := f.service.RunChainChecks(context.Background()); e != nil {
		t.Fatal(e)
	}
	if intentFor(t, f, p.ID).VerificationStatus != "verified" {
		t.Fatal("restart recheck failed")
	}
}
func TestCallbackCannotForgeSelfhostSuccessAndBusinessFailureRollsBackReceipt(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	if _, e := f.service.HandleCallback(app.PaymentCallbackInput{PaymentID: p.ID, Status: "success", Amount: p.Amount, Currency: "USDT"}); e == nil {
		t.Fatal("unverified callback accepted")
	}
	if e := f.db.Exec("CREATE TRIGGER fail_fixture_settlement BEFORE UPDATE OF status ON payments WHEN NEW.status='success' BEGIN SELECT RAISE(ABORT,'synthetic settlement failure'); END").Error; e != nil {
		t.Fatal(e)
	}
	if _, e := submit(f, p, "0x"+strings.Repeat("d", 64)); e == nil {
		t.Fatal("write fault not surfaced")
	}
	assertUnpaid(t, f, p)
	var n int64
	f.db.Model(&domain.ChainReceipt{}).Count(&n)
	if n != 0 {
		t.Fatal("failed settlement consumed receipt")
	}
	f.db.Exec("DROP TRIGGER fail_fixture_settlement")
	due(f, p.ID)
	if e := f.service.RunChainChecks(context.Background()); e != nil {
		t.Fatal(e)
	}
	if intentFor(t, f, p.ID).VerificationStatus != "verified" {
		t.Fatal("retry after rollback failed")
	}
}
func TestLateAndClosedPaymentsRemainReviewWithoutWalletCredit(t *testing.T) {
	for _, recharge := range []bool{false, true} {
		t.Run(fmt.Sprint(recharge), func(t *testing.T) {
			f := setup(t)
			var p *domain.Payment
			if recharge {
				r, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(20)), Currency: "USDT"})
				if e != nil {
					t.Fatal(e)
				}
				p = r.Payment
			} else {
				p = f.order(t)
			}
			v := intentFor(t, f, p.ID)
			hash := "0x" + strings.Repeat("e", 64)
			f.verifier.evidence = &contract.ChainEvidence{TxID: hash, AmountUnits: v.ExpectedUnits, Recipient: v.Recipient, PaidAt: v.ExpiresAt.Add(time.Second), BlockNumber: 101, FinalizedBlockNumber: 102, BlockHash: "block", LogIndex: 1}
			if _, e := submit(f, p, hash); e != nil {
				t.Fatal(e)
			}
			assertUnpaid(t, f, p)
			if intentFor(t, f, p.ID).VerificationStatus != "review_required" {
				t.Fatal("late transfer did not require review")
			}
			if _, e := submit(f, p, "0x"+strings.Repeat("f", 64)); e == nil {
				t.Fatal("confirmed transfer hash overwritten")
			}
		})
	}
	f := setup(t)
	p := f.order(t)
	f.db.Model(&orderdomain.Order{}).Where("id = ?", p.OrderID).Update("status", "canceled")
	if _, e := submit(f, p, "0x"+strings.Repeat("f", 64)); e != nil {
		t.Fatal(e)
	}
	assertUnpaid(t, f, p)
	if intentFor(t, f, p.ID).VerificationStatus != "review_required" {
		t.Fatal("closed order was fulfilled")
	}
}
func TestOneEventCannotCreditTwoOrders(t *testing.T) {
	f := setup(t)
	a, b := f.order(t), f.order(t)
	hash := "0x" + strings.Repeat("9", 64)
	var wg sync.WaitGroup
	for _, p := range []*domain.Payment{a, b} {
		wg.Add(1)
		go func(p *domain.Payment) { defer wg.Done(); _, _ = submit(f, p, hash) }(p)
	}
	wg.Wait()
	var n int64
	f.db.Model(&domain.ChainReceipt{}).Count(&n)
	if n != 1 {
		t.Fatalf("receipts=%d", n)
	}
	f.db.Model(&domain.Payment{}).Where("status='success'").Count(&n)
	if n != 1 {
		t.Fatalf("payments=%d", n)
	}
}
func TestOldBlockAndWrongFactsCannotSettle(t *testing.T) {
	for _, fault := range []string{"old_block", "unfinalized", "amount", "recipient", "old_time"} {
		t.Run(fault, func(t *testing.T) {
			f := setup(t)
			p := f.order(t)
			v := intentFor(t, f, p.ID)
			hash := "0x" + strings.Repeat("8", 64)
			proof := &contract.ChainEvidence{TxID: hash, AmountUnits: v.ExpectedUnits, Recipient: v.Recipient, PaidAt: time.Now(), BlockNumber: 101, FinalizedBlockNumber: 102, BlockHash: "block", LogIndex: 1}
			switch fault {
			case "old_block":
				proof.BlockNumber = 100
			case "unfinalized":
				proof.FinalizedBlockNumber = 100
			case "amount":
				proof.AmountUnits = "1"
			case "recipient":
				proof.Recipient = "foreign"
			case "old_time":
				proof.PaidAt = v.CreatedAt.Add(-time.Minute)
			}
			f.verifier.evidence = proof
			if _, e := submit(f, p, hash); e == nil {
				t.Fatal("invalid receipt accepted")
			}
			assertUnpaid(t, f, p)
		})
	}
}
func TestRejectedHashCanBeCorrected(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	if _, e := submit(f, p, "not a hash"); e == nil {
		t.Fatal("malformed hash accepted")
	}
	f.verifier.kind = "mismatch"
	if _, e := submit(f, p, "0x"+strings.Repeat("7", 64)); e != nil {
		t.Fatal(e)
	}
	if intentFor(t, f, p.ID).VerificationStatus != "rejected" {
		t.Fatal("mismatch not rejected")
	}
	f.verifier.kind = ""
	if _, e := submit(f, p, "0x"+strings.Repeat("6", 64)); e != nil {
		t.Fatal(e)
	}
	if intentFor(t, f, p.ID).VerificationStatus != "verified" {
		t.Fatal("corrected hash not checked")
	}
}
