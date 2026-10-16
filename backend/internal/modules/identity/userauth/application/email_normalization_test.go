package application

import (
	"fmt"
	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/constants"
	usercontract "github.com/dujiao-next/internal/modules/identity/user/contract"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	"testing"
	"time"
)

func TestNormalizeEmailUsesMailboxForDisplayNameAliases(t *testing.T) {
	for _, raw := range []string{"Same@Example.COM", "  same@example.com  ", "First <same@example.com>", "Second <SAME@EXAMPLE.COM>"} {
		got, err := normalizeEmail(raw)
		if err != nil || got != "same@example.com" {
			t.Fatalf("normalize %q = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"not an address", "", "a@example.com, b@example.com"} {
		if _, err := normalizeEmail(raw); err == nil {
			t.Fatalf("invalid address accepted: %q", raw)
		}
	}
}

type externalLoginSnapshotStore struct {
	usercontract.Store
	current *userdomain.User
	touched bool
}

func (s *externalLoginSnapshotStore) GetByID(uint) (*userdomain.User, error) {
	copy := *s.current
	return &copy, nil
}
func (s *externalLoginSnapshotStore) TouchLastLoginIfVersion(_ uint, version uint64, _ time.Time) (bool, error) {
	s.touched = true
	return s.current.TokenVersion == version, nil
}
func (s *externalLoginSnapshotStore) Update(*userdomain.User) error {
	panic("external login must never update an old account snapshot")
}
func TestExternalLoginBindsAuthenticatedAccountSnapshot(t *testing.T) {
	for _, twoFA := range []bool{false, true} {
		t.Run(fmt.Sprint(twoFA), func(t *testing.T) {
			snapshot := &userdomain.User{ID: 1, TokenVersion: 1, Status: constants.UserStatusActive}
			if twoFA {
				now := time.Now()
				snapshot.TOTPEnabledAt = &now
			}
			current := *snapshot
			current.TokenVersion = 2
			store := &externalLoginSnapshotStore{current: &current}
			service := &Service{cfg: &config.Config{UserJWT: config.JWTConfig{SecretKey: "test-jwt", ExpireHours: 1}}, userRepo: store}
			if result, err := service.completeExternalLogin(snapshot, constants.LoginLogSourceGoogle); err == nil {
				t.Fatalf("external login accepted stale snapshot: %+v", result)
			}
		})
	}
}
