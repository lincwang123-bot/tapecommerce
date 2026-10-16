package integrationtest

import (
	"errors"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	emailverificationdomain "github.com/dujiao-next/internal/modules/identity/emailverification/domain"
	userauthapp "github.com/dujiao-next/internal/modules/identity/userauth/application"
	userauthgormstore "github.com/dujiao-next/internal/modules/identity/userauth/infrastructure/gormstore"
	"golang.org/x/crypto/bcrypt"
)

func TestRegistrationDatabaseFailureDoesNotConsumeCode(t *testing.T) {
	svc, _, _, db := newUser2FATestServices(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	svc.SetAuthUnitOfWork(userauthgormstore.New(db))
	now := time.Now()
	code := emailverificationdomain.Code{Email: "register-transaction@example.test", Purpose: constants.VerifyPurposeRegister, Code: "123456", ExpiresAt: now.Add(time.Minute), SentAt: now, CreatedAt: now}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_fixture_registration BEFORE INSERT ON users BEGIN SELECT RAISE(ABORT,'synthetic registration failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Register(code.Email, "Fixture-Aa9-password", code.Code, true, true); err == nil {
		t.Fatal("database failure not propagated")
	}
	var persisted emailverificationdomain.Code
	if err := db.First(&persisted, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.VerifiedAt != nil {
		t.Fatal("failed registration consumed code")
	}
	if err := db.Exec("DROP TRIGGER fail_fixture_registration").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Register(code.Email, "Fixture-Aa9-password", "000000", true, true); !errors.Is(err, userauthapp.ErrVerifyCodeInvalid) {
		t.Fatalf("wrong code error: %v", err)
	}
	if err := db.First(&persisted, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.AttemptCount != 1 {
		t.Fatalf("wrong-code attempt rolled back: %d", persisted.AttemptCount)
	}
	if user, token, _, err := svc.Register(code.Email, "Fixture-Aa9-password", code.Code, true, true); err != nil || user == nil || token == "" {
		t.Fatalf("retry after rollback failed: %v", err)
	}
	if err := db.First(&persisted, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.VerifiedAt == nil {
		t.Fatal("successful transaction did not consume code")
	}
}

func TestPasswordWriteFailureRollsBackCodeAndTokenVersion(t *testing.T) {
	svc, _, users, db := newUser2FATestServices(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	svc.SetAuthUnitOfWork(userauthgormstore.New(db))
	user := createActiveUser(t, users, "reset-transaction@example.test", "Original-Aa9-password")
	now := time.Now()
	code := emailverificationdomain.Code{Email: user.Email, Purpose: constants.VerifyPurposeReset, Code: "654321", ExpiresAt: now.Add(time.Minute), SentAt: now, CreatedAt: now}
	if err := db.Create(&code).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_fixture_password BEFORE UPDATE OF password_hash ON users BEGIN SELECT RAISE(ABORT,'synthetic password failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(user.Email, code.Code, "Replacement-Bb8-password"); err == nil {
		t.Fatal("database failure not propagated")
	}
	var persisted emailverificationdomain.Code
	if err := db.First(&persisted, code.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.VerifiedAt != nil {
		t.Fatal("failed password write consumed code")
	}
	after, err := users.GetByID(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != user.PasswordHash || after.TokenVersion != user.TokenVersion {
		t.Fatal("failed reset partially committed account")
	}
	if err := db.Exec("DROP TRIGGER fail_fixture_password").Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(user.Email, code.Code, "Replacement-Bb8-password"); err != nil {
		t.Fatal(err)
	}
	after, err = users.GetByID(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TokenVersion != user.TokenVersion+1 || bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte("Replacement-Bb8-password")) != nil {
		t.Fatal("successful reset was not committed")
	}
	if err := svc.ResetPassword(user.Email, code.Code, "Other-Cc7-password"); !errors.Is(err, userauthapp.ErrVerifyCodeInvalid) {
		t.Fatalf("consumed code returned nonstandard error: %v", err)
	}
}
