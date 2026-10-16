package application_test

import (
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	couponstore "github.com/dujiao-next/internal/modules/coupon/infrastructure/gormstore"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	memberapp "github.com/dujiao-next/internal/modules/memberlevel/application"
	memberdomain "github.com/dujiao-next/internal/modules/memberlevel/domain"
	memberstore "github.com/dujiao-next/internal/modules/memberlevel/infrastructure/gormstore"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	promotionstore "github.com/dujiao-next/internal/modules/promotion/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/shopspring/decimal"
	"testing"
)

func TestTierChangeAffectsNewOrdersKeepsIdempotentSnapshot(t *testing.T) {
	f := newCouponLimitRaceFixture(t, "tier_snapshot", 0, 0)
	if err := f.db.AutoMigrate(&memberdomain.MemberLevel{}, &memberdomain.MemberLevelPrice{}); err != nil {
		t.Fatal(err)
	}
	tiers := memberapp.NewService(memberstore.NewLevelStore(f.db), memberstore.NewPriceStore(f.db), memberstore.NewUserStore(f.db))
	level := memberdomain.MemberLevel{NameJSON: jsonmap.JSON{"zh-CN": "代理"}, Slug: "agent", DiscountRate: money.FromDecimal(decimal.NewFromInt(90)), IsActive: true}
	if err := tiers.CreateLevel(&level); err != nil {
		t.Fatal(err)
	}
	if err := tiers.SetUserLevel(1, level.ID); err != nil {
		t.Fatal(err)
	}
	svc := orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: orderstore.New(f.db, "test-guest-credential-secret-with-32-bytes"), UserStore: userstore.New(f.db), ProductStore: productstore.NewProductStore(f.db), ProductSKUStore: productstore.NewSKUStore(f.db), Queue: f.queue, PromotionRepo: promotionstore.New(f.db), CouponStore: couponstore.New(f.db), CouponUsageStore: couponstore.NewUsageStore(f.db), ExpireMinutes: 15, MemberLevelService: tiers})
	input := f.input(1)
	input.CouponCode = ""
	input.ClientRequestID = "tier-first"
	first, err := svc.CreateOrder(input)
	if err != nil {
		t.Fatal(err)
	}
	expected := f.sku.PriceAmount.Mul(decimal.RequireFromString("0.9")).Round(2)
	if !first.TotalAmount.Equal(expected) {
		t.Fatalf("got %s expected %s", first.TotalAmount, expected)
	}
	if err := tiers.SetUserLevel(1, 0); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.CreateOrder(input)
	if err != nil || replay.ID != first.ID || !replay.TotalAmount.Equal(expected) {
		t.Fatal("historical replay changed", err)
	}
	input.ClientRequestID = "tier-after-downgrade"
	next, err := svc.CreateOrder(input)
	if err != nil {
		t.Fatal(err)
	}
	if !next.TotalAmount.Equal(f.sku.PriceAmount.Decimal) {
		t.Fatal("downgrade not reflected", next.TotalAmount)
	}
}
