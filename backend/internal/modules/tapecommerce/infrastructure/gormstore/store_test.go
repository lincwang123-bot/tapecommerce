package gormstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dujiao-next/internal/modules/tapecommerce/application"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func setup(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "isolated.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sql, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sql.Close(); err != nil {
			t.Error(err)
		}
	})
	s := New(db)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, db
}
func hash(c string) string    { return "0x" + strings.Repeat(c, 64) }
func address(c string) string { return "0x" + strings.Repeat(c, 40) }
func inv() domain.Invoice {
	now := uint64(time.Now().UnixMilli())
	return domain.Invoice{InvoiceID: hash("1"), ShopID: "shop-1", OrderID: 1, ChainID: 196, Checkout: address("2"), TenantHash: hash("3"), Payer: address("4"), Merchant: address("5"), AmountWei: "1000000000000000000", Deadline: now/1000 + 60, FXSnapshot: domain.FXSnapshot{PolicyVersion: domain.FXPolicyVersion, Source: domain.FXSource, InstrumentID: domain.FXInstrument, PriceField: domain.FXPriceField, RateUSDTPerOKB: "100.0000", AmountUSDT: "100.00", SourceTimestampMS: now, FetchedAtMS: now, IssuedAtMS: now, Rounding: domain.FXRounding}}
}
func ev() domain.Evidence {
	return domain.Evidence{ChainID: 196, TxHash: hash("6"), LogIndex: 2, BlockNumber: 100, BlockHash: hash("7"), SafeBlock: 101, Safe: true}
}
func prepared(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	s, db := setup(t)
	ctx := context.Background()
	if err := s.Create(ctx, inv()); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, "shop-1", hash("1"), hash("6")); err != nil {
		t.Fatal(err)
	}
	return s, db
}

func TestImmutableQuoteAndScope(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	i := inv()
	if err := s.Create(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, i); err != nil {
		t.Fatal(err)
	}
	other := i
	other.AmountWei = "1"
	if err := s.Create(ctx, other); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mutated quote: %v", err)
	}
	other = i
	other.InvoiceID = hash("a")
	if err := s.Create(ctx, other); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same order paid twice: %v", err)
	}
	if _, err := s.Get(ctx, "other-shop", i.InvoiceID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, "other-shop", i.InvoiceID, hash("6")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDetectionSafeTransitionReplayAndLeaseRecovery(t *testing.T) {
	s, db := prepared(t)
	ctx := context.Background()
	e := ev()
	e.Safe = false
	e.SafeBlock = 99
	if err := s.Record(ctx, "shop-1", hash("1"), e); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&outboxRecord{}).Count(&count)
	if count != 0 {
		t.Fatal("unsafe event created fulfillment job")
	}
	if err := s.Record(ctx, "shop-1", hash("1"), ev()); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, "shop-1", hash("1"), ev()); err != nil {
		t.Fatal(err)
	}
	db.Model(&eventRecord{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
	db.Model(&outboxRecord{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
	now := time.Now()
	a, err := s.Claim(ctx, "shop-1", hash("1"), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "shop-1", hash("1"), now, time.Minute); !errors.Is(err, domain.ErrLease) {
		t.Fatal(err)
	}
	b, err := s.Claim(ctx, "shop-1", hash("1"), now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, a, true, "", time.Time{}); !errors.Is(err, domain.ErrLease) {
		t.Fatalf("stale lease accepted: %v", err)
	}
	if err := s.Finish(ctx, b, true, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, "shop-1", hash("1"))
	if err != nil || p.State != domain.Delivered {
		t.Fatalf("%+v %v", p, err)
	}
}

type verifier struct {
	e   domain.Evidence
	err error
}

func (v verifier) Verify(context.Context, domain.Invoice, string) (domain.Evidence, error) {
	return v.e, v.err
}

// admission is a controlled test-only port. It holds the real invoice/outbox
// transaction locks, but its delivery effect is an in-memory counter, not stock.
// It is not a production bridge to the existing CreateAuto transaction.
type admission struct {
	calls  atomic.Int32
	err    error
	store  *Store
	before func(context.Context, domain.Job) error
}

func (d *admission) AdmitAndFulfill(ctx context.Context, job domain.Job) error {
	if d.before != nil {
		if err := d.before(ctx, job); err != nil {
			return err
		}
	}
	return d.store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := load(tx, job.ShopID, job.InvoiceID, true)
		if err != nil {
			return err
		}
		if r.State != domain.FulfillmentPending || r.OrderID != job.OrderID {
			return domain.ErrReview
		}
		var event eventRecord
		if err := tx.Where("invoice_id = ?", r.InvoiceID).First(&event).Error; err != nil {
			return err
		}
		if !event.Safe {
			return domain.ErrReview
		}
		var outbox outboxRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("invoice_id = ?", r.InvoiceID).First(&outbox).Error; err != nil {
			return err
		}
		if outbox.State != "processing" || outbox.Key != job.Key || outbox.Lease != job.Lease || !outbox.LeaseUntil.After(time.Now()) {
			return domain.ErrLease
		}
		d.calls.Add(1)
		return d.err
	})
}

func TestConcurrentOutboxDeliversOnce(t *testing.T) {
	s, db := prepared(t)
	ctx := context.Background()
	d := &admission{store: s}
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{e: ev()}, Admission: d}
	var wg sync.WaitGroup
	for n := 0; n < 24; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.DeliverOne(ctx, hash("1"))
			if err != nil && !errors.Is(err, domain.ErrLease) && !errors.Is(err, domain.ErrConflict) {
				t.Errorf("delivery: %v", err)
			}
		}()
	}
	wg.Wait()
	if d.calls.Load() != 1 {
		t.Fatalf("adapter called %d times", d.calls.Load())
	}
	var count int64
	db.Model(&outboxRecord{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
}

func TestRPCRecoveryAndReorgHoldInventory(t *testing.T) {
	s, _ := prepared(t)
	ctx := context.Background()
	d := &admission{store: s}
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{err: domain.ErrRPC}, Admission: d}
	if err := svc.DeliverOne(ctx, hash("1")); !errors.Is(err, domain.ErrRPC) {
		t.Fatal(err)
	}
	p, _ := s.Get(ctx, "shop-1", hash("1"))
	if p.State != domain.Submitted || d.calls.Load() != 0 {
		t.Fatal(p.State)
	}
	svc.Verifier = verifier{e: ev()}
	if _, err := svc.Check(ctx, hash("1")); err != nil {
		t.Fatal(err)
	}
	changed := ev()
	changed.BlockHash = hash("a")
	svc.Verifier = verifier{e: changed}
	if err := svc.DeliverOne(ctx, hash("1")); !errors.Is(err, domain.ErrReorg) {
		t.Fatal(err)
	}
	p, _ = s.Get(ctx, "shop-1", hash("1"))
	if p.State != domain.ReviewRequired || d.calls.Load() != 0 {
		t.Fatal(p.State)
	}
}

func TestFeatureOffAndDeliveryReview(t *testing.T) {
	if err := (application.Service{}).Submit(context.Background(), hash("1"), hash("6")); !errors.Is(err, domain.ErrDisabled) {
		t.Fatal(err)
	}
	s, _ := prepared(t)
	d := &admission{store: s, err: domain.ErrReview}
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{e: ev()}, Admission: d}
	if err := svc.DeliverOne(context.Background(), hash("1")); !errors.Is(err, domain.ErrReview) {
		t.Fatal(err)
	}
	p, _ := s.Get(context.Background(), "shop-1", hash("1"))
	if p.State != domain.ReviewRequired {
		t.Fatal(p.State)
	}
	if err := svc.DeliverOne(context.Background(), hash("1")); !errors.Is(err, domain.ErrReview) {
		t.Fatal(err)
	}
	if d.calls.Load() != 1 {
		t.Fatal(d.calls.Load())
	}
}

func TestEventCannotFundSecondInvoiceAndTransientRetrySurvivesNewStore(t *testing.T) {
	s, db := prepared(t)
	ctx := context.Background()
	if err := s.Record(ctx, "shop-1", hash("1"), ev()); err != nil {
		t.Fatal(err)
	}
	other := inv()
	other.OrderID = 2
	other.InvoiceID = hash("a")
	if err := s.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(ctx, "shop-1", other.InvoiceID, hash("6")); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, "shop-1", other.InvoiceID, ev()); !errors.Is(err, domain.ErrReorg) {
		t.Fatalf("event funded a second invoice: %v", err)
	}
	now := time.Now()
	job, err := s.Claim(ctx, "shop-1", hash("1"), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, job, false, "delivery_retry", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered := New(db)
	if _, err := recovered.Claim(ctx, "shop-1", hash("1"), now, time.Minute); !errors.Is(err, domain.ErrPending) {
		t.Fatal(err)
	}
	next, err := recovered.Claim(ctx, "shop-1", hash("1"), now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.Key != job.Key || next.Lease == job.Lease || next.Attempts != 2 {
		t.Fatalf("retry did not retain durable identity: %+v", next)
	}
}

func TestReviewPersistsOnlyErrorCategory(t *testing.T) {
	s, _ := prepared(t)
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{err: fmt.Errorf("%w: secret response", domain.ErrReceipt)}}
	if _, err := svc.Check(context.Background(), hash("1")); !errors.Is(err, domain.ErrReceipt) {
		t.Fatal(err)
	}
	p, err := s.Get(context.Background(), "shop-1", hash("1"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Reason != "receipt_mismatch" {
		t.Fatalf("unsafe persisted reason: %q", p.Reason)
	}
}

func TestDeliveryDisabledWithoutAtomicAdmission(t *testing.T) {
	s, db := prepared(t)
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{e: ev()}}
	if err := svc.DeliverOne(context.Background(), hash("1")); !errors.Is(err, domain.ErrDisabled) {
		t.Fatal(err)
	}
	p, err := s.Get(context.Background(), "shop-1", hash("1"))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != domain.Submitted {
		t.Fatalf("missing gate changed state: %s", p.State)
	}
	var count int64
	if err := db.Model(&outboxRecord{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("missing gate created job")
	}
}

func TestReviewAfterClaimRejectsStockEffect(t *testing.T) {
	s, _ := prepared(t)
	ctx := context.Background()
	d := &admission{store: s, before: func(ctx context.Context, job domain.Job) error {
		// Deterministically model a second worker committing reorg review after
		// Claim and before the delivery transaction acquires the invoice lock.
		return s.Review(ctx, job.ShopID, job.InvoiceID, "canonical_block_mismatch")
	}}
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{e: ev()}, Admission: d}
	if err := svc.DeliverOne(ctx, hash("1")); !errors.Is(err, domain.ErrReview) {
		t.Fatal(err)
	}
	if d.calls.Load() != 0 {
		t.Fatal("reviewed invoice reached delivery effect")
	}
	p, err := s.Get(ctx, "shop-1", hash("1"))
	if err != nil {
		t.Fatal(err)
	}
	if p.State != domain.ReviewRequired {
		t.Fatal(p.State)
	}
	var outbox outboxRecord
	if err := s.db.Where("invoice_id = ?", hash("1")).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.State != "review" {
		t.Fatalf("job still releasable: %s", outbox.State)
	}
}

func TestFXSnapshotPersistedAndImmutableEvenWhenWeiUnchanged(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	i := inv()
	if err := s.Create(ctx, i); err != nil {
		t.Fatal(err)
	}
	p, err := New(db).Get(ctx, i.ShopID, i.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Invoice != i || p.Invoice.FXSnapshot.RateUSDTPerOKB != "100.0000" {
		t.Fatalf("audit snapshot did not roundtrip: %+v", p.Invoice.FXSnapshot)
	}
	changed := i
	changed.FXSnapshot.AmountUSDT = "200.00"
	changed.FXSnapshot.RateUSDTPerOKB = "200.0000"
	if err := changed.Validate(); err != nil {
		t.Fatal("test snapshot must still produce same wei", err)
	}
	if err := s.Create(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed original amount/rate accepted: %v", err)
	}
	changed = i
	changed.FXSnapshot.SourceTimestampMS--
	if err := s.Create(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("changed observation timestamp accepted: %v", err)
	}
	if err := s.Create(ctx, i); err != nil {
		t.Fatal("identical persisted snapshot is not idempotent", err)
	}
}

func TestCorruptOrMissingFXSnapshotFailsClosed(t *testing.T) {
	s, db := setup(t)
	ctx := context.Background()
	i := inv()
	missing := i
	missing.FXSnapshot = domain.FXSnapshot{}
	if err := s.Create(ctx, missing); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("accepted missing FX snapshot", err)
	}
	if err := s.Create(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&invoiceRecord{}).Where("invoice_id = ?", i.InvoiceID).Update("fx_rate_usdt_per_okb", "200").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, i.ShopID, i.InvoiceID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("loaded internally inconsistent FX", err)
	}
	if err := s.Submit(ctx, i.ShopID, i.InvoiceID, hash("6")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("submitted corrupt quote", err)
	}
}

func TestCreateTrustedChecksFXFreshnessOnlyAtIssue(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	svc := application.Service{Enabled: true, ShopID: "shop-1", Store: s, Verifier: verifier{e: ev()}}
	i := inv()
	i.FXSnapshot.SourceTimestampMS -= 31000
	i.FXSnapshot.FetchedAtMS -= 31000
	i.FXSnapshot.IssuedAtMS -= 31000
	i.Deadline = i.FXSnapshot.IssuedAtMS/1000 + 60
	if err := i.Validate(); err != nil {
		t.Fatal("historically valid quote rejected", err)
	}
	if err := svc.CreateTrusted(ctx, i); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("stale new quote accepted", err)
	}
	i = inv()
	if err := svc.CreateTrusted(ctx, i); err != nil {
		t.Fatal(err)
	}
	// Public/payment release paths remain disabled without atomic admission.
	if err := svc.DeliverOne(ctx, i.InvoiceID); !errors.Is(err, domain.ErrDisabled) {
		t.Fatal(err)
	}
}
