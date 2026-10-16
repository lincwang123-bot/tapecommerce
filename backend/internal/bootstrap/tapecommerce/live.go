package tapecommercebootstrap

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dujiao-next/internal/config"
	fulfillmentapp "github.com/dujiao-next/internal/modules/fulfillment/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentapp "github.com/dujiao-next/internal/modules/payment/application"
	"github.com/dujiao-next/internal/modules/payment/application/settlement"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	paymentstore "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	reseller "github.com/dujiao-next/internal/modules/reseller/contract"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/evmrpc"
	tapestore "github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/gormstore"
	"golang.org/x/crypto/sha3"
	"gorm.io/gorm"
)

type OrderAuthorization interface {
	GetOrderByUserOrderNoForTenant(reseller.TenantContext, string, uint) (*orderdomain.Order, error)
	GetOrderByGuestOrderNoForTenant(reseller.TenantContext, string, string, string) (*orderdomain.Order, error)
}
type Live struct {
	statusDB   *gorm.DB
	readyMu    sync.Mutex
	cached     PublicConfig
	cacheUntil time.Time
	started    bool
	stopped    bool
	doneOnce   sync.Once
	Config     config.TapeCommerceConfig
	Bridge     *Bridge
	Orders     OrderAuthorization
	Signer     QuoteSigner
	reason     string
	done       chan struct{}
	stop       context.CancelFunc
	mutex      sync.Mutex
	cursor     string
}
type PublicConfig struct {
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Ready     bool   `json:"ready"`
	Mode      string `json:"mode"`
	ChainID   uint64 `json:"chain_id"`
	Checkout  string `json:"checkout_address,omitempty"`
	Symbol    string `json:"native_symbol"`
	TTL       int    `json:"quote_ttl_seconds"`
	Reason    string `json:"reason,omitempty"`
}
type PublicQuote struct {
	InvoiceID  string                  `json:"invoice_id"`
	PaymentID  uint                    `json:"payment_id"`
	State      string                  `json:"state"`
	ChainID    uint64                  `json:"chain_id"`
	Checkout   string                  `json:"checkout_address"`
	Domain     SigningDomain           `json:"domain"`
	Types      map[string][]TypedField `json:"types"`
	Invoice    SignedInvoice           `json:"invoice"`
	Signature  string                  `json:"signature"`
	FXSnapshot domain.FXSnapshot       `json:"fx_snapshot"`
	AmountWei  string                  `json:"amount_wei"`
	Deadline   string                  `json:"deadline"`
}

// NewLive never starts a worker or makes RPC calls. Migration is an explicit,
// enabled-only operator action. Missing configuration leaves this channel closed
// while the pre-existing payment service keeps its behavior.
func NewLive(c config.TapeCommerceConfig, db *gorm.DB, guestSecret string, orders OrderAuthorization, payments *paymentapp.PaymentService, auto *fulfillmentapp.Service) *Live {
	l := &Live{Config: c, Orders: orders, done: make(chan struct{}), statusDB: db}
	if !c.Enabled {
		l.reason = "disabled"
		return l
	}
	if !validLiveConfig(c) || db == nil || orders == nil || payments == nil || auto == nil {
		l.reason = "tapecommerce_config_incomplete"
		return l
	}
	signer, e := NewHTTPSigner(c)
	if e != nil {
		l.reason = "tapecommerce_signer_unavailable"
		return l
	}
	l.Signer = signer
	store := tapestore.New(db)
	if c.Migrate {
		if e = store.Migrate(context.Background()); e != nil {
			l.reason = "tapecommerce_migration_failed"
			return l
		}
	}
	if !store.SchemaReady() {
		l.reason = "tapecommerce_schema_not_ready"
		return l
	}
	a, e := evmrpc.NewClient(c.RPCPrimary)
	if e != nil {
		l.reason = "tapecommerce_rpc_config_invalid"
		return l
	}
	b, e := evmrpc.NewClient(c.RPCSecondary)
	if e != nil {
		l.reason = "tapecommerce_rpc_config_invalid"
		return l
	}
	l.Bridge = &Bridge{Enabled: true, DB: db, GuestSecret: guestSecret, ShopID: c.ShopID, Checkout: c.Checkout, Merchant: c.Merchant, TenantHash: c.TenantHash, ChannelID: c.ChannelID, Verifier: evmrpc.Verifier{Primary: a, Witness: b}, Auto: auto}
	l.Bridge.ChannelPolicy = func(tx *gorm.DB, o *orderdomain.Order, ch *paymentdomain.PaymentChannel) error {
		if e := payments.ValidateVerifiedStockChannel(o, ch, paymentstore.UseTransaction(tx, guestSecret).Products()); e != nil {
			return ErrIneligible
		}
		return nil
	}
	return l
}
func validLiveConfig(c config.TapeCommerceConfig) bool {
	if c.ShopID == "" || len(c.ShopID) > 128 || c.ChannelID == 0 || c.SignerSecretFile == "" || c.RPCPrimary == c.RPCSecondary {
		return false
	}
	for _, a := range []string{c.Checkout, c.Merchant, c.QuoteSigner} {
		if !domain.ValidHex(a, 20) || domain.IsZeroHex(a) {
			return false
		}
	}
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(c.ShopID))
	if !strings.EqualFold(c.TenantHash, "0x"+hex.EncodeToString(h.Sum(nil))) {
		return false
	}
	if strings.EqualFold(c.Checkout, c.Merchant) {
		return false
	}
	for _, raw := range []string{c.RPCPrimary, c.RPCSecondary} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return false
		}
	}
	return true
}
func (l *Live) localReady() error {
	if l == nil || !l.Config.Enabled || l.reason != "" || l.Bridge == nil || l.Signer == nil {
		return domain.ErrDisabled
	}
	return l.Bridge.ready()
}
func (l *Live) preflight(ctx context.Context) error {
	if e := l.localReady(); e != nil {
		return e
	}
	h, e := l.Signer.Health(ctx)
	if e != nil || !h.Ready || h.Mode != "live" || h.ChainID != 196 || h.Tenant != l.Config.ShopID || !strings.EqualFold(h.Checkout, l.Config.Checkout) || !strings.EqualFold(h.Merchant, l.Config.Merchant) || !strings.EqualFold(h.QuoteSigner, l.Config.QuoteSigner) {
		return ErrSigner
	}
	ch, e := paymentstore.NewChannelStore(l.Bridge.DB).GetByID(l.Config.ChannelID)
	if e != nil || ch == nil || !ch.IsActive || ch.DeletedAt != nil || ch.ProviderType != "tapecommerce" || ch.ChannelType != "okb" || !ch.FeeRate.IsZero() || !ch.FixedFee.IsZero() {
		return domain.ErrDisabled
	}
	return nil
}
func (l *Live) PublicConfig(ctx context.Context) PublicConfig {
	if l == nil {
		return PublicConfig{Mode: "live", ChainID: 196, Symbol: "OKB", TTL: 60, Reason: "disabled"}
	}
	l.readyMu.Lock()
	defer l.readyMu.Unlock()
	if time.Now().Before(l.cacheUntil) {
		return l.cached
	}
	v := l.publicConfig(ctx)
	l.cached = v
	l.cacheUntil = time.Now().Add(5 * time.Second)
	return v
}
func (l *Live) publicConfig(ctx context.Context) PublicConfig {
	v := PublicConfig{Mode: "live", ChainID: 196, Symbol: "OKB", TTL: 60, Reason: "disabled"}
	if l == nil {
		return v
	}
	v.Enabled = l.Config.Enabled
	if l.reason != "" {
		v.Reason = l.reason
		return v
	}
	if e := l.preflight(ctx); e != nil {
		v.Reason = "tapecommerce_unavailable"
		return v
	}
	v.Available = true
	v.Ready = true
	v.Checkout = l.Config.Checkout
	v.Reason = ""
	return v
}
func (owner Owner) key() string {
	if owner.Guest {
		return fmt.Sprintf("guest:%d", owner.OrderID)
	}
	return fmt.Sprintf("user:%d", owner.UserID)
}
func (l *Live) Quote(ctx context.Context, owner Owner, payer string) (PublicQuote, error) {
	if e := l.localReady(); e != nil {
		return PublicQuote{}, e
	}
	store := tapestore.New(l.Bridge.DB)
	if old, e := store.ByOrder(ctx, l.Config.ShopID, owner.OrderID); e == nil {
		if e := l.preflight(ctx); e != nil {
			return PublicQuote{}, e
		}
		return l.existingQuote(ctx, owner, payer, old)
	} else if !errors.Is(e, domain.ErrNotFound) {
		return PublicQuote{}, e
	}
	q, e := l.Bridge.PrepareOwned(ctx, owner, payer)
	if e != nil {
		return PublicQuote{}, e
	}
	// The private Sign endpoint performs a fresh live identity/contract preflight.
	// Do not duplicate its network work with an additional health call here.
	signed, e := l.Signer.Sign(ctx, SignRequest{OrderID: q.OrderID, Tenant: q.Tenant, Buyer: owner.key(), Currency: q.Currency, AmountUSDT: q.AmountUSDT, Status: q.Status, Payer: q.Payer, ExpiresAtMS: q.ExpiresAtMS})
	if e != nil {
		return PublicQuote{}, e
	}
	if signed.Domain.Name != "TapeCommerceCheckout" || signed.Domain.Version != "1" || signed.Domain.ChainID != 196 || !strings.EqualFold(signed.Domain.VerifyingContract, l.Config.Checkout) || len(signed.Types) != 1 || !reflect.DeepEqual(signed.Types["Invoice"], invoiceFields) {
		return PublicQuote{}, ErrSigner
	}
	deadline, e := strconv.ParseUint(signed.Invoice.Deadline, 10, 64)
	if e != nil || strconv.FormatUint(deadline, 10) != signed.Invoice.Deadline {
		return PublicQuote{}, ErrSigner
	}
	i := domain.Invoice{InvoiceID: signed.Invoice.InvoiceID, ShopID: l.Config.ShopID, OrderID: owner.OrderID, ChainID: 196, Checkout: l.Config.Checkout, TenantHash: signed.Invoice.TenantHash, Payer: signed.Invoice.Payer, Merchant: signed.Invoice.Merchant, AmountWei: signed.Invoice.AmountWei, Deadline: deadline, FXSnapshot: signed.FXSnapshot}
	_, e = l.Bridge.PersistOwned(ctx, owner, i, signed.Signature)
	if errors.Is(e, domain.ErrConflict) {
		if old, err := store.ByOrder(ctx, l.Config.ShopID, owner.OrderID); err == nil {
			return l.existingQuote(ctx, owner, payer, old)
		}
	}
	if e != nil {
		return PublicQuote{}, e
	}
	stored, e := store.Get(ctx, l.Config.ShopID, i.InvoiceID)
	if e != nil {
		return PublicQuote{}, e
	}
	return l.existingQuote(ctx, owner, payer, stored)
}
func (l *Live) existingQuote(ctx context.Context, owner Owner, payer string, p domain.Payment) (PublicQuote, error) {
	p, bind, e := l.Bridge.authorizedOwned(ctx, owner, p.Invoice.InvoiceID)
	if e != nil {
		return PublicQuote{}, e
	}
	if !strings.EqualFold(p.Invoice.Payer, payer) {
		return PublicQuote{}, domain.ErrConflict
	}
	if p.State != domain.QuoteIssued {
		return PublicQuote{}, domain.ErrConflict
	}
	if p.Invoice.Deadline <= uint64(time.Now().Unix()) {
		return PublicQuote{}, ErrExpired
	}
	// A stored signature remains usable on chain until its deadline. Never
	// offer it again after the original order was canceled, paid, refunded or
	// repriced. Use the admission lock order for a coherent read.
	e = l.Bridge.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		o, err := orderstore.New(tx, l.Bridge.GuestSecret).GetByIDForUpdateWithChildren(owner.OrderID)
		if err != nil {
			return err
		}
		current, err := tapestore.New(tx).GetForUpdate(ctx, l.Config.ShopID, p.Invoice.InvoiceID)
		if err != nil {
			return err
		}
		if !owner.owns(o) {
			return domain.ErrNotFound
		}
		if current.State != domain.QuoteIssued {
			return domain.ErrConflict
		}
		if current.Invoice.Deadline <= uint64(time.Now().Unix()) {
			return ErrExpired
		}
		if settlement.ValidateOrder(o, owner.UserID, current.Invoice.FXSnapshot.AmountUSDT, time.Unix(int64(current.Invoice.Deadline), 0)) != nil {
			return ErrIneligible
		}
		p = current
		return nil
	})
	if e != nil {
		return PublicQuote{}, e
	}
	i := p.Invoice
	deadline := strconv.FormatUint(i.Deadline, 10)
	return PublicQuote{InvoiceID: i.InvoiceID, PaymentID: bind.PaymentID, State: p.State, ChainID: 196, Checkout: i.Checkout, Domain: SigningDomain{"TapeCommerceCheckout", "1", 196, i.Checkout}, Types: map[string][]TypedField{"Invoice": invoiceFields}, Invoice: SignedInvoice{i.InvoiceID, i.TenantHash, i.Payer, i.Merchant, i.AmountWei, deadline}, Signature: bind.Signature, FXSnapshot: i.FXSnapshot, AmountWei: i.AmountWei, Deadline: deadline}, nil
}
func (l *Live) Status(ctx context.Context, owner Owner) (InvoiceStatus, error) {
	if l == nil || !owner.valid() {
		return InvoiceStatus{}, domain.ErrNotFound
	}
	if l.localReady() != nil {
		if l.statusDB == nil || !tapestore.New(l.statusDB).SchemaReady() {
			return InvoiceStatus{}, domain.ErrNotFound
		}
		p, e := tapestore.New(l.statusDB).ByOrder(ctx, l.Config.ShopID, owner.OrderID)
		if e != nil {
			return InvoiceStatus{}, e
		}
		bind, e := tapestore.New(l.statusDB).Binding(ctx, p.Invoice.InvoiceID)
		if e != nil || bind.BuyerID != owner.UserID || bind.Guest != owner.Guest {
			return InvoiceStatus{}, domain.ErrNotFound
		}
		v := InvoiceStatus{InvoiceID: p.Invoice.InvoiceID, OrderID: owner.OrderID, PaymentID: bind.PaymentID, State: p.State, TxHash: p.TxHash, Reason: "channel_disabled"}
		if p.Evidence != nil {
			v.Safe = p.Evidence.Safe
			v.BlockNumber = p.Evidence.BlockNumber
		}
		return v, nil
	}
	p, e := tapestore.New(l.Bridge.DB).ByOrder(ctx, l.Config.ShopID, owner.OrderID)
	if e != nil {
		return InvoiceStatus{}, e
	}
	return l.Bridge.StatusOwned(ctx, owner, p.Invoice.InvoiceID)
}
func (l *Live) Submit(ctx context.Context, owner Owner, id, hash string) (InvoiceStatus, error) {
	if e := l.localReady(); e != nil {
		return InvoiceStatus{}, e
	}
	return l.Bridge.SubmitOwned(ctx, owner, id, hash)
}
