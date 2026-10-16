package selfhost_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dujiao-next/internal/constants"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	app "github.com/dujiao-next/internal/modules/payment/application"
	domain "github.com/dujiao-next/internal/modules/payment/domain"
	store "github.com/dujiao-next/internal/modules/payment/infrastructure/gormstore"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"
	walletapp "github.com/dujiao-next/internal/modules/wallet/application"
	walletstore "github.com/dujiao-next/internal/modules/wallet/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
)

// Production uses a single SQLite connection and a database-backed settings
// service. A transaction that reads settings through the global repository will
// wait on its own connection forever. Bound only the settings reads so this
// regression fails cleanly without leaving a blocked transaction or goroutine.
func TestSelfhostInvoicesWithDatabaseSettingsDoNotRequestASecondConnection(t *testing.T) {
	for _, kind := range []string{"recharge", "direct", "direct_earlier_order_deadline"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			if err := f.db.AutoMigrate(&settingsstore.SettingRecord{}); err != nil {
				t.Fatal(err)
			}
			settingsRepo := settingsstore.New(f.db)
			if _, err := settingsRepo.Upsert(constants.SettingKeyOrderConfig, jsonmap.JSON{
				constants.SettingFieldPaymentExpireMinutes: 7,
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w := walletstore.New(f.db)
			f.service = app.NewPaymentService(app.PaymentServiceOptions{
				PaymentStore: store.New(f.db, "synthetic-guest-secret-only"),
				ChannelStore: store.NewChannelStore(f.db),
				OrderStore:   orderstore.New(f.db, "synthetic-guest-secret-only"),
				ProductRepo:  productstore.NewProductStore(f.db),
				UserStore:    userstore.New(f.db), WalletRepo: w,
				WalletService:  walletapp.NewService(walletapp.Options{Repository: w, Transactions: w}),
				SettingService: settingsapp.NewService(settingsstore.New(f.db.WithContext(ctx))),
				ChainVerifier:  f.verifier, ExpireMinutes: 15,
			})
			sqlDB, err := f.db.DB()
			if err != nil {
				t.Fatal(err)
			}
			before := sqlDB.Stats().WaitCount
			started := time.Now()
			var p *domain.Payment
			var orderDeadline *time.Time
			if kind == "recharge" {
				result, err := f.service.CreateWalletRechargePayment(app.CreateWalletRechargePaymentInput{
					UserID: f.user.ID, ChannelID: f.channel.ID,
					Amount: money.FromDecimal(decimal.NewFromInt(30)), Currency: "USDT", Context: context.Background(),
				})
				if err != nil {
					t.Fatal(err)
				}
				p = result.Payment
			} else {
				if kind == "direct_earlier_order_deadline" {
					deadline := started.Add(3 * time.Minute)
					orderDeadline = &deadline
				}
				o := &orderdomain.Order{
					OrderNo: fmt.Sprint(time.Now().UnixNano()), UserID: f.user.ID,
					Status: constants.OrderStatusPendingPayment, Currency: "USDT",
					TotalAmount: money.FromDecimal(decimal.NewFromInt(20)), ExpiresAt: orderDeadline,
				}
				if err := f.db.Create(o).Error; err != nil {
					t.Fatal(err)
				}
				result, err := f.service.CreatePayment(app.CreatePaymentInput{
					OrderID: o.ID, ChannelID: f.channel.ID, Context: context.Background(),
				})
				if err != nil {
					t.Fatal(err)
				}
				p = result.Payment
			}
			if waits := sqlDB.Stats().WaitCount - before; waits != 0 {
				t.Errorf("invoice creation requested %d extra pooled connection(s) while holding the sole transaction connection", waits)
			}
			if err := ctx.Err(); err != nil {
				t.Errorf("settings query exhausted its deadline: %v", err)
			}
			if p == nil || p.Status != constants.PaymentStatusPending || p.ExpiredAt == nil {
				t.Fatalf("missing usable pending invoice: %+v", p)
			}
			if orderDeadline != nil {
				if !p.ExpiredAt.Equal(*orderDeadline) {
					t.Errorf("invoice outlived order deadline: got %s, want %s", p.ExpiredAt, orderDeadline)
				}
			} else if lifetime := p.ExpiredAt.Sub(started); lifetime < 7*time.Minute || lifetime > 7*time.Minute+time.Second {
				t.Errorf("invoice did not preserve the database-configured 7 minute lifetime: %s", lifetime)
			}
			// The same pool must remain usable by ordinary storefront queries.
			if _, _, err := settingsRepo.GetByKey(constants.SettingKeyOrderConfig); err != nil {
				t.Fatal(err)
			}
		})
	}
}
