package chainpayment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	"strings"
	"time"
)

func ValidHash(network, hash string) bool {
	raw := hash
	if network != "tron" {
		if !strings.HasPrefix(raw, "0x") {
			return false
		}
		raw = raw[2:]
	} else {
		raw = strings.TrimPrefix(raw, "0x")
	}
	if len(raw) != 64 {
		return false
	}
	_, e := hex.DecodeString(raw)
	return e == nil
}
func (s *Service) Submit(ctx context.Context, id uint, hash string) (*domain.Payment, error) {
	return s.submit(ctx, id, hash, false)
}
func (s *Service) submit(ctx context.Context, id uint, hash string, automatic bool) (*domain.Payment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p, e := s.Payments.GetByID(id)
	if e != nil || p == nil {
		return nil, ErrInvalid
	}
	if p.ProviderType != Provider {
		return nil, ErrInvalid
	}
	store := Stores(s.Payments)
	if store == nil {
		return nil, ErrUnavailable
	}
	v, e := store.Get(id, false)
	if e != nil || v == nil {
		return nil, ErrInvalid
	}
	hash = strings.TrimSpace(hash)
	if hash != "" && !ValidHash(v.Network, hash) {
		return nil, ErrInvalid
	}
	hash = strings.ToLower(hash)
	if v.Network == "tron" {
		hash = strings.TrimPrefix(hash, "0x")
	}
	if v.VerificationStatus == "verified" || v.VerificationStatus == "review_required" {
		if hash != "" && hash != v.SubmittedHash {
			return nil, ErrConflict
		}
		p, e = s.Payments.GetByID(id)
		if e != nil || p == nil {
			return nil, ErrUnavailable
		}
		Project(p, v)
		return p, nil
	}
	if hash == "" {
		hash = v.SubmittedHash
	}
	if hash == "" {
		return nil, ErrInvalid
	}
	if !automatic && v.NextCheckAt != nil && time.Now().Before(*v.NextCheckAt) {
		Project(p, v)
		return p, nil
	}
	tokenBytes := make([]byte, 24)
	if _, e = rand.Read(tokenBytes); e != nil {
		return nil, e
	}
	token := hex.EncodeToString(tokenBytes)
	now := time.Now()
	var ok bool
	if automatic {
		ds, supported := store.(contract.ChainDiscoveryStore)
		if !supported {
			return nil, ErrUnavailable
		}
		ok, e = ds.AcquireCandidate(id, token, now, now.Add(90*time.Second))
	} else {
		ok, e = store.Acquire(id, token, now, now.Add(90*time.Second))
	}
	if e != nil {
		return nil, e
	}
	if !ok {
		p, e = s.Payments.GetByID(id)
		return p, e
	}
	// Hash and retry schedule are durable before the read-only external request.
	e = s.Payments.WithinTransaction(func(tx contract.Transaction) error {
		payment, err := tx.Payments().GetByIDForUpdate(id)
		if err != nil || payment == nil {
			return ErrInvalid
		}
		repo := Stores(tx)
		current, err := repo.Get(id, true)
		if err != nil || current == nil {
			return ErrUnavailable
		}
		if current.LeaseToken != token {
			return ErrConflict
		}
		if current.VerificationStatus == "verified" || current.VerificationStatus == "review_required" {
			return ErrConflict
		}
		current.SubmittedHash = hash
		current.VerificationStatus = "pending"
		current.VerificationCode = "checking"
		current.UpdatedAt = now
		next := now.Add(90 * time.Second)
		current.NextCheckAt = &next
		v = current
		Project(payment, current)
		if err = tx.Payments().Update(payment); err != nil {
			return err
		}
		return repo.Update(current)
	})
	if e != nil {
		return nil, e
	}
	result, failure := s.Verifier.Verify(ctx, v.Network, contract.ChainVerifyRequest{TxID: hash, Recipient: v.Recipient, AmountUnits: v.ExpectedUnits, EarliestPaidAt: v.CreatedAt, MinBlockNumber: v.MinBlockNumber})
	if failure == nil && result != nil {
		if s.Settle == nil {
			return nil, ErrUnavailable
		}
		e = s.Settle(ctx, p, v, result)
		if e != nil {
			if errors.Is(e, ErrConflict) {
				if err := s.progress(id, token, "rejected", "receipt_already_assigned", true); err != nil {
					return nil, err
				}
				return s.Payments.GetByID(id)
			}
			_ = s.progress(id, token, "unavailable", "settlement_retry", false)
			return nil, e
		}
	} else {
		status, code := "unavailable", "rpc_unavailable"
		if failure != nil {
			code = failure.Code
			switch failure.Kind {
			case "pending":
				status = "pending"
			case "mismatch", "invalid_input":
				status = "rejected"
			}
		}
		e = s.progress(id, token, status, code, true)
		if e != nil {
			return nil, e
		}
	}
	return s.Payments.GetByID(id)
}
func (s *Service) progress(id uint, token, status, code string, increase bool) error {
	return s.Payments.WithinTransaction(func(tx contract.Transaction) error {
		p, e := tx.Payments().GetByIDForUpdate(id)
		if e != nil || p == nil {
			return ErrInvalid
		}
		repo := Stores(tx)
		v, e := repo.Get(id, true)
		if e != nil || v == nil {
			return ErrUnavailable
		}
		if v.LeaseToken != token {
			return nil
		}
		if v.VerificationStatus == "verified" || v.VerificationStatus == "review_required" {
			return nil
		}
		v.VerificationStatus = status
		v.VerificationCode = code
		v.LeaseToken = ""
		v.LeaseUntil = nil
		v.UpdatedAt = time.Now()
		if increase {
			v.Attempts++
		}
		v.NextCheckAt = nil
		if status == "pending" || status == "unavailable" {
			delay := time.Duration(10*(1<<min(v.Attempts, 6))) * time.Second
			next := time.Now().Add(delay)
			v.NextCheckAt = &next
		}
		Project(p, v)
		if e = tx.Payments().Update(p); e != nil {
			return e
		}
		return repo.Update(v)
	})
}
func (s *Service) RunOnce(ctx context.Context) error {
	store := Stores(s.Payments)
	if store == nil {
		return nil
	}
	rows, e := store.Due(time.Now(), 20)
	if e != nil {
		return e
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, _ = s.Submit(ctx, row.PaymentID, "")
	}
	return nil
}

// Claim is called only inside the existing accounting transaction. Failure or
// rollback leaves both the receipt claim and all balances unchanged.
func Claim(tx contract.Transaction, p *domain.Payment, expected *domain.ChainIntent, evidence *contract.ChainEvidence, businessOpen bool) (bool, error) {
	store := Stores(tx)
	if store == nil {
		return false, ErrUnavailable
	}
	v, e := store.Get(p.ID, true)
	if e != nil || v == nil {
		return false, ErrInvalid
	}
	if expected == nil || evidence == nil || v.PaymentID != p.ID || v.OrderID != p.OrderID || v.LeaseToken != expected.LeaseToken || v.SubmittedHash != evidence.TxID || v.ExpectedUnits != evidence.AmountUnits || v.Recipient != evidence.Recipient || v.SettlementAmount != p.Amount.String() || evidence.BlockNumber <= v.MinBlockNumber || evidence.FinalizedBlockNumber < evidence.BlockNumber || evidence.PaidAt.Before(v.CreatedAt) {
		return false, ErrInvalid
	}
	receipt := &domain.ChainReceipt{Network: v.Network, TxHash: evidence.TxID, LogIndex: evidence.LogIndex, PaymentID: p.ID, TokenContract: v.TokenContract, Recipient: evidence.Recipient, Sender: evidence.Sender, AmountUnits: evidence.AmountUnits, BlockNumber: evidence.BlockNumber, BlockHash: evidence.BlockHash, FinalizedBlockNumber: evidence.FinalizedBlockNumber, PaidAt: evidence.PaidAt, CreatedAt: time.Now()}
	ok, e := store.ClaimReceipt(receipt)
	if e != nil {
		return false, e
	}
	if !ok {
		return false, ErrConflict
	}
	v.ReceivedUnits = evidence.AmountUnits
	v.NextCheckAt = nil
	v.LeaseToken = ""
	v.LeaseUntil = nil
	v.UpdatedAt = time.Now()
	eligible := businessOpen && p.Status == "pending" && p.SupersededAt == nil && !evidence.PaidAt.After(v.ExpiresAt) && time.Now().Before(v.ExpiresAt.Add(domain.ChainConfirmationGrace))
	v.VerificationStatus = "verified"
	v.VerificationCode = "finalized"
	if !eligible {
		v.VerificationStatus = "review_required"
		v.VerificationCode = "late_or_closed_payment"
	}
	Project(p, v)
	if e = store.Update(v); e != nil {
		return false, e
	}
	return eligible, nil
}
