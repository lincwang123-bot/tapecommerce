package selfhost_test

import (
	"context"
	"github.com/dujiao-next/internal/constants"
	app "github.com/dujiao-next/internal/modules/payment/application"
	"github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/modules/payment/domain"
	wallet "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func (v *verifier) Discover(_ context.Context, _ string, r contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
	if v.discover != nil {
		return v.discover(r)
	}
	return &contract.ChainDiscoverPage{Done: true}, nil
}
func discoveryDue(t *testing.T, f *fixture, id uint) {
	t.Helper()
	if e := f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", id).Updates(map[string]any{"discovery_next_at": nil, "discovery_lease_until": nil}).Error; e != nil {
		t.Fatal(e)
	}
}
func TestDiscoveryNoHashOrderRechargeAndRace(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	f.verifier.discover = func(r contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		return &contract.ChainDiscoverPage{TxHashes: []string{"0x" + strings.Repeat("a", 64)}, NextCursor: "101", Done: true}, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	v := intentFor(t, f, p.ID)
	if v.VerificationStatus != "verified" {
		t.Fatal(v.VerificationStatus)
	}
	var n int64
	f.db.Model(&domain.ChainReceipt{}).Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
	r, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(30)), Currency: "USDT", Context: context.Background()})
	if e != nil {
		t.Fatal(e)
	}
	f.verifier.discover = func(r contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		return &contract.ChainDiscoverPage{TxHashes: []string{"0x" + strings.Repeat("b", 64)}, Done: true}, nil
	}
	for range 3 {
		if _, e = f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
			t.Fatal(e)
		}
	}
	if intentFor(t, f, r.Payment.ID).VerificationStatus != "verified" {
		t.Fatal("recharge not verified")
	}
	var txs []wallet.Transaction
	f.db.Find(&txs)
	if len(txs) != 1 || txs[0].Amount.String() != "30.00" {
		t.Fatalf("%+v", txs)
	}
}
func TestDiscoveryRPCPageAndWrongManualHashRecovery(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	f.verifier.kind = "pending"
	if _, e := submit(f, p, "0x"+strings.Repeat("c", 64)); e != nil {
		t.Fatal(e)
	}
	// Wrong manual hash is in long retry delay; discovery must still verify its candidate.
	f.verifier.kind = ""
	f.verifier.discover = func(r contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		return nil, &contract.ChainError{Kind: "unavailable", Code: "fixture_rpc"}
	}
	report, e := f.service.RunChainDiscovery(context.Background(), "bsc")
	if e != nil || report.Failed != 1 {
		t.Fatal(report, e)
	}
	if v := intentFor(t, f, p.ID); v.DiscoveryCursor != "" || v.DiscoveryCode != "fixture_rpc" {
		t.Fatal(v)
	}
	discoveryDue(t, f, p.ID)
	f.verifier.discover = func(r contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		if r.Cursor != "" {
			t.Fatal(r.Cursor)
		}
		return &contract.ChainDiscoverPage{NextCursor: "page2"}, nil
	}
	if _, e = f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
		t.Fatal(e)
	}
	discoveryDue(t, f, p.ID)
	f.verifier.discover = func(r contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		if r.Cursor != "page2" {
			t.Fatal(r.Cursor)
		}
		return &contract.ChainDiscoverPage{TxHashes: []string{"0x" + strings.Repeat("d", 64)}, Done: true}, nil
	}
	if _, e = f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
		t.Fatal(e)
	}
	if v := intentFor(t, f, p.ID); v.VerificationStatus != "verified" || v.SubmittedHash != "0x"+strings.Repeat("d", 64) {
		t.Fatal(v)
	}
}
func TestDiscoveryExpiredClosedGoesToReview(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	past := time.Now().Add(-time.Hour)
	f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", p.ID).Update("expires_at", past)
	f.verifier.discover = func(contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		return &contract.ChainDiscoverPage{TxHashes: []string{"0x" + strings.Repeat("e", 64)}, Done: true}, nil
	}
	if _, e := f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
		t.Fatal(e)
	}
	if v := intentFor(t, f, p.ID); v.VerificationStatus != "review_required" {
		t.Fatal(v)
	}
}
func TestDiscoveryPendingCandidateRetainsPage(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	f.verifier.kind = "unavailable"
	f.verifier.discover = func(contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		return &contract.ChainDiscoverPage{TxHashes: []string{"0x" + strings.Repeat("f", 64)}, NextCursor: "must-not-advance", Done: true}, nil
	}
	if _, e := f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
		t.Fatal(e)
	}
	if v := intentFor(t, f, p.ID); v.DiscoveryCursor != "" || v.DiscoveryCode != "candidate_retry" {
		t.Fatal(v)
	}
	discoveryDue(t, f, p.ID)
	f.verifier.kind = ""
	if _, e := f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
		t.Fatal(e)
	}
	if v := intentFor(t, f, p.ID); v.VerificationStatus != "verified" {
		t.Fatal(v)
	}
}

func TestDiscoveryClosedOrderCannotReopenAndOldInvoicesStop(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	if e := f.db.Table("orders").Where("id = ?", p.OrderID).Update("status", constants.OrderStatusCanceled).Error; e != nil {
		t.Fatal(e)
	}
	f.verifier.discover = func(contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		return &contract.ChainDiscoverPage{TxHashes: []string{"0x" + strings.Repeat("1", 64)}, Done: true}, nil
	}
	if _, e := f.service.RunChainDiscovery(context.Background(), "bsc"); e != nil {
		t.Fatal(e)
	}
	if v := intentFor(t, f, p.ID); v.VerificationStatus != "review_required" {
		t.Fatal(v)
	}
	old := f.order(t)
	f.db.Model(&domain.ChainIntent{}).Where("payment_id = ?", old.ID).Update("expires_at", time.Now().Add(-8*24*time.Hour))
	report, e := f.service.RunChainDiscovery(context.Background(), "bsc")
	if e != nil || report.Code != "idle" || report.Scanned != 0 {
		t.Fatal(report, e)
	}
	if v := intentFor(t, f, old.ID); v.VerificationStatus != "awaiting_hash" {
		t.Fatal(v)
	}
}

func TestDiscoveryColumnsMigrateExistingInvoice(t *testing.T) {
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.sqlite")), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sql, _ := db.DB()
	defer sql.Close()
	if e = db.Exec("CREATE TABLE chain_payment_intents (payment_id integer PRIMARY KEY, network text NOT NULL, token_contract text NOT NULL, recipient text NOT NULL, expected_units text NOT NULL)").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Exec("INSERT INTO chain_payment_intents(payment_id,network,token_contract,recipient,expected_units) VALUES(1,'bsc','contract','recipient','200001')").Error; e != nil {
		t.Fatal(e)
	}
	if e = db.AutoMigrate(&domain.ChainIntent{}); e != nil {
		t.Fatal(e)
	}
	for _, column := range []string{"discovery_cursor", "discovery_code", "discovery_next_at", "discovery_lease", "discovery_lease_until", "discovery_updated_at"} {
		if !db.Migrator().HasColumn(&domain.ChainIntent{}, column) {
			t.Fatal(column)
		}
	}
	var v domain.ChainIntent
	if e = db.First(&v, 1).Error; e != nil {
		t.Fatal(e)
	}
	if v.ExpectedUnits != "200001" || v.DiscoveryCursor != "" {
		t.Fatal(v)
	}
}

func TestDiscoveryOutageStopsBatchWithoutAdvancingOtherInvoices(t *testing.T) {
	f := setup(t)
	first := f.order(t)
	second := f.order(t)
	calls := 0
	f.verifier.discover = func(contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError) {
		calls++
		return nil, &contract.ChainError{Kind: "unavailable", Code: "rpc_unavailable"}
	}
	report, err := f.service.RunChainDiscovery(context.Background(), "bsc")
	if err != nil || report.Failed != 1 || calls != 1 {
		t.Fatal(report, err, calls)
	}
	a, b := intentFor(t, f, first.ID), intentFor(t, f, second.ID)
	if a.DiscoveryCursor != "" || b.DiscoveryCursor != "" || b.DiscoveryNextAt != nil {
		t.Fatal("outage advanced unscanned invoice", a, b)
	}
}
