package emailverificationcontract

import (
	"errors"
	"time"

	emailverificationdomain "github.com/dujiao-next/internal/modules/identity/emailverification/domain"
)

var ErrCodeConsumed = errors.New("verification code unavailable for consumption")

type Store interface {
	Create(*emailverificationdomain.Code) error
	ReserveSend(email, purpose string, now time.Time, interval time.Duration) (bool, error)
	GetLatest(email, purpose string) (*emailverificationdomain.Code, error)
	MarkVerified(id uint, verifiedAt time.Time) error
	IncrementAttempt(id uint) error
}
