// Package domain contains immutable checkout facts. None of these values may be
// sourced from a browser price, tenant ID, or claimed payment result.
package domain

import (
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"time"
)

const ChainID uint64 = 196

const (
	QuoteIssued        = "quote_issued"
	Submitted          = "submitted"
	Detected           = "detected"
	FulfillmentPending = "fulfillment_pending"
	Delivered          = "delivered"
	ReviewRequired     = "review_required"
)

var (
	ErrDisabled = errors.New("tapecommerce_disabled")
	ErrInvalid  = errors.New("tapecommerce_invalid_input")
	ErrNotFound = errors.New("tapecommerce_invoice_not_found")
	ErrConflict = errors.New("tapecommerce_conflict")
	ErrPending  = errors.New("tapecommerce_receipt_pending")
	ErrUnsafe   = errors.New("tapecommerce_safe_block_pending")
	ErrRPC      = errors.New("tapecommerce_rpc_unavailable")
	ErrReceipt  = errors.New("tapecommerce_receipt_mismatch")
	ErrReorg    = errors.New("tapecommerce_canonical_block_mismatch")
	ErrReview   = errors.New("tapecommerce_review_required")
	ErrLease    = errors.New("tapecommerce_stale_lease")
)

// Invoice is a trusted server-side snapshot of an existing order. ShopID comes
// from the instance/session configuration; OrderID and amount from its database.
// An authenticated buyer-to-order check remains the caller's responsibility.
type Invoice struct {
	InvoiceID  string
	ShopID     string
	OrderID    uint
	ChainID    uint64
	Checkout   string
	TenantHash string
	Payer      string
	Merchant   string
	AmountWei  string
	Deadline   uint64
	FXSnapshot FXSnapshot `json:"fxSnapshot"`
}

func (i Invoice) Validate() error {
	if i.ShopID == "" || len(i.ShopID) > 128 || i.OrderID == 0 || i.ChainID != ChainID || i.Deadline == 0 {
		return ErrInvalid
	}
	for _, hash := range []string{i.InvoiceID, i.TenantHash} {
		if !ValidHex(hash, 32) || IsZeroHex(hash) {
			return ErrInvalid
		}
	}
	for _, address := range []string{i.Checkout, i.Payer, i.Merchant} {
		if !ValidHex(address, 20) || IsZeroHex(address) {
			return ErrInvalid
		}
	}
	if strings.EqualFold(i.Checkout, i.Merchant) {
		return ErrInvalid
	}
	if len(i.AmountWei) > 78 {
		return ErrInvalid
	}
	amount, ok := new(big.Int).SetString(i.AmountWei, 10)
	if !ok || amount.Sign() <= 0 || amount.BitLen() > 256 || amount.String() != i.AmountWei {
		return ErrInvalid
	}
	return i.FXSnapshot.ValidateBinding(i.AmountWei, i.Deadline)
}

// Normalize makes database uniqueness independent of hex letter case.
func (i Invoice) Normalize() Invoice {
	i.InvoiceID = strings.ToLower(i.InvoiceID)
	i.TenantHash = strings.ToLower(i.TenantHash)
	i.Checkout = strings.ToLower(i.Checkout)
	i.Payer = strings.ToLower(i.Payer)
	i.Merchant = strings.ToLower(i.Merchant)
	return i
}

func ValidHex(s string, bytes int) bool {
	if len(s) != 2+bytes*2 || !strings.HasPrefix(s, "0x") {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

func IsZeroHex(s string) bool { return len(s) >= 2 && strings.Trim(s[2:], "0") == "" }

type Evidence struct {
	ChainID     uint64
	TxHash      string
	LogIndex    uint64
	BlockNumber uint64
	BlockHash   string
	SafeBlock   uint64
	PaidAt      uint64 // Canonical inclusion block timestamp, in seconds.
	Safe        bool
}

type Payment struct {
	Invoice  Invoice
	State    string
	TxHash   string
	Evidence *Evidence
	Reason   string
}

type Job struct {
	InvoiceID  string
	ShopID     string
	OrderID    uint
	Key        string
	Lease      string
	LeaseUntil time.Time
	Attempts   int
}
