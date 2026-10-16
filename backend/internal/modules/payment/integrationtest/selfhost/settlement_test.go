package selfhost_test

import (
	"context"
	"fmt"
	"github.com/dujiao-next/internal/constants"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	app "github.com/dujiao-next/internal/modules/payment/application"
	contract "github.com/dujiao-next/internal/modules/payment/contract"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	store "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletdomain "github.com/dujiao-next/internal/modules/wallet/domain"
	walletstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type verifier struct {
	discover func(contract.ChainDiscoverRequest) (*contract.ChainDiscoverPage, *contract.ChainError)
	mu       sync.Mutex
	kind     string
	evidence *contract.ChainEvidence
	onHead   func()
}

func (*verifier) Network(s string) (contract.ChainNetwork, bool) {
	return contract.ChainNetwork{Network: "bsc", ChainID: 56, Label: "BSC", TokenContract: "0x" + strings.Repeat("5", 40), Decimals: 18}, s == "usdtbep20"
}
func (*verifier) NormalizeAddress(_, s string) (string, bool) {
	return strings.ToLower(s), len(s) == 42
}
func (v *verifier) ReadHead(context.Context, string) (*contract.ChainHead, *contract.ChainError) {
	if v.onHead != nil {
		v.onHead()
	}
	return &contract.ChainHead{Number: 100, Hash: "head", Timestamp: time.Now()}, nil
}
func (v *verifier) Verify(_ context.Context, _ string, r contract.ChainVerifyRequest) (*contract.ChainEvidence, *contract.ChainError) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.kind != "" {
		return nil, &contract.ChainError{Kind: v.kind, Code: "fixture_" + v.kind}
	}
	if v.evidence != nil {
		return v.evidence, nil
	}
	return &contract.ChainEvidence{TxID: r.TxID, LogIndex: 1, AmountUnits: r.AmountUnits, Recipient: r.Recipient, Sender: "fixture", PaidAt: time.Now(), BlockNumber: 101, FinalizedBlockNumber: 102, BlockHash: "block"}, nil
}

type fixture struct {
	db       *gorm.DB
	service  *app.PaymentService
	verifier *verifier
	user     *userdomain.User
	channel  *domain.PaymentChannel
}

func setup(t *testing.T) *fixture { return setupWithQueue(t, nil) }
func setupWithQueue(t *testing.T, queue contract.Queue) *fixture {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "isolated.sqlite")), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { sql.Close() })
	if e = db.AutoMigrate(&userdomain.User{}, &orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &productdomain.Product{}, &productdomain.ProductSKU{}, &domain.Payment{}, &domain.PaymentChannel{}, &domain.ChainIntent{}, &domain.ChainReceipt{}, &walletdomain.Account{}, &walletdomain.Transaction{}, &walletdomain.RechargeOrder{}); e != nil {
		t.Fatal(e)
	}
	w := walletstore.New(db)
	v := &verifier{}
	u := &userdomain.User{Email: "fixture@example.test", Status: "active"}
	db.Create(u)
	ch := &domain.PaymentChannel{Name: "synthetic", ProviderType: "selfhost", ChannelType: "usdtbep20", InteractionMode: "qr", IsActive: true, ConfigJSON: jsonmap.JSON{"receiving_address": "0x" + strings.Repeat("1", 40)}}
	db.Create(ch)
	s := app.NewPaymentService(app.PaymentServiceOptions{Queue: queue, PaymentStore: store.New(db, "synthetic-guest-secret-only"), ChannelStore: store.NewChannelStore(db), OrderStore: orderstore.New(db, "synthetic-guest-secret-only"), ProductRepo: productstore.NewProductStore(db), UserStore: userstore.New(db), WalletRepo: w, WalletService: walletapp.NewService(walletapp.Options{Repository: w, Transactions: w}), ChainVerifier: v, ExpireMinutes: 15})
	return &fixture{db, s, v, u, ch}
}
func (f *fixture) order(t *testing.T) *domain.Payment {
	t.Helper()
	o := &orderdomain.Order{OrderNo: fmt.Sprint(time.Now().UnixNano()), UserID: f.user.ID, Status: constants.OrderStatusPendingPayment, Currency: "USDT", TotalAmount: money.FromDecimal(decimal.NewFromInt(20))}
	if e := f.db.Create(o).Error; e != nil {
		t.Fatal(e)
	}
	r, e := f.service.CreatePayment(app.CreatePaymentInput{OrderID: o.ID, ChannelID: f.channel.ID, Context: context.Background()})
	if e != nil {
		t.Fatal(e)
	}
	return r.Payment
}
func submit(f *fixture, p *domain.Payment, hash string) (*domain.Payment, error) {
	return f.service.SubmitTransaction(app.SubmitTransactionInput{PaymentID: p.ID, TransactionHash: hash, AuthorizedOrderID: p.OrderID, AuthorizedUserID: f.user.ID, Context: context.Background()})
}
func TestDirectAndRechargeSettleExactlyOnce(t *testing.T) {
	f := setup(t)
	p := f.order(t)
	var intent domain.ChainIntent
	f.db.First(&intent, p.ID)
	if intent.ChainAmount == "20.000000" || intent.ExpectedUnits == "" {
		t.Fatal("missing permanent exact amount")
	}
	hash := "0x" + strings.Repeat("a", 64)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := submit(f, p, hash); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	var count int64
	f.db.Model(&domain.ChainReceipt{}).Count(&count)
	if count != 1 {
		t.Fatalf("receipts=%d", count)
	}
	var o orderdomain.Order
	f.db.First(&o, p.OrderID)
	if o.Status != constants.OrderStatusPaid {
		t.Fatal(o.Status)
	}
	r, e := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{UserID: f.user.ID, ChannelID: f.channel.ID, Amount: money.FromDecimal(decimal.NewFromInt(30)), Currency: "USDT", Context: context.Background()})
	if e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if _, e = submit(f, r.Payment, "0x"+strings.Repeat("b", 64)); e != nil {
			t.Fatal(e)
		}
	}
	var txs []walletdomain.Transaction
	f.db.Find(&txs)
	if len(txs) != 1 || txs[0].Amount.String() != "30.00" {
		t.Fatalf("wallet credited incorrectly: %+v", txs)
	}
}
