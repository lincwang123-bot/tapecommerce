package integrationtest

import (
	"fmt"
	"github.com/dujiao-next/internal/config"
	usercontract "github.com/dujiao-next/internal/modules/identity/user/contract"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userauthapp "github.com/dujiao-next/internal/modules/identity/userauth/application"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"testing"
	"time"
)

func TestUserChallengeRevokedOnCredentialVersionChange(t *testing.T) {
	auth, otp, repo, _ := newUser2FATestServices(t)
	subject := createActiveUser(t, repo, "challenge-version@example.test", "secret123")
	setup, _ := otp.Setup(subject.ID)
	code, _ := totp.GenerateCode(setup.Secret, time.Now())
	if _, err := otp.Enable(subject.ID, code); err != nil {
		t.Fatal(err)
	}
	login, err := auth.LoginStep1(subject.Email, "secret123", false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseUserChallengeToken(login.ChallengeToken)
	if err != nil {
		t.Fatal(err)
	}
	subject, _ = repo.GetByID(subject.ID)
	subject.TokenVersion++
	if err := repo.Update(subject); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseUserChallengeToken(login.ChallengeToken); err == nil {
		t.Fatal("stale challenge accepted")
	}
	if _, err := auth.CompleteLoginAfter2FA(subject.ID, false, *claims.TokenVersion); err == nil {
		t.Fatal("stale verified challenge minted access token")
	}
	fresh, _ := repo.GetByID(subject.ID)
	if fresh.TokenVersion != subject.TokenVersion {
		t.Fatal("login overwrote credential state")
	}
}

type resettingUserSnapshotStore struct {
	usercontract.Store
	reset func(uint) error
}

func (r *resettingUserSnapshotStore) resetAfterRead(subject *userdomain.User, err error) (*userdomain.User, error) {
	if err == nil && subject != nil && r.reset != nil {
		reset := r.reset
		r.reset = nil
		err = reset(subject.ID)
	}
	return subject, err
}
func (r *resettingUserSnapshotStore) GetByEmail(email string) (*userdomain.User, error) {
	return r.resetAfterRead(r.Store.GetByEmail(email))
}
func (r *resettingUserSnapshotStore) GetByID(id uint) (*userdomain.User, error) {
	return r.resetAfterRead(r.Store.GetByID(id))
}
func TestUserPasswordSnapshotCannotSurviveConcurrentReset(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			_, _, repo, db := newUser2FATestServices(t)
			sqlDB, _ := db.DB()
			t.Cleanup(func() { _ = sqlDB.Close() })
			subject := createActiveUser(t, repo, "snapshot-user@example.test", "OldPassword123!")
			if enabled {
				now := time.Now()
				subject.TOTPEnabledAt = &now
				if err := repo.Update(subject); err != nil {
					t.Fatal(err)
				}
			}
			replacement, _ := bcrypt.GenerateFromPassword([]byte("Replacement123!"), bcrypt.DefaultCost)
			wrapped := &resettingUserSnapshotStore{Store: repo, reset: func(id uint) error {
				_, err := repo.UpdatePasswordIfVersion(id, subject.TokenVersion, string(replacement), time.Now())
				return err
			}}
			auth := userauthapp.NewService(&config.Config{UserJWT: config.JWTConfig{SecretKey: "snapshot-test-jwt", ExpireHours: 1}}, wrapped, nil, nil, nil, nil, nil)
			if result, err := auth.LoginStep1(subject.Email, "OldPassword123!", false); err == nil {
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
func TestProfileUpdatePreservesConcurrentCredentialReset(t *testing.T) {
	_, _, repo, db := newUser2FATestServices(t)
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	subject := createActiveUser(t, repo, "snapshot-profile@example.test", "OldPassword123!")
	replacement, _ := bcrypt.GenerateFromPassword([]byte("Replacement123!"), bcrypt.DefaultCost)
	wrapped := &resettingUserSnapshotStore{Store: repo, reset: func(id uint) error {
		_, err := repo.UpdatePasswordIfVersion(id, subject.TokenVersion, string(replacement), time.Now())
		return err
	}}
	auth := userauthapp.NewService(&config.Config{}, wrapped, nil, nil, nil, nil, nil)
	name := "New nickname"
	if _, err := auth.UpdateProfile(subject.ID, &name, nil); err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.GetByID(subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.DisplayName != name || fresh.PasswordHash != string(replacement) || fresh.TokenVersion != subject.TokenVersion+1 {
		t.Fatal("profile update restored stale auth state")
	}
}
func TestPasswordChangeRejectsSnapshotResetWhilePreservingNewCredentials(t *testing.T) {
	_, _, repo, db := newUser2FATestServices(t)
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	subject := createActiveUser(t, repo, "snapshot-change@example.test", "OldPassword123!")
	replacement, _ := bcrypt.GenerateFromPassword([]byte("Replacement123!"), bcrypt.DefaultCost)
	wrapped := &resettingUserSnapshotStore{Store: repo, reset: func(id uint) error {
		_, err := repo.UpdatePasswordIfVersion(id, subject.TokenVersion, string(replacement), time.Now())
		return err
	}}
	auth := userauthapp.NewService(&config.Config{}, wrapped, nil, nil, nil, nil, nil)
	if err := auth.ChangePassword(subject.ID, "OldPassword123!", "AnotherPassword123!"); err == nil {
		t.Fatal("stale password change accepted")
	}
	fresh, err := repo.GetByID(subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.PasswordHash != string(replacement) || fresh.TokenVersion != subject.TokenVersion+1 {
		t.Fatal("stale password change restored old version")
	}
}
