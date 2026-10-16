package chainpayment

import (
	"github.com/dujiao-next/internal/modules/payment/domain"
	"testing"
	"time"
)

func TestDiscoveryPollDelay(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name    string
		expires time.Time
		want    time.Duration
	}{
		{"paying window", now.Add(time.Minute), 5 * time.Second},
		{"expiry boundary", now, 10 * time.Second},
		{"grace boundary", now.Add(-domain.ChainConfirmationGrace), 5 * time.Minute},
		{"retained expired", now.Add(-time.Hour), 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := domain.ChainIntent{CreatedAt: now.Add(-time.Hour), ExpiresAt: tc.expires}
			if got := discoveryPollDelay(v, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
