package gormstore

import (
	"fmt"
	"github.com/dujiao-next/internal/modules/payment/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryDueReservesActiveAndOldestLanes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "discovery.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err = db.AutoMigrate(&domain.ChainIntent{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for id := uint(1); id <= 5; id++ {
		expires := now.Add(-time.Hour)
		if id >= 4 {
			expires = now.Add(time.Minute)
		}
		v := domain.ChainIntent{PaymentID: id, Network: "bsc", ExpectedUnits: fmt.Sprint(id), CreatedAt: now.Add(-time.Duration(10-id) * time.Hour), ExpiresAt: expires, VerificationStatus: "pending"}
		if err = db.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := &chainStore{db}
	first, err := store.DiscoveryDue("bsc", now, 2)
	if err != nil || len(first) != 2 || first[0].PaymentID != 4 || first[1].PaymentID != 1 {
		t.Fatal(first, err)
	}
	for _, v := range first {
		acquired, err := store.AcquireDiscovery(v.PaymentID, "lease", now, now.Add(time.Minute))
		if err != nil || !acquired {
			t.Fatal(err)
		}
		if err = store.FinishDiscovery(v.PaymentID, "lease", "", "caught_up", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	second, err := store.DiscoveryDue("bsc", now, 2)
	if err != nil || len(second) != 2 || second[0].PaymentID != 5 || second[1].PaymentID != 2 {
		t.Fatal(second, err)
	}
	rows, err := store.DiscoveryDue("bsc", now, 0)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}
