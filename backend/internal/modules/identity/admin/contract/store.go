package admincontract

import (
	"time"

	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
)

type Store interface {
	GetByUsername(string) (*admindomain.Admin, error)
	GetByID(uint) (*admindomain.Admin, error)
	List() ([]admindomain.Admin, error)
	Count() (int64, error)
	Create(*admindomain.Admin) error
	Update(*admindomain.Admin) error
	Delete(uint) error
	UpdateTOTPPending(uint, string, time.Time) error
	UpdateTOTPEnabled(uint, string, time.Time, string) error
	UpdateRecoveryCodes(uint, string) error
	ConsumeRecoveryCodes(uint, string, string) (bool, error)
	TouchLastLoginIfVersion(uint, uint64, time.Time) (bool, error)
	ClearTOTP(uint) error
	UpdatePassword(uint, string) error
	UpdatePasswordIfVersion(uint, uint64, string, time.Time) (bool, error)
}
