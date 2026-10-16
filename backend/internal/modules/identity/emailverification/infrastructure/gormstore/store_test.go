package emailverificationstore

import (
	"fmt"
	"sync"
	"testing"
	"time"

	emailverificationdomain "github.com/dujiao-next/internal/modules/identity/emailverification/domain"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := fmt.Sprintf("file:email_verification_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(&emailverificationdomain.Code{}, &emailverificationdomain.SendReservation{}, &emailverificationdomain.SendBudget{}); err != nil {
		t.Fatalf("migrate verification code failed: %v", err)
	}
	return New(db)
}

func TestStoreReturnsLatestActiveCodeAndPersistsAttempts(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	older := &emailverificationdomain.Code{
		Email: "buyer@example.com", Purpose: "login", Code: "111111",
		ExpiresAt: now.Add(time.Minute), SentAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Minute),
	}
	latest := &emailverificationdomain.Code{
		Email: "buyer@example.com", Purpose: "login", Code: "222222",
		ExpiresAt: now.Add(time.Minute), SentAt: now, CreatedAt: now,
	}
	deletedAt := now.Add(time.Second)
	deleted := &emailverificationdomain.Code{
		Email: "buyer@example.com", Purpose: "login", Code: "333333",
		ExpiresAt: now.Add(time.Minute), SentAt: now.Add(time.Minute), CreatedAt: now.Add(time.Minute), DeletedAt: &deletedAt,
	}
	for _, code := range []*emailverificationdomain.Code{older, latest, deleted} {
		if err := store.Create(code); err != nil {
			t.Fatalf("create verification code failed: %v", err)
		}
	}

	got, err := store.GetLatest("buyer@example.com", "login")
	if err != nil {
		t.Fatalf("get latest verification code failed: %v", err)
	}
	if got == nil || got.ID != latest.ID {
		t.Fatalf("unexpected latest verification code: %#v", got)
	}
	if err := store.IncrementAttempt(got.ID); err != nil {
		t.Fatalf("increment attempt failed: %v", err)
	}
	verifiedAt := now.Add(2 * time.Second)
	if err := store.MarkVerified(got.ID, verifiedAt); err != nil {
		t.Fatalf("mark verified failed: %v", err)
	}
	refreshed, err := store.GetLatest("buyer@example.com", "login")
	if err != nil {
		t.Fatalf("reload verification code failed: %v", err)
	}
	if refreshed.AttemptCount != 1 || refreshed.VerifiedAt == nil || !refreshed.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("verification state was not persisted: %#v", refreshed)
	}
}

func TestConcurrentReadersCanConsumeVerificationCodeOnlyOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:atomic_code_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&emailverificationdomain.Code{}, &emailverificationdomain.SendReservation{}, &emailverificationdomain.SendBudget{}); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	now := time.Now()
	if err = store.Create(&emailverificationdomain.Code{Email: "fixture@example.test", Purpose: "reset", Code: "123456", ExpiresAt: now.Add(time.Minute), SentAt: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var read sync.WaitGroup
	read.Add(2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			record, e := store.GetLatest("fixture@example.test", "reset")
			if e != nil {
				read.Done()
				results <- e
				return
			}
			if record == nil || record.VerifiedAt != nil {
				read.Done()
				results <- fmt.Errorf("both readers must observe an unused code")
				return
			}
			read.Done()
			read.Wait() // deterministic scheduling only; actual GORM updates are untouched.
			results <- store.MarkVerified(record.ID, time.Now())
		}()
	}
	successes := 0
	for range 2 {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("same real persisted code consumed %d times by concurrent readers", successes)
	}
}
