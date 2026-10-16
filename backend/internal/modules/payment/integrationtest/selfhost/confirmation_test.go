package selfhost_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	carddomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	coupondomain "github.com/dujiao-next/internal/modules/coupon/domain"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	ordercontract "github.com/dujiao-next/internal/modules/order/contract"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	app "github.com/dujiao-next/internal/modules/payment/application"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	presenter "github.com/dujiao-next/internal/modules/payment/transport/presenter"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

type confirmationQueue struct {
	delays         []time.Duration
	rechargeDelays []time.Duration
	fail           bool
}

func (*confirmationQueue) Enabled() bool { return true }
func (q *confirmationQueue) EnqueueTimeoutCancel(_ uint, d time.Duration) error {
	q.delays = append(q.delays, d)
	return nil
}
func (*confirmationQueue) EnqueueStatusEmail(uint, string) error { return nil }
func confirmationOrderService(t *testing.T, f *fixture, q *confirmationQueue, store ordercontract.Store) *orderapp.OrderService {
	t.Helper()
	if e := f.db.AutoMigrate(&carddomain.Batch{}, &carddomain.Secret{}, &coupondomain.Coupon{}, &coupondomain.CouponUsage{}); e != nil {
		t.Fatal(e)
	}
	if store == nil {
		store = orderstore.New(f.db, "synthetic-guest-secret-only")
	}
	return orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: store, Queue: q})
}
func backdateConfirmation(t *testing.T, f *fixture, p *domain.Payment, deadline time.Time) domain.ChainIntent {
	t.Helper()
	created := deadline.Add(-15 * time.Minute)
	if e := f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", p.ID).Updates(map[string]any{"created_at": created, "expires_at": deadline}).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.db.Model(&domain.Payment{}).Where("id = ?", p.ID).Updates(map[string]any{"created_at": created, "expired_at": deadline}).Error; e != nil {
		t.Fatal(e)
	}
	if p.OrderID > 0 {
		if e := f.db.Model(&orderdomain.Order{}).Where("id = ?", p.OrderID).Updates(map[string]any{"created_at": created, "expires_at": deadline}).Error; e != nil {
			t.Fatal(e)
		}
	}
	return intentFor(t, f, p.ID)
}
func confirmationEvidence(v domain.ChainIntent, paid time.Time) *contract.ChainEvidence {
	return &contract.ChainEvidence{TxID: "0x" + strings.Repeat("8", 64), AmountUnits: v.ExpectedUnits, Recipient: v.Recipient, PaidAt: paid, BlockNumber: 101, FinalizedBlockNumber: 102, BlockHash: "fixture-final", LogIndex: 1}
}
func TestConfirmationGraceOrderAndRecharge(t *testing.T) {
	for _, recharge := range []bool{false, true} {
		for _, scenario := range []string{"on_time", "late_transfer", "grace_expired", "closed", "refunded"} {
			name := scenario
			if recharge {
				name = "recharge_" + name
			}
			t.Run(name, func(t *testing.T) {
				f := setup(t)
				q := &confirmationQueue{}
				orders := confirmationOrderService(t, f, q, nil)
				var p *domain.Payment
				if recharge {
					r, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(20)), Currency: "USDT", Context: context.Background()})
					if e != nil {
						t.Fatal(e)
					}
					p = r.Payment
				} else {
					p = f.order(t)
				}
				deadline := time.Now().Add(-time.Minute)
				if scenario == "grace_expired" {
					deadline = time.Now().Add(-31 * time.Minute)
				}
				v := backdateConfirmation(t, f, p, deadline)
				paid := deadline.Add(-time.Second)
				if scenario == "late_transfer" {
					paid = deadline.Add(time.Second)
				}
				f.verifier.evidence = confirmationEvidence(v, paid)
				if scenario == "closed" || scenario == "refunded" {
					if recharge {
						status := "failed"
						if scenario == "refunded" {
							status = "expired"
						}
						if e := f.db.Model(&walletdomain.RechargeOrder{}).Where("payment_id = ?", p.ID).Update("status", status).Error; e != nil {
							t.Fatal(e)
						}
					} else if scenario == "closed" {
						if _, e := orders.CancelOrder(p.OrderID, f.user.ID); e != nil {
							t.Fatal(e)
						}
					} else {
						if e := f.db.Model(&orderdomain.Order{}).Where("id = ?", p.OrderID).Update("status", "refunded").Error; e != nil {
							t.Fatal(e)
						}
					}
				} else if recharge {
					if _, e := f.service.ExpireWalletRechargePayment(p.ID); e != nil {
						t.Fatal(e)
					}
				} else {
					o, e := orders.CancelExpiredOrder(p.OrderID)
					if e != nil {
						t.Fatal(e)
					}
					if scenario != "grace_expired" {
						if o.Status != "pending_payment" || len(q.delays) != 1 || q.delays[0] <= 0 || q.delays[0] > 30*time.Minute {
							t.Fatalf("not bounded defer: %+v %v", o, q.delays)
						}
					}
				}
				fresh, e := f.service.GetPayment(p.ID)
				if e != nil {
					t.Fatal(e)
				}
				projection := presenter.ExtractChainPaymentFields(fresh)
				if projection.CanTransfer == nil || *projection.CanTransfer {
					t.Fatal("grace reopened transfers")
				}
				if scenario == "on_time" && projection.VerificationStatus != "confirming" {
					t.Fatalf("missing confirmation display %+v", projection)
				}
				result, e := submit(f, p, f.verifier.evidence.TxID)
				if e != nil {
					t.Fatal(e)
				}
				want := "review_required"
				if scenario == "on_time" {
					want = "verified"
				}
				if got := intentFor(t, f, p.ID).VerificationStatus; got != want {
					t.Fatalf("got %s want %s", got, want)
				}
				if scenario == "on_time" {
					if result.Status != "success" {
						t.Fatal(result.Status)
					}
					for range 3 {
						if _, e := submit(f, p, f.verifier.evidence.TxID); e != nil {
							t.Fatal(e)
						}
					}
				} else {
					assertUnpaid(t, f, p)
				}
				var count int64
				f.db.Model(&domain.ChainReceipt{}).Count(&count)
				if count != 1 {
					t.Fatalf("receipts=%d", count)
				}
				var credits int64
				f.db.Model(&walletdomain.Transaction{}).Count(&credits)
				if recharge && scenario == "on_time" {
					if credits != 1 {
						t.Fatalf("credits=%d", credits)
					}
				} else if credits != 0 {
					t.Fatalf("unexpected credits=%d", credits)
				}
				if !recharge {
					var o orderdomain.Order
					f.db.First(&o, p.OrderID)
					if scenario == "on_time" && o.Status != "paid" {
						t.Fatal(o.Status)
					}
					if scenario != "on_time" && o.Status == "paid" {
						t.Fatal("closed or late order paid")
					}
				}
			})
		}
	}
}

// Pause cancellation after its non-locking read, then settle in a real DB
// transaction. The stale cancellation must re-read its locked row and stop.
type gatedCancelStore struct {
	ordercontract.Store
	entered, release chan struct{}
}

func (s *gatedCancelStore) WithinTransaction(fn func(ordercontract.Transaction) error) error {
	close(s.entered)
	<-s.release
	return s.Store.WithinTransaction(fn)
}
func TestConfirmationSettlementWinsStaleCancellation(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	v := backdateConfirmation(t, f, p, time.Now().Add(-time.Minute))
	f.verifier.evidence = confirmationEvidence(v, v.ExpiresAt.Add(-time.Second))
	gate := &gatedCancelStore{Store: orderstore.New(f.db, "synthetic-guest-secret-only"), entered: make(chan struct{}), release: make(chan struct{})}
	svc := confirmationOrderService(t, f, &confirmationQueue{}, gate)
	done := make(chan error, 1)
	go func() { _, e := svc.CancelOrder(p.OrderID, f.user.ID); done <- e }()
	<-gate.entered
	_, err := submit(f, p, f.verifier.evidence.TxID)
	close(gate.release)
	if err != nil {
		t.Fatal(err)
	}
	if e := <-done; e == nil {
		t.Fatal("stale cancellation succeeded")
	}
	var o orderdomain.Order
	f.db.First(&o, p.OrderID)
	if o.Status != "paid" || o.CanceledAt != nil {
		t.Fatalf("settlement overwritten %+v", o)
	}
}

func TestConfirmationRequiresAnExistingValidIntent(t *testing.T) {
	for _, bad := range []string{"missing", "superseded", "underpaid", "mismatched_order", "late_created"} {
		t.Run(bad, func(t *testing.T) {
			f := setup(t)
			p := f.order(t)
			v := backdateConfirmation(t, f, p, time.Now().Add(-time.Minute))
			svc := confirmationOrderService(t, f, &confirmationQueue{}, nil)
			switch bad {
			case "missing":
				f.db.Delete(&domain.ChainIntent{}, "payment_id = ?", p.ID)
			case "superseded":
				f.db.Model(p).Update("superseded_at", time.Now())
			case "underpaid":
				f.db.Model(&orderdomain.Order{}).Where("id = ?", p.OrderID).Update("total_amount", 30)
			case "mismatched_order":
				f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", p.ID).Update("order_id", p.OrderID+100)
			case "late_created":
				f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", p.ID).Update("created_at", v.ExpiresAt.Add(time.Second))
			}
			o, e := svc.CancelExpiredOrder(p.OrderID)
			if e != nil {
				t.Fatal(e)
			}
			if o.Status != "canceled" {
				t.Fatal("invalid intent deferred cancellation")
			}
		})
	}
}

func TestConfirmationLazyOrderReadWaitsOnlyThroughGrace(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	deadline := time.Now().Add(-time.Minute)
	backdateConfirmation(t, f, p, deadline)
	svc := confirmationOrderService(t, f, &confirmationQueue{}, nil)
	o, e := svc.GetOrderByUser(p.OrderID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	if o.Status != "pending_payment" {
		t.Fatal("read prematurely canceled")
	}
	backdateConfirmation(t, f, p, time.Now().Add(-31*time.Minute))
	o, e = svc.GetOrderByUser(p.OrderID, f.user.ID)
	if e != nil {
		t.Fatal(e)
	}
	if o.Status != "canceled" {
		t.Fatal("read failed to close after grace")
	}
}

func TestConfirmationPendingFinalityThenExplicitCancellationCannotSettle(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	v := backdateConfirmation(t, f, p, time.Now().Add(-time.Minute))
	f.verifier.kind = "pending"
	hash := confirmationEvidence(v, v.ExpiresAt.Add(-time.Second)).TxID
	if _, e := submit(f, p, hash); e != nil {
		t.Fatal(e)
	}
	svc := confirmationOrderService(t, f, &confirmationQueue{}, nil)
	if _, e := svc.CancelOrder(p.OrderID, f.user.ID); e != nil {
		t.Fatal(e)
	}
	f.verifier.kind = ""
	f.verifier.evidence = confirmationEvidence(v, v.ExpiresAt.Add(-time.Second))
	due(f, p.ID)
	if e := f.service.RunChainChecks(context.Background()); e != nil {
		t.Fatal(e)
	}
	assertUnpaid(t, f, p)
	if intentFor(t, f, p.ID).VerificationStatus != "review_required" {
		t.Fatal("cancellation bypassed")
	}
	var o orderdomain.Order
	f.db.First(&o, p.OrderID)
	if o.Status != "canceled" {
		t.Fatal(o.Status)
	}
}

func (*confirmationQueue) EnqueueOrderAutoFulfill(uint) error                    { return nil }
func (*confirmationQueue) EnqueueBotNotification(contract.BotNotification) error { return nil }
func (q *confirmationQueue) EnqueueWalletRechargeExpire(_ uint, delay time.Duration) error {
	q.rechargeDelays = append(q.rechargeDelays, delay)
	if q.fail {
		return errors.New("fixture queue unavailable")
	}
	return nil
}
func TestConfirmationRechargeExpiryActuallyEnqueuedAndReusedOnce(t *testing.T) {
	q := &confirmationQueue{}
	f := setupWithQueue(t, q)
	input := app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(20)), Currency: "USDT"}
	r, e := f.service.CreateWalletRechargePayment(input)
	if e != nil {
		t.Fatal(e)
	}
	if len(q.rechargeDelays) != 1 || q.rechargeDelays[0] < 14*time.Minute || q.rechargeDelays[0] > 15*time.Minute {
		t.Fatalf("initial expiry not queued %v", q.rechargeDelays)
	}
	again, e := f.service.CreateWalletRechargePayment(input)
	if e != nil {
		t.Fatal(e)
	}
	if again.Payment.ID != r.Payment.ID || len(q.rechargeDelays) != 1 {
		t.Fatal("reuse duplicated expiry")
	}
	backdateConfirmation(t, f, r.Payment, time.Now().Add(-time.Minute))
	if _, e = f.service.ExpireWalletRechargePayment(r.Payment.ID); e != nil {
		t.Fatal(e)
	}
	if len(q.rechargeDelays) != 2 || q.rechargeDelays[1] < 28*time.Minute || q.rechargeDelays[1] > 30*time.Minute {
		t.Fatalf("grace expiry not queued %v", q.rechargeDelays)
	}
	backdateConfirmation(t, f, r.Payment, time.Now().Add(-31*time.Minute))
	expired, e := f.service.ExpireWalletRechargePayment(r.Payment.ID)
	if e != nil {
		t.Fatal(e)
	}
	if expired.Status != "expired" || len(q.rechargeDelays) != 2 {
		t.Fatal("grace did not terminate")
	}
}
func TestConfirmationRechargeQueueFailureCannotExposeReservedInvoice(t *testing.T) {
	q := &confirmationQueue{fail: true}
	f := setupWithQueue(t, q)
	_, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(20)), Currency: "USDT"})
	if !errors.Is(e, app.ErrQueueUnavailable) {
		t.Fatalf("got %v", e)
	}
	var intents int64
	f.db.Model(&domain.ChainIntent{}).Count(&intents)
	if intents != 0 {
		t.Fatal("payable invoice committed without expiry task")
	}
	var p domain.Payment
	f.db.First(&p)
	if p.Status != "failed" {
		t.Fatal(p.Status)
	}
	var r walletdomain.RechargeOrder
	f.db.First(&r)
	if r.Status != "failed" {
		t.Fatal(r.Status)
	}
}
