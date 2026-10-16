package chainpayment

import (
	"context"
	"crypto/rand"
	"errors"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/shopspring/decimal"
	"math/big"
	"strings"
	"time"
)

const Provider = "selfhost"

var ErrInvalid = errors.New("chain payment invalid")
var ErrExhausted = errors.New("chain amount slots exhausted")
var ErrUnavailable = errors.New("chain verification unavailable")
var ErrConflict = errors.New("chain receipt already assigned")

type Prepared struct {
	Head                              *contract.ChainHead
	Network, TokenContract, Recipient string
}
type Service struct {
	Payments contract.Store
	Verifier contract.ChainVerifier
	Settle   func(context.Context, *domain.Payment, *domain.ChainIntent, *contract.ChainEvidence) error
}

func Stores(v any) contract.ChainStore {
	if p, ok := v.(contract.ChainStores); ok {
		return p.ChainPayments()
	}
	return nil
}
func (s *Service) Validate(c *domain.PaymentChannel) error {
	if c == nil || s.Verifier == nil || c.ProviderType != Provider || c.InteractionMode != "qr" || !c.FeeRate.Decimal.IsZero() || !c.FixedFee.Decimal.IsZero() || len(c.ConfigJSON) != 1 {
		return ErrInvalid
	}
	n, ok := s.Verifier.Network(c.ChannelType)
	if !ok {
		return ErrInvalid
	}
	a, ok := c.ConfigJSON["receiving_address"].(string)
	if !ok {
		return ErrInvalid
	}
	if _, ok = s.Verifier.NormalizeAddress(n.Network, strings.TrimSpace(a)); !ok {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Prepare(ctx context.Context, c *domain.PaymentChannel) (*Prepared, error) {
	if err := s.Validate(c); err != nil {
		return nil, err
	}
	n, _ := s.Verifier.Network(c.ChannelType)
	if ctx == nil {
		ctx = context.Background()
	}
	h, e := s.Verifier.ReadHead(ctx, n.Network)
	if e != nil || h == nil || h.Number == 0 {
		return nil, ErrUnavailable
	}
	address, _ := s.Verifier.NormalizeAddress(n.Network, strings.TrimSpace(c.ConfigJSON["receiving_address"].(string)))
	return &Prepared{Head: h, Network: n.Network, TokenContract: n.TokenContract, Recipient: address}, nil
}
func (s *Service) Reserve(tx contract.Transaction, p *domain.Payment, c *domain.PaymentChannel, userID uint, expires time.Time, prepared *Prepared) error {
	if p == nil || p.ID == 0 || p.Currency != "USDT" || prepared == nil || prepared.Head == nil || s.Validate(c) != nil {
		return ErrInvalid
	}
	store := Stores(tx)
	if store == nil {
		return ErrUnavailable
	}
	if prior, e := store.Get(p.ID, true); e != nil {
		return e
	} else if prior != nil {
		Project(p, prior)
		return nil
	}
	n, _ := s.Verifier.Network(c.ChannelType)
	if n.Decimals < 6 || n.Decimals > 30 {
		return ErrInvalid
	}
	a, _ := s.Verifier.NormalizeAddress(n.Network, strings.TrimSpace(c.ConfigJSON["receiving_address"].(string)))
	// ReadHead and invoice allocation must describe the same immutable chain and
	// destination even if channel settings change between the two transactions.
	if prepared.Network != n.Network || prepared.TokenContract != n.TokenContract || prepared.Recipient != a {
		return ErrUnavailable
	}
	seed, e := rand.Int(rand.Reader, big.NewInt(9999))
	if e != nil {
		return e
	}
	base := p.Amount.Decimal
	now := time.Now().UTC()
	if !expires.After(now) {
		return ErrInvalid
	}
	for k := 0; k < 9999; k++ {
		tail := decimal.NewFromInt((seed.Int64()+int64(k))%9999 + 1).Shift(-6)
		total := base.Add(tail)
		v := &domain.ChainIntent{PaymentID: p.ID, OrderID: p.OrderID, UserID: userID, Network: n.Network, ChainID: n.ChainID, ChainLabel: strings.ToUpper(n.Network), TokenContract: n.TokenContract, Decimals: n.Decimals, Recipient: a, ExpectedUnits: total.Shift(int32(n.Decimals)).StringFixed(0), ChainAmount: total.StringFixed(6), AmountAdjustment: tail.StringFixed(6), SettlementAmount: p.Amount.String(), MinBlockNumber: prepared.Head.Number, CreatedAt: now.Truncate(time.Second), ExpiresAt: expires, VerificationStatus: "awaiting_hash", UpdatedAt: now}
		ok, e := store.Create(v)
		if e != nil {
			return e
		}
		if !ok {
			continue
		}
		p.Status = "pending"
		p.QRCode = a
		p.ExpiredAt = &expires
		Project(p, v)
		return tx.Payments().Update(p)
	}
	return ErrExhausted
}
func Project(p *domain.Payment, v *domain.ChainIntent) {
	if p.ProviderPayload == nil {
		p.ProviderPayload = jsonmap.JSON{}
	}
	for k, value := range map[string]any{"wallet_address": v.Recipient, "chain_amount": v.ChainAmount, "chain": v.ChainLabel, "network": v.Network, "chain_id": v.ChainID, "token_contract": v.TokenContract, "token_id": "USDT", "amount_adjustment": v.AmountAdjustment, "transaction_hash": v.SubmittedHash, "verification_status": v.VerificationStatus, "verification_code": v.VerificationCode, "received_units": v.ReceivedUnits, "can_transfer": v.VerificationStatus == "awaiting_hash" && time.Now().Before(v.ExpiresAt) && p.Status != "success"} {
		p.ProviderPayload[k] = value
	}
}
