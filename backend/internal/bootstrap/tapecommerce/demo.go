package tapecommercebootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dujiao-next/internal/constants"
	carddomain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	coupondomain "github.com/dujiao-next/internal/modules/coupon/domain"
	couponstore "github.com/dujiao-next/internal/modules/coupon/infrastructure/gormstore"
	fulfillmentapp "github.com/dujiao-next/internal/modules/fulfillment/application"
	fulfillmentdomain "github.com/dujiao-next/internal/modules/fulfillment/domain"
	fulfillmentstore "github.com/dujiao-next/internal/modules/fulfillment/infrastructure/gormstore"
	userdomain "github.com/dujiao-next/internal/modules/identity/user/domain"
	userstore "github.com/dujiao-next/internal/modules/identity/user/infrastructure/gormstore"
	orderapp "github.com/dujiao-next/internal/modules/order/application"
	orderdomain "github.com/dujiao-next/internal/modules/order/domain"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	paymentdomain "github.com/dujiao-next/internal/modules/payment/domain"
	promotiondomain "github.com/dujiao-next/internal/modules/promotion/domain"
	promotionstore "github.com/dujiao-next/internal/modules/promotion/infrastructure/gormstore"
	settingsapp "github.com/dujiao-next/internal/modules/settings/application"
	settingsstore "github.com/dujiao-next/internal/modules/settings/infrastructure/gormstore"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	tapestore "github.com/dujiao-next/internal/modules/tapecommerce/infrastructure/gormstore"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"github.com/dujiao-next/internal/shared/money"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const DemoShopID = "tapecommerce-isolated-demo"

// Demo owns only a newly-created temporary directory/database and synthetic
// licensed launch-kit inventory. No existing DB path or production config is
// accepted. Close removes exactly that owned directory after closing SQLite.
type Demo struct {
	Bridge    *Bridge
	Orders    *orderapp.OrderService
	DB        *gorm.DB
	Root      string
	ProductID uint
	queue     *demoQueue
	closeOnce sync.Once
	closeErr  error
}
type CatalogProduct struct {
	ID              uint   `json:"id"`
	Title           string `json:"title"`
	AmountUSDT      string `json:"amountUSDT"`
	Currency        string `json:"currency"`
	FulfillmentType string `json:"fulfillmentType"`
}

func OpenDemo(parent string, options Bridge) (d *Demo, err error) {
	if len(options.GuestSecret) < 32 {
		return nil, domain.ErrInvalid
	}
	root, err := os.MkdirTemp(parent, "tapecommerce-db-")
	if err != nil {
		return nil, err
	}
	d = &Demo{Root: root}
	owned := d
	defer func() {
		if err != nil {
			_ = owned.Close()
		}
	}()
	d.DB, err = gorm.Open(sqlite.Open(filepath.Join(root, "demo.db")), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sql, err := d.DB.DB()
	if err != nil {
		return nil, err
	}
	sql.SetMaxOpenConns(1)
	if err = d.DB.AutoMigrate(&userdomain.User{}, &categorydomain.Category{}, &productdomain.Product{}, &productdomain.ProductSKU{}, &orderdomain.Order{}, &orderdomain.OrderItem{}, &fulfillmentdomain.Fulfillment{}, &carddomain.Secret{}, &carddomain.Batch{}, &paymentdomain.Payment{}, &paymentdomain.PaymentChannel{}, &coupondomain.Coupon{}, &coupondomain.CouponUsage{}, &promotiondomain.Promotion{}, &settingsstore.SettingRecord{}, &demoBudget{}); err != nil {
		return nil, err
	}
	if err = d.DB.Create(&[]demoBudget{{Key: "buyers"}, {Key: "orders"}}).Error; err != nil {
		return nil, err
	}
	if err = tapestore.New(d.DB).Migrate(context.Background()); err != nil {
		return nil, err
	}
	if _, err = settingsstore.New(d.DB).Upsert(constants.SettingKeySiteConfig, jsonmap.JSON{constants.SettingFieldSiteCurrency: "USDT"}); err != nil {
		return nil, err
	}
	cat := categorydomain.Category{Slug: "tapecommerce-demo", NameJSON: jsonmap.JSON{"en": "TapeCommerce digital kits"}, IsActive: true}
	if err = d.DB.Create(&cat).Error; err != nil {
		return nil, err
	}
	price := money.FromDecimal(decimal.NewFromInt(1))
	product := productdomain.Product{CategoryID: cat.ID, Slug: "tapecommerce-launch-kit", TitleJSON: jsonmap.JSON{"en": "TapeCommerce Launch Kit", "zh-CN": "TapeCommerce 数字开店工具包"}, PriceAmount: price, PurchaseType: constants.ProductPurchaseMember, FulfillmentType: constants.FulfillmentTypeAuto, IsActive: true}
	if err = d.DB.Create(&product).Error; err != nil {
		return nil, err
	}
	d.ProductID = product.ID
	sku := productdomain.ProductSKU{ProductID: product.ID, SKUCode: productdomain.DefaultSKUCode, PriceAmount: price, IsActive: true}
	if err = d.DB.Create(&sku).Error; err != nil {
		return nil, err
	}
	cards := make([]carddomain.Secret, 1000)
	for n := range cards {
		nonce, e := randomDemoID()
		if e != nil {
			return nil, e
		}
		cards[n] = carddomain.Secret{ProductID: product.ID, SKUID: sku.ID, Secret: "TAPE-LAUNCH-KIT-DEMO-" + nonce, Status: carddomain.StatusAvailable}
	}
	if err = d.DB.CreateInBatches(cards, 100).Error; err != nil {
		return nil, err
	}
	channel := paymentdomain.PaymentChannel{Name: "Isolated TapeCommerce OKB", ProviderType: "tapecommerce", ChannelType: "okb", InteractionMode: "wallet", IsActive: true}
	if err = d.DB.Create(&channel).Error; err != nil {
		return nil, err
	}
	osStore := orderstore.New(d.DB, options.GuestSecret)
	options.DB = d.DB
	options.ShopID = DemoShopID
	options.ChannelID = channel.ID
	options.Auto = fulfillmentapp.New(fulfillmentapp.Options{OrderStore: osStore, FulfillmentStore: fulfillmentstore.New(d.DB)})
	d.Bridge = &options
	d.queue = &demoQueue{due: map[uint]time.Time{}}
	d.Orders = orderapp.NewOrderService(orderapp.OrderServiceOptions{OrderStore: osStore, UserStore: userstore.New(d.DB), ProductStore: productstore.NewProductStore(d.DB), ProductSKUStore: productstore.NewSKUStore(d.DB), CouponStore: couponstore.New(d.DB), CouponUsageStore: couponstore.NewUsageStore(d.DB), PromotionRepo: promotionstore.New(d.DB), SettingService: settingsapp.NewService(settingsstore.New(d.DB)), Queue: d.queue, ExpireMinutes: 15})
	return d, nil
}
func (d *Demo) Close() error {
	d.closeOnce.Do(func() {
		if d.DB != nil {
			sql, err := d.DB.DB()
			if err != nil {
				d.closeErr = err
			} else {
				d.closeErr = sql.Close()
			}
		}
		if d.closeErr == nil && d.Root != "" {
			d.closeErr = os.RemoveAll(d.Root)
		}
	})
	return d.closeErr
}
func randomDemoID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func (d *Demo) NewBuyer(ctx context.Context) (uint, error) {
	if err := d.reserveBudget(ctx, "buyers"); err != nil {
		return 0, err
	}
	nonce, err := randomDemoID()
	if err != nil {
		return 0, err
	}
	u := userdomain.User{Email: nonce + "@demo.invalid", PasswordHash: "!demo-login-disabled", DisplayName: "Isolated demo buyer", Status: "active"}
	if err := d.DB.WithContext(ctx).Create(&u).Error; err != nil {
		return 0, err
	}
	return u.ID, nil
}
func (d *Demo) Catalog(ctx context.Context) ([]CatalogProduct, error) {
	var p productdomain.Product
	if err := d.DB.WithContext(ctx).Where("id = ? AND is_active = ?", d.ProductID, true).First(&p).Error; err != nil {
		return nil, err
	}
	return []CatalogProduct{{ID: p.ID, Title: "TapeCommerce Launch Kit", AmountUSDT: p.PriceAmount.String(), Currency: "USDT", FulfillmentType: constants.FulfillmentTypeAuto}}, nil
}
func (d *Demo) CreateOrder(ctx context.Context, buyer, product uint, payer string) (PreparedQuote, error) {
	if product != d.ProductID || buyer == 0 || !domain.ValidHex(payer, 20) || domain.IsZeroHex(payer) {
		return PreparedQuote{}, domain.ErrInvalid
	}
	u, err := userstore.New(d.DB).GetByID(buyer)
	if err != nil {
		return PreparedQuote{}, err
	}
	if u == nil || u.Status != "active" {
		return PreparedQuote{}, domain.ErrNotFound
	}
	if err := d.Sweep(); err != nil {
		return PreparedQuote{}, err
	}
	if err := d.reserveBudget(ctx, "orders"); err != nil {
		return PreparedQuote{}, err
	}
	o, err := d.Orders.CreateOrder(orderapp.CreateOrderInput{UserID: buyer, Items: []orderapp.CreateOrderItem{{ProductID: product, Quantity: 1}}, SkipRiskControl: true})
	if err != nil {
		return PreparedQuote{}, err
	}
	return d.Bridge.Prepare(ctx, buyer, o.ID, payer)
}

// The demo timeout queue is process-local and drained before each HTTP request;
// there is no external Redis connection or hidden worker. Order expiry is also
// enforced at quote/admission. Restart removes the whole ephemeral demo DB.
type demoQueue struct {
	mu  sync.Mutex
	due map[uint]time.Time
}

func (q *demoQueue) Enabled() bool { return true }
func (q *demoQueue) EnqueueTimeoutCancel(id uint, delay time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.due[id] = time.Now().Add(delay)
	return nil
}
func (q *demoQueue) EnqueueStatusEmail(uint, string) error { return nil }
func (d *Demo) Sweep() error {
	d.queue.mu.Lock()
	var due []uint
	for id, at := range d.queue.due {
		if !at.After(time.Now()) {
			due = append(due, id)
			delete(d.queue.due, id)
		}
	}
	d.queue.mu.Unlock()
	for _, id := range due {
		_, err := d.Orders.CancelExpiredOrder(id)
		if err != nil && !errors.Is(err, orderapp.ErrOrderStatusInvalid) {
			return err
		}
	}
	return nil
}

// Hard lifetime budget protects the demo DB even after Node session eviction.
// Failed creation attempts consume a slot too. SQL CAS is atomic; restarting
// discards only the command-owned temporary DB and resets this demo budget.
type demoBudget struct {
	Key   string `gorm:"primaryKey"`
	Count uint
}

func (demoBudget) TableName() string { return "tapecommerce_demo_budgets" }
func (d *Demo) reserveBudget(ctx context.Context, key string) error {
	res := d.DB.WithContext(ctx).Model(&demoBudget{}).Where("key = ? AND count < ?", key, 1000).UpdateColumn("count", gorm.Expr("count + 1"))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrConflict
	}
	return nil
}
