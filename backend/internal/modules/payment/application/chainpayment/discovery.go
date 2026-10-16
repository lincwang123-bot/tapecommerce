package chainpayment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/dujiao-next/internal/modules/payment/contract"
	"github.com/dujiao-next/internal/modules/payment/domain"
	"os"
	"strings"
	"time"
)

func AutoDiscoveryEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SELFHOST_AUTO_DISCOVERY_ENABLED")), "true")
}

// DiscoveryReport separates a healthy scan from idle/no-invoice and RPC failure.
type DiscoveryReport struct {
	Network         string
	Scanned, Failed int
	Code            string
}

func (s *Service) DiscoverOnce(ctx context.Context, network string) (DiscoveryReport, error) {
	report := DiscoveryReport{Network: network, Code: "idle"}
	d, ok := s.Verifier.(contract.ChainDiscoverer)
	if !ok {
		return report, ErrUnavailable
	}
	store, ok := Stores(s.Payments).(contract.ChainDiscoveryStore)
	if !ok {
		return report, ErrUnavailable
	}
	rows, err := store.DiscoveryDue(network, time.Now(), 2)
	if err != nil {
		return report, err
	}
	for _, v := range rows {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		b := make([]byte, 24)
		if _, err = rand.Read(b); err != nil {
			return report, err
		}
		token := hex.EncodeToString(b)
		now := time.Now()
		acquired, e := store.AcquireDiscovery(v.PaymentID, token, now, now.Add(2*time.Minute))
		if e != nil {
			return report, e
		}
		if !acquired {
			continue
		}
		// Bound each RPC+verification page below the durable discovery lease.
		scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		page, failure := d.Discover(scanCtx, network, contract.ChainDiscoverRequest{Recipient: v.Recipient, AmountUnits: v.ExpectedUnits, EarliestPaidAt: v.CreatedAt, MinBlockNumber: v.MinBlockNumber, Cursor: v.DiscoveryCursor})
		cursor, code, delay := v.DiscoveryCursor, "rpc_unavailable", 30*time.Second
		complete := failure == nil && page != nil
		if failure != nil {
			code = failure.Code
		}
		if complete {
			for _, hash := range page.TxHashes {
				_, e = s.submit(scanCtx, v.PaymentID, hash, true)
				current, readErr := Stores(s.Payments).Get(v.PaymentID, false)
				if e != nil || readErr != nil || current == nil {
					complete = false
					code = "candidate_retry"
					break
				}
				if current.VerificationStatus == "verified" || current.VerificationStatus == "review_required" {
					break
				}
				// A rejected candidate is safe to skip; unavailable/pending/contended
				// verification retains the page so crashes and RPC failures cannot lose it.
				normalizedHash := strings.ToLower(strings.TrimSpace(hash))
				if network == "tron" {
					normalizedHash = strings.TrimPrefix(normalizedHash, "0x")
				}
				if current.VerificationStatus != "rejected" || current.SubmittedHash != normalizedHash {
					complete = false
					code = "candidate_retry"
					break
				}
			}
		}
		cancel()
		if complete {
			cursor = page.NextCursor
			code = "catching_up"
			delay = time.Second
			if page.Done {
				code = "caught_up"
				delay = discoveryPollDelay(v, time.Now())
			}
			report.Scanned++
		} else {
			report.Failed++
		}
		report.Code = code
		if e = store.FinishDiscovery(v.PaymentID, token, cursor, code, time.Now().Add(delay)); e != nil {
			return report, e
		}
		// A provider outage must not fan out over the remaining invoice batch.
		if failure != nil && failure.Kind == "unavailable" {
			break
		}
	}
	return report, nil
}

// Unexpired invoices retain fast polling for their entire payment window.
// Retained expired invoices reconcile at a lower rate.
func discoveryPollDelay(v domain.ChainIntent, now time.Time) time.Duration {
	if !v.ExpiresAt.After(now) {
		if now.Before(v.ExpiresAt.Add(domain.ChainConfirmationGrace)) {
			return 10 * time.Second
		}
		return 5 * time.Minute
	}
	return 5 * time.Second
}
