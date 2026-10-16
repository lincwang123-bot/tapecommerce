package contract

import (
	"context"
	"github.com/dujiao-next/internal/modules/payment/domain"
	"time"
)

// Discovery cursors are opaque, versioned client checkpoints. On failure callers
// retain the old cursor. Done means caught up, not permanently finished.
type ChainDiscoverRequest struct {
	Recipient, AmountUnits string
	EarliestPaidAt         time.Time
	MinBlockNumber         uint64
	Cursor                 string
}
type ChainDiscoverPage struct {
	TxHashes   []string
	NextCursor string
	Done       bool
}
type ChainDiscoverer interface {
	Discover(context.Context, string, ChainDiscoverRequest) (*ChainDiscoverPage, *ChainError)
}

// Optional extension: legacy verifier/store doubles remain compatible.
type ChainDiscoveryStore interface {
	DiscoveryDue(string, time.Time, int) ([]domain.ChainIntent, error)
	AcquireDiscovery(uint, string, time.Time, time.Time) (bool, error)
	FinishDiscovery(uint, string, string, string, time.Time) error
	AcquireCandidate(uint, string, time.Time, time.Time) (bool, error)
}
