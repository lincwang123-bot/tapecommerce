package gormstore

import (
	"fmt"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRechargeAdmissionAcrossDatabaseConnections(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "quota.sqlite")+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	pool.SetMaxOpenConns(8)
	defer pool.Close()
	if err = db.AutoMigrate(&userdomain.User{}, &paymentdomain.Payment{}, &walletdomain.RechargeOrder{}); err != nil {
		t.Fatal(err)
	}
	user := userdomain.User{Email: "quota@example.test", Status: "active"}
	if err = db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := db.Transaction(func(tx *gorm.DB) error {
				store := New(tx, "test-secret")
				if err := store.LockRechargeUser(user.ID); err != nil {
					return err
				}
				now := time.Now()
				rows, err := store.PendingRechargePayments(user.ID, now, now.Add(-time.Hour))
				if err != nil {
					return err
				}
				if len(rows) >= 3 {
					return nil
				}
				until := now.Add(time.Minute)
				p := paymentdomain.Payment{Status: "initiated", ExpiredAt: &until}
				if err := store.Create(&p); err != nil {
					return err
				}
				r := walletdomain.RechargeOrder{RechargeNo: fmt.Sprintf("concurrent-%d", i), UserID: user.ID, PaymentID: p.ID, Status: "pending"}
				if err := tx.Create(&r).Error; err != nil {
					return err
				}
				accepted.Add(1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if accepted.Load() != 3 {
		t.Fatalf("quota exceeded across connections: %d", accepted.Load())
	}
}
