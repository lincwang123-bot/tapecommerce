package tapecommercebootstrap

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	fulfillmentapp "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	"github.com/dujiao-next/internal/modules/payment/application/settlement"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	tapeapp "github.com/dujiao-next/internal/modules/tapecommerce/application"
	tapecontract "github.com/dujiao-next/internal/modules/tapecommerce/contract"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	tapestore "github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/gormstore"
	"gorm.io/gorm"
)

// Bridge owns atomic admission for both explicit live composition and the
// isolated demo. Production HTTP derives Owner from the original order auth;
// signed quotes come only from the private signer, never browser price inputs.
type Bridge struct {
	ChannelPolicy                                       func(*gorm.DB, *orderdomain.Order, *paymentdomain.PaymentChannel) error
	Enabled                                             bool
	DB                                                  *gorm.DB
	GuestSecret, ShopID, Checkout, Merchant, TenantHash string
	ChannelID                                           uint
	Verifier                                            tapecontract.Verifier
	Auto                                                *fulfillmentapp.Service
}

var _ tapecontract.AtomicAdmission = (*Bridge)(nil)

// Owner is constructed only after the original member/guest order authorization.
// It is never decoded from a browser request. Guest ownership is scoped to one order.
type Owner struct {
	OrderID uint
	UserID  uint
	Guest   bool
}

func (o Owner) valid() bool {
	return o.OrderID > 0 && ((o.UserID > 0 && !o.Guest) || (o.UserID == 0 && o.Guest))
}
func (o Owner) owns(order *orderdomain.Order) bool {
	return o.valid() && order != nil && order.ID == o.OrderID && order.UserID == o.UserID && (!o.Guest || (order.GuestEmail != "" && order.GuestPassword != "")) && order.ResellerID == nil
}

type PreparedQuote struct {
	OrderID     uint   `json:"orderId"`
	BuyerID     uint   `json:"buyerId"`
	Tenant      string `json:"tenant"`
	Currency    string `json:"currency"`
	AmountUSDT  string `json:"amountUSDT"`
	Status      string `json:"status"`
	Payer       string `json:"payer"`
	ExpiresAtMS int64  `json:"expiresAtMs"`
}
type QuoteResult struct {
	InvoiceID string `json:"invoiceId"`
	PaymentID uint   `json:"paymentId"`
	State     string `json:"state"`
}
type InvoiceStatus struct {
	InvoiceID   string `json:"invoiceId"`
	OrderID     uint   `json:"orderId"`
	PaymentID   uint   `json:"paymentId"`
	State       string `json:"state"`
	TxHash      string `json:"txHash,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Safe        bool   `json:"safe"`
	BlockNumber uint64 `json:"blockNumber,omitempty"`
}
type Delivery struct {
	InvoiceID   string     `json:"invoiceId"`
	OrderID     uint       `json:"orderId"`
	Payload     string     `json:"payload"`
	DeliveredAt *time.Time `json:"deliveredAt"`
}

func (b *Bridge) ready() error {
	if !b.Enabled {
		return domain.ErrDisabled
	}
	if b.DB == nil || b.GuestSecret == "" || b.ShopID == "" || b.ChannelID == 0 || b.Verifier == nil || !domain.ValidHex(b.Checkout, 20) || !domain.ValidHex(b.Merchant, 20) || !domain.ValidHex(b.TenantHash, 32) {
		return domain.ErrInvalid
	}
	return nil
}
func (b *Bridge) service() tapeapp.Service {
	return tapeapp.Service{Enabled: b.Enabled, ShopID: b.ShopID, Store: tapestore.New(b.DB), Verifier: b.Verifier, Admission: b}
}

func (b *Bridge) Prepare(ctx context.Context, buyer, orderID uint, payer string) (PreparedQuote, error) {
	return b.PrepareOwned(ctx, Owner{OrderID: orderID, UserID: buyer}, payer)
}
func (b *Bridge) PrepareOwned(ctx context.Context, owner Owner, payer string) (PreparedQuote, error) {
	buyer, orderID := owner.UserID, owner.OrderID
	var result PreparedQuote
	if err := b.ready(); err != nil {
		return result, err
	}
	if !owner.valid() || !domain.ValidHex(payer, 20) || domain.IsZeroHex(payer) {
		return result, domain.ErrInvalid
	}
	err := b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		o, err := orderstore.New(tx, b.GuestSecret).GetByIDForUpdateWithChildren(orderID)
		if err != nil {
			return err
		}
		if !owner.owns(o) {
			return domain.ErrNotFound
		}
		if settlement.ValidateOrder(o, buyer, o.TotalAmount.String(), time.Now().Add(60*time.Second)) != nil {
			return domain.ErrReview
		}
		if err := tapestore.New(tx).Prepare(ctx, tapestore.Preparation{ShopID: b.ShopID, OrderID: orderID, BuyerID: buyer, Guest: owner.Guest, Payer: payer}); err != nil {
			return err
		}
		result = PreparedQuote{OrderID: orderID, BuyerID: buyer, Tenant: b.ShopID, Currency: o.Currency, AmountUSDT: o.TotalAmount.String(), Status: "pending", Payer: strings.ToLower(payer), ExpiresAtMS: o.ExpiresAt.UnixMilli()}
		return nil
	})
	return result, err
}

// Persist accepts a signature only from the authenticated trusted signer port.
// It independently validates math, fixed instance identities, pinned payer,
// buyer ownership, current DB amount and lifetime. The signature is public audit
// material, not proof that a browser supplied quote is trusted. No HTTP public
// route in the production service accepts an invoice or signature input.
func (b *Bridge) Persist(ctx context.Context, buyer uint, i domain.Invoice, signature string) (QuoteResult, error) {
	return b.PersistOwned(ctx, Owner{OrderID: i.OrderID, UserID: buyer}, i, signature)
}
func (b *Bridge) PersistOwned(ctx context.Context, owner Owner, i domain.Invoice, signature string) (QuoteResult, error) {
	buyer := owner.UserID
	var result QuoteResult
	if err := b.ready(); err != nil {
		return result, err
	}
	i = i.Normalize()
	signature = strings.ToLower(signature)
	if !owner.valid() || owner.OrderID != i.OrderID || i.ShopID != b.ShopID || i.Checkout != strings.ToLower(b.Checkout) || i.Merchant != strings.ToLower(b.Merchant) || i.TenantHash != strings.ToLower(b.TenantHash) || !domain.ValidHex(signature, 65) || (signature[len(signature)-2:] != "1b" && signature[len(signature)-2:] != "1c") {
		return result, domain.ErrInvalid
	}
	if err := i.Validate(); err != nil {
		return result, err
	}
	err := b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ports := paymentstore.UseTransaction(tx, b.GuestSecret)
		o, err := ports.Orders().GetByIDForUpdateWithChildren(i.OrderID)
		if err != nil {
			return err
		}
		if !owner.owns(o) {
			return domain.ErrNotFound
		}
		ts := tapestore.New(tx)
		old, err := ts.Get(ctx, b.ShopID, i.InvoiceID)
		if err == nil {
			bind, e := ts.Binding(ctx, i.InvoiceID)
			if e != nil {
				return e
			}
			if old.Invoice != i || bind.BuyerID != buyer || bind.Guest != owner.Guest || bind.Signature != signature {
				return domain.ErrConflict
			}
			result = QuoteResult{i.InvoiceID, bind.PaymentID, old.State}
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		now := time.Now()
		if err := i.FXSnapshot.ValidateAtIssue(now); err != nil {
			return err
		}
		if i.Deadline <= uint64(now.Unix()) || settlement.ValidateOrder(o, buyer, i.FXSnapshot.AmountUSDT, time.Unix(int64(i.Deadline), 0)) != nil {
			return domain.ErrReview
		}
		prepared, err := ts.Prepared(ctx, b.ShopID, i.OrderID)
		if err != nil {
			return err
		}
		if prepared.BuyerID != buyer || prepared.Guest != owner.Guest || prepared.Payer != i.Payer {
			return domain.ErrConflict
		}
		channel, err := ports.PaymentChannels().GetByID(b.ChannelID)
		if err != nil {
			return err
		}
		if channel == nil || !channel.IsActive || channel.ProviderType != "tapecommerce" || channel.ChannelType != "okb" || !channel.FeeRate.IsZero() || !channel.FixedFee.IsZero() {
			return domain.ErrDisabled
		}
		if b.ChannelPolicy != nil {
			if err := b.ChannelPolicy(tx, o, channel); err != nil {
				return err
			}
		}
		// The unique (shop, order) key prevents quote refresh from stranding any
		// still-payable historical signature. Everything rolls back on conflict.
		if err := ts.Create(ctx, i); err != nil {
			return err
		}
		payment := &paymentdomain.Payment{OrderID: o.ID, ChannelID: b.ChannelID, ProviderType: "tapecommerce", ChannelType: "okb", InteractionMode: "wallet", Amount: o.TotalAmount, FeePolicy: "none", Currency: "USDT", Status: constants.PaymentStatusPending, CreatedAt: now, UpdatedAt: now}
		if err := ports.Payments().Create(payment); err != nil {
			return err
		}
		if err := ts.Bind(ctx, tapestore.Binding{InvoiceID: i.InvoiceID, BuyerID: buyer, Guest: owner.Guest, PaymentID: payment.ID, Signature: signature}); err != nil {
			return err
		}
		result = QuoteResult{i.InvoiceID, payment.ID, domain.QuoteIssued}
		return nil
	})
	return result, err
}

func (b *Bridge) authorized(ctx context.Context, buyer uint, id string) (domain.Payment, tapestore.Binding, error) {
	if err := b.ready(); err != nil {
		return domain.Payment{}, tapestore.Binding{}, err
	}
	p, err := tapestore.New(b.DB).Get(ctx, b.ShopID, id)
	if err != nil {
		return p, tapestore.Binding{}, err
	}
	return b.authorizedOwned(ctx, Owner{OrderID: p.Invoice.OrderID, UserID: buyer}, id)
}
func (b *Bridge) authorizedOwned(ctx context.Context, owner Owner, id string) (domain.Payment, tapestore.Binding, error) {
	if err := b.ready(); err != nil {
		return domain.Payment{}, tapestore.Binding{}, err
	}
	ts := tapestore.New(b.DB)
	p, err := ts.Get(ctx, b.ShopID, id)
	if err != nil {
		return p, tapestore.Binding{}, err
	}
	bind, err := ts.Binding(ctx, id)
	if err != nil {
		return p, bind, err
	}
	o, err := orderstore.New(b.DB, b.GuestSecret).GetByID(p.Invoice.OrderID)
	if err != nil {
		return p, bind, err
	}
	if !owner.owns(o) || bind.BuyerID != owner.UserID || bind.Guest != owner.Guest {
		return domain.Payment{}, tapestore.Binding{}, domain.ErrNotFound
	}
	return p, bind, nil
}
func (b *Bridge) Submit(ctx context.Context, buyer uint, id, hash string) (InvoiceStatus, error) {
	p, _, err := b.authorized(ctx, buyer, id)
	if err != nil {
		return InvoiceStatus{}, err
	}
	return b.SubmitOwned(ctx, Owner{OrderID: p.Invoice.OrderID, UserID: buyer}, id, hash)
}
func (b *Bridge) SubmitOwned(ctx context.Context, owner Owner, id, hash string) (InvoiceStatus, error) {
	if _, _, err := b.authorizedOwned(ctx, owner, id); err != nil {
		return InvoiceStatus{}, err
	}
	if err := b.service().Submit(ctx, id, hash); err != nil {
		return InvoiceStatus{}, err
	}
	return b.StatusOwned(ctx, owner, id)
}
func (b *Bridge) Status(ctx context.Context, buyer uint, id string) (InvoiceStatus, error) {
	p, _, err := b.authorized(ctx, buyer, id)
	if err != nil {
		return InvoiceStatus{}, err
	}
	return b.StatusOwned(ctx, Owner{OrderID: p.Invoice.OrderID, UserID: buyer}, id)
}
func (b *Bridge) StatusOwned(ctx context.Context, owner Owner, id string) (InvoiceStatus, error) {
	p, bind, err := b.authorizedOwned(ctx, owner, id)
	if err != nil {
		return InvoiceStatus{}, err
	}
	if p.TxHash != "" && p.State != domain.ReviewRequired {
		err = b.service().DeliverOne(ctx, id)
	}
	if err != nil && !errors.Is(err, domain.ErrPending) && !errors.Is(err, domain.ErrUnsafe) && !errors.Is(err, domain.ErrRPC) && !errors.Is(err, domain.ErrLease) && !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrReview) && !errors.Is(err, domain.ErrReceipt) && !errors.Is(err, domain.ErrReorg) {
		return InvoiceStatus{}, err
	}
	p, err = tapestore.New(b.DB).Get(ctx, b.ShopID, id)
	if err != nil {
		return InvoiceStatus{}, err
	}
	result := InvoiceStatus{InvoiceID: p.Invoice.InvoiceID, OrderID: p.Invoice.OrderID, PaymentID: bind.PaymentID, State: p.State, TxHash: p.TxHash, Reason: p.Reason}
	if p.Evidence != nil {
		result.Safe = p.Evidence.Safe
		result.BlockNumber = p.Evidence.BlockNumber
	}
	return result, nil
}

func (b *Bridge) ClaimDelivery(ctx context.Context, buyer uint, id string) (Delivery, error) {
	var result Delivery
	p, bind, err := b.authorized(ctx, buyer, id)
	if err != nil {
		return result, err
	}
	err = b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		o, err := orderstore.New(tx, b.GuestSecret).GetByIDForUpdateWithChildren(p.Invoice.OrderID)
		if err != nil {
			return err
		}
		current, err := tapestore.New(tx).GetForUpdate(ctx, b.ShopID, id)
		if err != nil {
			return err
		}
		if o == nil || o.UserID != buyer || bind.BuyerID != buyer {
			return domain.ErrNotFound
		}
		if current.State != domain.Delivered || o.Status != constants.OrderStatusCompleted || !o.RefundedAmount.IsZero() {
			return domain.ErrReview
		}
		leafIDs := []uint{o.ID}
		if len(o.Children) > 0 {
			leafIDs = nil
			for _, c := range o.Children {
				if c.Status != constants.OrderStatusCompleted || !c.RefundedAmount.IsZero() {
					return domain.ErrReview
				}
				leafIDs = append(leafIDs, c.ID)
			}
		}
		var payloads []string
		var deliveredAt *time.Time
		for _, leafID := range leafIDs {
			f, err := fulfillmentstore.New(tx).GetByOrderID(leafID)
			if err != nil {
				return err
			}
			if f == nil || f.Type != constants.FulfillmentTypeAuto || f.Status != constants.FulfillmentStatusDelivered {
				return domain.ErrReview
			}
			payloads = append(payloads, f.Payload)
			deliveredAt = f.DeliveredAt
		}
		result = Delivery{InvoiceID: current.Invoice.InvoiceID, OrderID: o.ID, Payload: strings.Join(payloads, "\n"), DeliveredAt: deliveredAt}
		return nil
	})
	return result, err
}

// AdmitAndFulfill commits payment, order, original stock allocation, original
// delivery row and outbox completion together. It shares order locks with the
// legacy payment/cancellation/refund paths and invoice locks with Review.
func (b *Bridge) AdmitAndFulfill(ctx context.Context, j domain.Job) error {
	if err := b.ready(); err != nil {
		return err
	}
	if j.ShopID != b.ShopID {
		return domain.ErrReview
	}
	bind, err := tapestore.New(b.DB).Binding(ctx, j.InvoiceID)
	if err != nil {
		return err
	}
	var deliveredOrder *orderdomain.Order
	var parentStatus string
	err = b.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ports := paymentstore.UseTransaction(tx, b.GuestSecret)
		o, err := ports.Orders().GetByIDForUpdateWithChildren(j.OrderID)
		if err != nil {
			return err
		}
		if o != nil {
			sort.Slice(o.Children, func(i, k int) bool { return o.Children[i].ID < o.Children[k].ID })
			for idx, child := range o.Children {
				locked, err := ports.Orders().GetByIDForUpdateWithChildren(child.ID)
				if err != nil {
					return err
				}
				if locked == nil {
					return domain.ErrReview
				}
				o.Children[idx] = *locked
			}
		}
		payment, err := ports.Payments().GetByIDForUpdate(bind.PaymentID)
		if err != nil {
			return err
		}
		ts := tapestore.New(tx)
		p, err := ts.LockAdmission(ctx, j, time.Now())
		if err != nil {
			return err
		}
		if p.Invoice.OrderID != j.OrderID || !(Owner{OrderID: j.OrderID, UserID: bind.BuyerID, Guest: bind.Guest}).owns(o) || payment == nil || payment.ChannelID != b.ChannelID || p.Invoice.Checkout != strings.ToLower(b.Checkout) || p.Invoice.Merchant != strings.ToLower(b.Merchant) || p.Invoice.TenantHash != strings.ToLower(b.TenantHash) {
			return domain.ErrReview
		}
		paidAt := time.Unix(int64(p.Evidence.PaidAt), 0)
		if p.Evidence.PaidAt > p.Invoice.Deadline || p.Evidence.PaidAt < p.Invoice.FXSnapshot.IssuedAtMS/1000 {
			return domain.ErrReview
		}
		now := time.Now()
		if err := settlement.ApplyVerifiedStock(ports, o, payment, bind.BuyerID, p.Invoice.FXSnapshot.AmountUSDT, p.TxHash, paidAt, now); err != nil {
			if errors.Is(err, settlement.ErrIneligible) {
				return domain.ErrReview
			}
			return err
		}
		var f *fulfillmentdomain.Fulfillment
		leafIDs := []uint{j.OrderID}
		if len(o.Children) > 0 {
			leafIDs = nil
			for _, child := range o.Children {
				leafIDs = append(leafIDs, child.ID)
			}
		}
		for _, leafID := range leafIDs {
			f, deliveredOrder, parentStatus, err = fulfillmentapp.CreateAutoInTransaction(ports, leafID, now)
			if err != nil {
				break
			}
		}
		if err != nil {
			if errors.Is(err, fulfillmentapp.ErrCardSecretInsufficient) || errors.Is(err, fulfillmentapp.ErrFulfillmentExists) || errors.Is(err, fulfillmentapp.ErrOrderStatusInvalid) {
				return domain.ErrReview
			}
			return err
		}
		if f == nil {
			return domain.ErrReview
		}
		return ts.Finish(ctx, j, true, "", time.Time{})
	})
	if err == nil && b.Auto != nil {
		b.Auto.NotifyAutoCommitted(deliveredOrder, parentStatus)
	}
	return err
}
