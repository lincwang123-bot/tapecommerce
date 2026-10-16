package emailverificationstore

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSendReservationSerializesConcurrentAndCrossPurposeRequests(t *testing.T) {
	store := newTestStore(t)
	sqlDB, _ := store.db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Now().UTC()
	var wg sync.WaitGroup
	results := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.ReserveSend("recipient@example.test", "register", now, time.Minute)
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("delivery reservations=%d", winners)
	}
	if ok, err := store.ReserveSend("recipient@example.test", "reset", now, time.Minute); err != nil || ok {
		t.Fatalf("cross-purpose bypass: %v %v", ok, err)
	}
	if ok, err := store.ReserveSend("other@example.test", "register", now, time.Minute); err != nil || !ok {
		t.Fatalf("different recipient blocked: %v %v", ok, err)
	}
	if ok, err := store.ReserveSend("recipient@example.test", "register", now.Add(time.Minute), time.Minute); err != nil || !ok {
		t.Fatalf("cooldown did not expire: %v %v", ok, err)
	}
}

func TestDeliveryBudgetOnlyConsumesAcceptedReservations(t *testing.T) {
	store := newTestStore(t)
	sqlDB, _ := store.db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	now := time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	for i := 0; i < 100; i++ {
		ok, err := store.ReserveSend(fmt.Sprintf("budget-%d@example.test", i), "register", now, time.Minute)
		if err != nil || !ok {
			t.Fatalf("reservation %d: %v %v", i, ok, err)
		}
	}
	if ok, err := store.ReserveSend("over-budget@example.test", "register", now, time.Minute); err != nil || ok {
		t.Fatalf("budget bypass: %v %v", ok, err)
	}
	if ok, err := store.ReserveSend("over-budget@example.test", "register", now.Add(time.Hour), time.Minute); err != nil || !ok {
		t.Fatalf("hourly recovery: %v %v", ok, err)
	}
}
