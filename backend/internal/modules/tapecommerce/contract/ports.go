package contract

import (
	"context"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"time"
)

type Verifier interface {
	Verify(context.Context, domain.Invoice, string) (domain.Evidence, error)
}

type Store interface {
	Create(context.Context, domain.Invoice) error
	Get(context.Context, string, string) (domain.Payment, error)
	Submit(context.Context, string, string, string) error
	Record(context.Context, string, string, domain.Evidence) error
	Review(context.Context, string, string, string) error
	Claim(context.Context, string, string, time.Time, time.Duration) (domain.Job, error)
	Finish(context.Context, domain.Job, bool, string, time.Time) error
}

// Fulfill must be idempotent for job.Key, including after a process crash and
// expired lease. A plain upstream purchase POST does not satisfy this contract.
// This returns no card payload. Authorized delivery reads use the existing API.
type Fulfiller interface {
	Fulfill(context.Context, domain.Job) error
}

// AtomicAdmission is the mandatory release gate, not a convenience wrapper
// around Fulfiller. Implementations MUST use the same database transaction and
// row locks for (1) invoice shop/order + safe evidence + non-review state,
// (2) the current unexpired outbox lease, and (3) stock consumption/fulfillment.
// Review and admission must serialize on that invoice row across processes.
// Existing fulfillment effects must be recognized idempotently after a crash.
// Returning nil means the stock effect committed; completion may be atomic too.
// A network call, a second Check, or a process-local mutex does not satisfy this
// contract. bootstrap/tapecommerce.Bridge implements this for original local-stock
// orders through default-disabled live composition. StockFulfiller
// intentionally lacks this method.
type AtomicAdmission interface {
	AdmitAndFulfill(context.Context, domain.Job) error
}
