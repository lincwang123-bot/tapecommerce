package application

import (
	"context"
	"errors"
	"github.com/dujiao-next/internal/modules/tapecommerce/contract"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"strings"
	"time"
)

// Service is deliberately not registered in the live application. Enabling it
// requires a trusted quote/order bridge and an approved deployed checkout.
type Service struct {
	Enabled   bool
	ShopID    string
	Store     contract.Store
	Verifier  contract.Verifier
	Admission contract.AtomicAdmission
}

func (s Service) ready() error {
	if !s.Enabled {
		return domain.ErrDisabled
	}
	if s.ShopID == "" || s.Store == nil || s.Verifier == nil {
		return domain.ErrInvalid
	}
	return nil
}

// CreateTrusted accepts only an already authorized immutable order snapshot.
// It is NOT an HTTP input model or a public price signing endpoint.
func (s Service) CreateTrusted(ctx context.Context, invoice domain.Invoice) error {
	if err := s.ready(); err != nil {
		return err
	}
	if invoice.ShopID != s.ShopID {
		return domain.ErrNotFound
	}
	if err := invoice.Validate(); err != nil {
		return err
	}
	if invoice.Deadline <= uint64(time.Now().Unix()) {
		return domain.ErrInvalid
	}
	if err := invoice.FXSnapshot.ValidateAtIssue(time.Now()); err != nil {
		return err
	}
	return s.Store.Create(ctx, invoice.Normalize())
}

func (s Service) Submit(ctx context.Context, invoiceID, txHash string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if !domain.ValidHex(invoiceID, 32) || !domain.ValidHex(txHash, 32) {
		return domain.ErrInvalid
	}
	return s.Store.Submit(ctx, s.ShopID, strings.ToLower(invoiceID), strings.ToLower(txHash))
}

// Check reads chain data independently. RPC errors/pending safe blocks preserve
// recoverable state; contradictory finalized facts require manual review.
func (s Service) Check(ctx context.Context, invoiceID string) (domain.Payment, error) {
	if err := s.ready(); err != nil {
		return domain.Payment{}, err
	}
	invoiceID = strings.ToLower(invoiceID)
	p, err := s.Store.Get(ctx, s.ShopID, invoiceID)
	if err != nil {
		return p, err
	}
	if p.State == domain.ReviewRequired {
		return p, domain.ErrReview
	}
	if p.TxHash == "" {
		return p, domain.ErrPending
	}
	evidence, err := s.Verifier.Verify(ctx, p.Invoice, p.TxHash)
	if err != nil {
		if errors.Is(err, domain.ErrReceipt) || errors.Is(err, domain.ErrReorg) {
			reason := "receipt_mismatch"
			if errors.Is(err, domain.ErrReorg) {
				reason = "canonical_block_mismatch"
			}
			if reviewErr := s.Store.Review(ctx, s.ShopID, invoiceID, reason); reviewErr != nil {
				return p, reviewErr
			}
		}
		return p, err
	}
	if err := s.Store.Record(ctx, s.ShopID, invoiceID, evidence); err != nil {
		if errors.Is(err, domain.ErrReorg) {
			if reviewErr := s.Store.Review(ctx, s.ShopID, invoiceID, "canonical_block_mismatch"); reviewErr != nil {
				return p, reviewErr
			}
		}
		return p, err
	}
	p, err = s.Store.Get(ctx, s.ShopID, invoiceID)
	if err == nil && !evidence.Safe {
		err = domain.ErrUnsafe
	}
	return p, err
}

// DeliverOne requires an atomic admission implementation which shares invoice
// locks with the stock transaction. Without that bridge it fails closed, even
// when receipt verification and the feature flag are configured. An ordinary
// Fulfiller cannot be passed here. This avoids review-after-claim stock release.
func (s Service) DeliverOne(ctx context.Context, invoiceID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if s.Admission == nil {
		return domain.ErrDisabled
	}
	p, err := s.Check(ctx, invoiceID)
	if err != nil {
		return err
	}
	if p.State == domain.Delivered {
		return nil
	}
	job, err := s.Store.Claim(ctx, s.ShopID, p.Invoice.InvoiceID, time.Now(), 2*time.Minute)
	if err != nil {
		return err
	}
	err = s.Admission.AdmitAndFulfill(ctx, job)
	if err != nil {
		review := errors.Is(err, domain.ErrReview)
		// Persist an error category only, never arbitrary upstream errors which
		// may contain card secrets, signed URLs or credentials.
		reason := "delivery_retry"
		if review {
			reason = "delivery_review_required"
			if reviewErr := s.Store.Review(ctx, job.ShopID, job.InvoiceID, reason); reviewErr != nil {
				return reviewErr
			}
			return err
		}
		if finishErr := s.Store.Finish(ctx, job, false, reason, time.Now().Add(time.Minute)); finishErr != nil {
			return finishErr
		}
		return err
	}
	return s.Store.Finish(ctx, job, true, "", time.Time{})
}
