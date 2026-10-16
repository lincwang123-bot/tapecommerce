package integrationtest

import (
	"github.com/dujiao-next/internal/config"
	contract "github.com/dujiao-next/internal/modules/identity/admin/contract"
	totpapp "github.com/dujiao-next/internal/modules/identity/adminauth/totp/application"
	"github.com/pquerna/otp/totp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recoveryBarrierStore struct {
	contract.Store
	calls atomic.Int32
	ready chan struct{}
}

func (r *recoveryBarrierStore) ConsumeRecoveryCodes(id uint, before, after string) (bool, error) {
	n := r.calls.Add(1)
	if n <= 2 {
		if n == 2 {
			close(r.ready)
		}
		<-r.ready
	}
	return r.Store.ConsumeRecoveryCodes(id, before, after)
}
func TestRecoveryCodeConcurrentConsumption(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same_code_once", false: "different_codes_stay_consumed"}[same], func(t *testing.T) {
			base, repo, db := newTOTPTestService(t)
			sqlDB, _ := db.DB()
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			subject := createTOTPTestAdmin(t, repo, "atomic-recovery@example.test")
			setup, err := base.Setup(subject.ID)
			if err != nil {
				t.Fatal(err)
			}
			code, _ := totp.GenerateCode(setup.Secret, time.Now())
			enabled, err := base.Enable(subject.ID, code)
			if err != nil {
				t.Fatal(err)
			}
			barrier := &recoveryBarrierStore{Store: repo, ready: make(chan struct{})}
			svc := totpapp.NewService(&config.Config{App: config.AppConfig{SecretKey: "unused-for-recovery"}}, barrier, nil)
			codes := []string{enabled.RecoveryCodes[0], enabled.RecoveryCodes[1]}
			if same {
				codes[1] = codes[0]
			}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, value := range codes {
				wg.Add(1)
				go func(c string) { defer wg.Done(); results <- svc.VerifyChallengeRecoveryCode(subject.ID, c) }(value)
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				}
			}
			want := 2
			if same {
				want = 1
			}
			if success != want {
				t.Fatalf("successful consumption=%d want=%d", success, want)
			}
			for _, value := range codes {
				if err := svc.VerifyChallengeRecoveryCode(subject.ID, value); err == nil {
					t.Fatal("used recovery code became reusable")
				}
			}
		})
	}
}
