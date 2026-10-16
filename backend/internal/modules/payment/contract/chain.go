package contract

import (
	"context"
	"errors"
	"github.com/dujiao-next/internal/modules/payment/domain"
	"time"
)

var ErrChainAmountExhausted = errors.New("chain_amount_slots_exhausted")

type ChainNetwork struct {
	Network              string
	ChainID              uint64
	Label, TokenContract string
	Decimals             uint8
}
type ChainHead struct {
	Number    uint64
	Hash      string
	Timestamp time.Time
}
type ChainVerifyRequest struct {
	TxID, Recipient, AmountUnits string
	EarliestPaidAt               time.Time
	MinBlockNumber               uint64
}
type ChainEvidence struct {
	TxID                         string
	LogIndex                     uint64
	AmountUnits                  string
	PaidAt                       time.Time
	BlockNumber                  uint64
	BlockHash, Recipient, Sender string
	FinalizedBlockNumber         uint64
}
type ChainError struct{ Kind, Code string }

func (e *ChainError) Error() string { return e.Kind + ":" + e.Code }

type ChainVerifier interface {
	Network(channelType string) (ChainNetwork, bool)
	NormalizeAddress(network, address string) (string, bool)
	ReadHead(context.Context, string) (*ChainHead, *ChainError)
	Verify(context.Context, string, ChainVerifyRequest) (*ChainEvidence, *ChainError)
}
type SubmitTransactionInput struct {
	PaymentID                           uint
	TransactionHash                     string
	AuthorizedOrderID, AuthorizedUserID uint
	Context                             context.Context
}
type TransactionSubmitter interface {
	SubmitTransaction(SubmitTransactionInput) (*domain.Payment, error)
}

// ChainStores binds permanent invoice and receipt reservations to the existing
// payment transaction. Old providers and their test doubles need no chain store.
type ChainStores interface{ ChainPayments() ChainStore }
type ChainStore interface {
	Get(paymentID uint, lock bool) (*domain.ChainIntent, error)
	Create(*domain.ChainIntent) (bool, error)
	Update(*domain.ChainIntent) error
	ClaimReceipt(*domain.ChainReceipt) (bool, error)
	Acquire(paymentID uint, token string, now, until time.Time) (bool, error)
	Due(time.Time, int) ([]domain.ChainIntent, error)
}
