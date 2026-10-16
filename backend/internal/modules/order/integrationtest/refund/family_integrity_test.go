package refund_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	affiliatedomain "github.com/dujiao-next/internal/modules/affiliate/domain"
	. "github.com/dujiao-next/internal/modules/order/application/refund"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	walletcontract "github.com/dujiao-next/internal/modules/wallet/contract"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func refundAmount(value int64) money.Amount { return money.FromDecimal(decimal.NewFromInt(value)) }

func refundFamilyFixture(t *testing.T) (*Service, *gorm.DB, *orderdomain.Order, []*orderdomain.Order) {
	t.Helper()
	t.Setenv("VOUCHER_BRIDGE_ENABLED", "false")
	svc, db := setupOrderRefundWalletTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	createTestUser(t, db, 501)
	root := createTestOrder(t, db, 501, "family", decimal.NewFromInt(100))
	paid := time.Now()
	root.PaidAt = &paid
	if err := db.Model(root).Updates(map[string]interface{}{"paid_at": paid, "status": constants.OrderStatusCompleted}).Error; err != nil {
		t.Fatal(err)
	}
	children := []*orderdomain.Order{
		createTestChildOrderWithFulfillmentType(t, db, root, "family-1", constants.OrderStatusCompleted, decimal.NewFromInt(60), constants.FulfillmentTypeManual, false),
		createTestChildOrderWithFulfillmentType(t, db, root, "family-2", constants.OrderStatusCompleted, decimal.NewFromInt(40), constants.FulfillmentTypeManual, false),
	}
	return svc, db, root, children
}
func checkRefundFamily(t *testing.T, db *gorm.DB, root *orderdomain.Order, children []*orderdomain.Order, total, first, second string) {
	t.Helper()
	for i, order := range append([]*orderdomain.Order{root}, children...) {
		var got orderdomain.Order
		if err := db.First(&got, order.ID).Error; err != nil {
			t.Fatal(err)
		}
		want := []string{total, first, second}[i]
		if !got.RefundedAmount.Decimal.Equal(decimal.RequireFromString(want)) {
			t.Fatalf("order %d refunded=%s want=%s", order.ID, got.RefundedAmount.String(), want)
		}
	}
}
func walletBalance(t *testing.T, db *gorm.DB) decimal.Decimal {
	t.Helper()
	var account walletdomain.Account
	if err := db.Where("user_id = ?", 501).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	return account.Balance.Decimal
}
func TestRefundFamilyFullRefundCannotBeCreditedTwice(t *testing.T) {
	for _, childFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(childFirst), func(t *testing.T) {
			svc, db, root, children := refundFamilyFixture(t)
			firstID, firstAmount, secondID, secondAmount := root.ID, int64(100), children[0].ID, int64(60)
			if childFirst {
				firstID, firstAmount, secondID, secondAmount = children[0].ID, 60, root.ID, 100
			}
			if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: firstID, Amount: refundAmount(firstAmount)}); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: secondID, Amount: refundAmount(secondAmount)}); !errors.Is(err, walletcontract.ErrRefundExceeded) {
				t.Fatalf("duplicate refund error=%v", err)
			}
			if !walletBalance(t, db).Equal(decimal.NewFromInt(firstAmount)) {
				t.Fatal("duplicate wallet credit")
			}
			if childFirst {
				if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(40)}); err != nil {
					t.Fatal(err)
				}
			}
			checkRefundFamily(t, db, root, children, "100", "60", "40")
		})
	}
}
func TestRefundFamilyPartialAllocationsAndManualWalletSharedCap(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(20)}); err != nil {
		t.Fatal(err)
	}
	checkRefundFamily(t, db, root, children, "20", "12", "8")
	if _, _, err := svc.AdminManualRefund(AdminManualRefundInput{OrderID: children[0].ID, Amount: refundAmount(48)}); err != nil {
		t.Fatal(err)
	}
	checkRefundFamily(t, db, root, children, "68", "60", "8")
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: children[0].ID, Amount: refundAmount(1)}); !errors.Is(err, walletcontract.ErrRefundExceeded) {
		t.Fatal(err)
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(32)}); err != nil {
		t.Fatal(err)
	}
	checkRefundFamily(t, db, root, children, "100", "60", "40")
	if !walletBalance(t, db).Equal(decimal.NewFromInt(52)) {
		t.Fatal("manual principal also credited to wallet")
	}
}
func TestRefundFamilyLegacyLedgerReconciledWithoutDoubleCounting(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	// Legacy release recorded 20 on the parent and 10 on the first child separately.
	for _, fixture := range []struct {
		id     uint
		amount int64
	}{{root.ID, 20}, {children[0].ID, 10}} {
		if err := db.Model(&orderdomain.Order{}).Where("id = ?", fixture.id).Update("refunded_amount", refundAmount(fixture.amount)).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&orderdomain.OrderRefundRecord{OrderID: fixture.id, UserID: 501, Type: constants.OrderRefundTypeManual, Amount: refundAmount(fixture.amount), Currency: "CNY"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(71)}); !errors.Is(err, walletcontract.ErrRefundExceeded) {
		t.Fatal(err)
	}
	// Rejected attempts do not rewrite legacy counters.
	checkRefundFamily(t, db, root, children, "20", "10", "0")
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(70)}); err != nil {
		t.Fatal(err)
	}
	checkRefundFamily(t, db, root, children, "100", "60", "40")
	if !walletBalance(t, db).Equal(decimal.NewFromInt(70)) {
		t.Fatal("legacy refunds credited again")
	}
}
func TestRefundIdempotencyReusesOriginalRecordAndRejectsChangedInput(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	input := AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(10), Remark: "retry fixture", IdempotencyKey: "refund-wallet-001"}
	_, txn1, r1, err := svc.AdminRefundToWallet(input)
	if err != nil {
		t.Fatal(err)
	}
	_, txn2, r2, err := svc.AdminRefundToWallet(input)
	if err != nil {
		t.Fatal(err)
	}
	if txn1.ID != txn2.ID || r1.ID != r2.ID || !walletBalance(t, db).Equal(decimal.NewFromInt(10)) {
		t.Fatal("retry created another refund")
	}
	for _, mutation := range []func(*AdminRefundToWalletInput){func(i *AdminRefundToWalletInput) { i.Amount = refundAmount(11) }, func(i *AdminRefundToWalletInput) { i.OrderID = children[0].ID }, func(i *AdminRefundToWalletInput) { i.Remark = "changed" }} {
		changed := input
		mutation(&changed)
		if _, _, _, err := svc.AdminRefundToWallet(changed); !errors.Is(err, walletcontract.ErrInvalidAmount) {
			t.Fatalf("changed key accepted: %v", err)
		}
	}
	if _, _, err := svc.AdminManualRefund(AdminManualRefundInput{OrderID: root.ID, Amount: refundAmount(10), Remark: input.Remark, IdempotencyKey: input.IdempotencyKey}); !errors.Is(err, walletcontract.ErrInvalidAmount) {
		t.Fatal(err)
	}
	manual := AdminManualRefundInput{OrderID: root.ID, Amount: refundAmount(10), IdempotencyKey: "refund-manual-002"}
	_, mr1, err := svc.AdminManualRefund(manual)
	if err != nil {
		t.Fatal(err)
	}
	_, mr2, err := svc.AdminManualRefund(manual)
	if err != nil {
		t.Fatal(err)
	}
	if mr1.ID != mr2.ID {
		t.Fatal("manual retry created duplicate record")
	}
	input.IdempotencyKey = "refund-wallet-003"
	if _, _, _, err := svc.AdminRefundToWallet(input); err != nil {
		t.Fatal(err)
	}
	checkRefundFamily(t, db, root, children, "30", "18", "12")
}
func TestConcurrentParentChildRefundsCannotExceedPaidAmount(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id, amount := root.ID, int64(100)
			if i%2 == 1 {
				id, amount = children[0].ID, 60
			}
			_, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: id, Amount: refundAmount(amount), IdempotencyKey: fmt.Sprintf("parallel-refund-%d", i)})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, walletcontract.ErrRefundExceeded) && !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
			t.Fatalf("unexpected concurrency error=%v", err)
		}
	}
	if success != 1 {
		t.Fatalf("successful conflicting refunds=%d", success)
	}
	balance := walletBalance(t, db)
	if balance.GreaterThan(decimal.NewFromInt(100)) {
		t.Fatalf("balance=%s", balance)
	}
	var records int64
	if err := db.Model(&orderdomain.OrderRefundRecord{}).Count(&records).Error; err != nil {
		t.Fatal(err)
	}
	if records != 1 {
		t.Fatalf("refund records=%d", records)
	}
}
func TestConcurrentIdempotentRefundRetryCreditsOnce(t *testing.T) {
	svc, db, root, _ := refundFamilyFixture(t)
	input := AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(10), IdempotencyKey: "parallel-same-refund"}
	start := make(chan struct{})
	results := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, _, _, err := svc.AdminRefundToWallet(input); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
			t.Fatal(err)
		}
	}
	if _, _, _, err := svc.AdminRefundToWallet(input); err != nil {
		t.Fatal(err)
	}
	if !walletBalance(t, db).Equal(decimal.NewFromInt(10)) {
		t.Fatal("parallel retry credited twice")
	}
	var records int64
	if err := db.Model(&orderdomain.OrderRefundRecord{}).Count(&records).Error; err != nil {
		t.Fatal(err)
	}
	if records != 1 {
		t.Fatalf("records=%d", records)
	}
}

func TestRefundRecordFailureRollsBackWalletAndFamilyAndCanRetry(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	failed := errors.New("synthetic refund record storage failure")
	if err := db.Callback().Create().Before("gorm:create").Register("refund-test-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "order_refund_records" {
			tx.AddError(failed)
		}
	}); err != nil {
		t.Fatal(err)
	}
	input := AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(10), IdempotencyKey: "rollback-refund-001"}
	if _, _, _, err := svc.AdminRefundToWallet(input); err == nil {
		t.Fatal("refund did not fail")
	}
	checkRefundFamily(t, db, root, children, "0", "0", "0")
	var transactions int64
	if err := db.Model(&walletdomain.Transaction{}).Count(&transactions).Error; err != nil {
		t.Fatal(err)
	}
	if transactions != 0 {
		t.Fatal("wallet credit survived failed record")
	}
	if err := db.Callback().Create().Remove("refund-test-failure"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.AdminRefundToWallet(input); err != nil {
		t.Fatal(err)
	}
	checkRefundFamily(t, db, root, children, "10", "6", "4")
	if !walletBalance(t, db).Equal(decimal.NewFromInt(10)) {
		t.Fatal("failed refund consumed key or double credited")
	}
}

func TestRefundFamilyCentAllocationAndLastRemainder(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	for _, amount := range []string{"0.01", "0.01", "0.01", "99.97"} {
		if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: money.FromDecimal(decimal.RequireFromString(amount))}); err != nil {
			t.Fatal(err)
		}
	}
	checkRefundFamily(t, db, root, children, "100", "60", "40")
	if !walletBalance(t, db).Equal(decimal.NewFromInt(100)) {
		t.Fatal("rounding changed credited total")
	}
}

func TestChildRefundReversesRootAffiliateCommission(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	createTestUser(t, db, 502)
	profile := affiliatedomain.Profile{UserID: 502, AffiliateCode: "REFUNDROOT", Status: constants.AffiliateProfileStatusActive}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
	commission := affiliatedomain.Commission{AffiliateProfileID: profile.ID, OrderID: root.ID, CommissionType: constants.AffiliateCommissionTypeOrder, BaseAmount: refundAmount(100), RatePercent: refundAmount(50), CommissionAmount: refundAmount(50), Status: constants.AffiliateCommissionStatusAvailable}
	if err := db.Create(&commission).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: children[0].ID, Amount: refundAmount(60)}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&commission, commission.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !commission.CommissionAmount.Decimal.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("commission after child refund=%s", commission.CommissionAmount)
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(40)}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&commission, commission.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !commission.CommissionAmount.Decimal.IsZero() || commission.Status != constants.AffiliateCommissionStatusRejected {
		t.Fatalf("commission after full refund=%+v", commission)
	}
}

func TestRefundLegacyCountersWithoutCompleteRecordsFailConservatively(t *testing.T) {
	svc, db, root, children := refundFamilyFixture(t)
	for _, fixture := range []struct {
		id     uint
		amount int64
	}{{root.ID, 20}, {children[0].ID, 10}} {
		if err := db.Model(&orderdomain.Order{}).Where("id = ?", fixture.id).Update("refunded_amount", refundAmount(fixture.amount)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := svc.AdminRefundToWallet(AdminRefundToWalletInput{OrderID: root.ID, Amount: refundAmount(80)}); !errors.Is(err, ErrOrderStatusInvalid) {
		t.Fatalf("missing legacy ledger undercounted: %v", err)
	}
	checkRefundFamily(t, db, root, children, "20", "10", "0")
}
