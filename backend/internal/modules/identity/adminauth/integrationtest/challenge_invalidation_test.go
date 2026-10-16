package integrationtest

import (
	"fmt"
	"github.com/dujiao-next/internal/config"
	admincontract "github.com/dujiao-next/internal/modules/identity/admin/contract"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
	adminauthapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"testing"
	"time"
)

func TestAdminChallengeRevokedOnCredentialVersionChange(t *testing.T) {
	auth, otp, repo := newAuthTestService(t)
	subject := createAuthTestAdmin(t, repo, "challenge-version", "secret123")
	setup, _ := otp.Setup(subject.ID)
	code, _ := totp.GenerateCode(setup.Secret, time.Now())
	if _, err := otp.Enable(subject.ID, code); err != nil {
		t.Fatal(err)
	}
	login, err := auth.Login(subject.Username, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseChallengeToken(login.ChallengeToken)
	if err != nil {
		t.Fatal(err)
	}
	subject, _ = repo.GetByID(subject.ID)
	subject.TokenVersion++
	if err := repo.Update(subject); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseChallengeToken(login.ChallengeToken); err == nil {
		t.Fatal("stale challenge accepted")
	}
	if _, err := auth.CompleteLoginAfter2FA(subject.ID, *claims.TokenVersion); err == nil {
		t.Fatal("stale verified challenge minted access token")
	}
	fresh, _ := repo.GetByID(subject.ID)
	if fresh.TokenVersion != subject.TokenVersion {
		t.Fatal("login overwrote credential state")
	}
}

// Simulates a credential reset after the password snapshot was read, before
// the login can either mint its challenge or persist its last-login timestamp.
type resettingAdminLoginStore struct {
	admincontract.Store
	reset func(uint) error
}

func (r *resettingAdminLoginStore) GetByUsername(name string) (*admindomain.Admin, error) {
	subject, err := r.Store.GetByUsername(name)
	if err == nil && subject != nil && r.reset != nil {
		reset := r.reset
		r.reset = nil
		err = reset(subject.ID)
	}
	return subject, err
}
func TestAdminPasswordSnapshotCannotSurviveConcurrentReset(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			_, _, repo := newAuthTestService(t)
			subject := createAuthTestAdmin(t, repo, "snapshot-admin", "OldPassword123!")
			if enabled {
				now := time.Now()
				subject.TOTPEnabledAt = &now
				if err := repo.Update(subject); err != nil {
					t.Fatal(err)
				}
			}
			replacement, _ := bcrypt.GenerateFromPassword([]byte("Replacement123!"), bcrypt.DefaultCost)
			wrapped := &resettingAdminLoginStore{Store: repo, reset: func(id uint) error { return repo.UpdatePassword(id, string(replacement)) }}
			auth := adminauthapp.NewService(&config.Config{App: config.AppConfig{SecretKey: "snapshot-test-key"}, JWT: config.JWTConfig{SecretKey: "snapshot-test-jwt", ExpireHours: 1}}, wrapped)
			if result, err := auth.Login(subject.Username, "OldPassword123!"); err == nil {
				t.Fatalf("stale password authenticated: %+v", result)
			}
			fresh, err := repo.GetByID(subject.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.PasswordHash != string(replacement) || fresh.TokenVersion != subject.TokenVersion+1 {
				t.Fatal("login overwrote concurrent reset")
			}
		})
	}
}
