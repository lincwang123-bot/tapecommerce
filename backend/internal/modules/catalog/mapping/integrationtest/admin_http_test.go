package integrationtest

import (
	context "context"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	constants "github.com/dujiao-next/internal/constants"
	factorysupply "github.com/dujiao-next/internal/factorysupply"
	categoryapp "github.com/dujiao-next/internal/modules/catalog/category/application"
	categorydomain "github.com/dujiao-next/internal/modules/catalog/category/domain"
	categorygormstore "github.com/dujiao-next/internal/modules/catalog/category/infrastructure/gormstore"
	mappingapp "github.com/dujiao-next/internal/modules/catalog/mapping/application"
	mappingcontract "github.com/dujiao-next/internal/modules/catalog/mapping/contract"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	mappinggormstore "github.com/dujiao-next/internal/modules/catalog/mapping/infrastructure/gormstore"
	mappinghttp "github.com/dujiao-next/internal/modules/catalog/mapping/transport/http"
	productdomain "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productgormstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
	settingsintegration "github.com/dujiao-next/internal/modules/settings/schema/integration"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	jsonmap "github.com/dujiao-next/internal/shared/jsonmap"
	money "github.com/dujiao-next/internal/shared/money"
	upstream "github.com/dujiao-next/internal/upstream"
	gin "github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	decimal "github.com/shopspring/decimal"
	gorm "gorm.io/gorm"
	http "net/http"
	httptest "net/http/httptest"
	os "os"
	filepath "path/filepath"
	strings "strings"
	testing "testing"
	time "time"
)

type adminNoopLocalMediaRecorder struct{}

func (adminNoopLocalMediaRecorder) RecordLocalFile(context.Context, string, string) {}

type mappingConnectionProvider struct {
	db      *gorm.DB
	adapter upstream.Adapter
}

func (p *mappingConnectionProvider) GetByID(id uint) (*siteconnectiondomain.Connection, error) {
	var conn siteconnectiondomain.Connection
	if err := p.db.First(&conn, id).Error; err != nil {
		return nil, err
	}
	return &conn, nil
}

func (p *mappingConnectionProvider) GetAdapter(conn *siteconnectiondomain.Connection) (upstream.Adapter, error) {
	return p.adapter, nil
}

type mappingImportUoW struct {
	products    *productgormstore.ProductStore
	skus        *productgormstore.SKUStore
	mappings    *mappinggormstore.MappingStore
	skuMappings *mappinggormstore.SKUMappingStore
}

func (unit *mappingImportUoW) WithinTransaction(fn func(mappingcontract.ImportRepositories) error) error {
	if fn == nil {
		return nil
	}
	return unit.products.Transaction(func(tx *gorm.DB) error {
		return fn(mappingcontract.ImportRepositories{
			Products:    unit.products.BindTx(tx),
			SKUs:        unit.skus.BindTx(tx),
			Mappings:    unit.mappings.WithTx(tx),
			SKUMappings: unit.skuMappings.WithTx(tx),
		})
	})
}

func setupAdminHandlerTest(t *testing.T, upstreamHandler http.HandlerFunc) (*mappinghttp.AdminHandler, *gorm.DB, uint, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := fmt.Sprintf("file:admin_product_mapping_handler_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	if err := db.AutoMigrate(
		&categorydomain.Category{},
		&productdomain.Product{},
		&productdomain.ProductSKU{},
		&siteconnectiondomain.Connection{},
		&mappingdomain.Mapping{},
		&mappingdomain.SKUMapping{},
	); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}

	server := httptest.NewServer(upstreamHandler)
	categoryRepo := categorygormstore.NewCategoryStore(db)
	categoryService := categoryapp.NewService(categoryRepo)

	conn := siteconnectiondomain.Connection{
		Name:      "upstream",
		BaseURL:   server.URL,
		ApiKey:    "test-key",
		ApiSecret: "test-secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
		Status:    constants.ConnectionStatusActive,
	}
	if err := db.Create(&conn).Error; err != nil {
		t.Fatalf("create connection failed: %v", err)
	}

	adapter, err := upstream.NewAdapter(&conn, t.TempDir())
	if err != nil {
		t.Fatalf("create upstream adapter failed: %v", err)
	}

	productStore := productgormstore.NewProductStore(db)
	skuStore := productgormstore.NewSKUStore(db)
	mappingStore := mappinggormstore.NewMappingStore(db)
	skuMappingStore := mappinggormstore.NewSKUMappingStore(db)

	mappingService, err := mappingapp.NewService(mappingapp.Options{
		Mappings:    mappingStore,
		SKUMappings: skuMappingStore,
		Products:    productStore,
		SKUs:        skuStore,
		Categories:  categoryRepo,
		Connections: &mappingConnectionProvider{db: db, adapter: adapter},
		Media:       adminNoopLocalMediaRecorder{},
		Transactions: &mappingImportUoW{
			products:    productStore,
			skus:        skuStore,
			mappings:    mappingStore,
			skuMappings: skuMappingStore,
		},
	})
	if err != nil {
		t.Fatalf("create product mapping service: %v", err)
	}
	mappingService.SetCategoryCreator(categoryService)

	h := mappinghttp.NewAdminHandler(mappingService)
	return h, db, conn.ID, server.Close
}

func TestBatchImportUpstreamProductsAutoCreatesCategory(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/upstream/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"categories": []upstream.UpstreamCategory{
					{ID: 9, Slug: "upstream-streaming", Name: jsonmap.JSON{"zh-CN": "流媒体"}},
				},
			})
		case "/api/v1/upstream/products/101":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"product": upstream.UpstreamProduct{
					ID:              101,
					CategoryID:      9,
					Title:           jsonmap.JSON{"zh-CN": "上游商品"},
					Description:     jsonmap.JSON{"zh-CN": "描述"},
					Content:         jsonmap.JSON{"zh-CN": "内容"},
					Images:          []string{},
					Tags:            []string{},
					PriceAmount:     "10.00",
					Currency:        "CNY",
					FulfillmentType: constants.FulfillmentTypeAuto,
					IsActive:        true,
					SKUs: []upstream.UpstreamSKU{
						{ID: 201, SKUCode: "SKU-A", SpecValues: jsonmap.JSON{"name": "A"}, PriceAmount: "10.00", IsActive: true},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true}`, connID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchImportUpstreamProducts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var imported productdomain.Product
	if err := db.First(&imported).Error; err != nil {
		t.Fatalf("load imported product failed: %v", err)
	}
	if imported.CategoryID == 0 {
		t.Fatalf("expected imported product to be assigned to auto-created category")
	}

	var category categorydomain.Category
	if err := db.First(&category, imported.CategoryID).Error; err != nil {
		t.Fatalf("load auto-created category failed: %v", err)
	}
	if category.Slug != "upstream-streaming" {
		t.Fatalf("expected auto-created category slug upstream-streaming, got %q", category.Slug)
	}
}

func TestBatchImportUpstreamProductsRestoresSoftDeletedAutoCategory(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/upstream/categories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"categories": []upstream.UpstreamCategory{
					{ID: 9, Slug: "upstream-streaming", Name: jsonmap.JSON{"zh-CN": "流媒体"}},
				},
			})
		case "/api/v1/upstream/products/101":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"product": upstream.UpstreamProduct{
					ID:              101,
					CategoryID:      9,
					Title:           jsonmap.JSON{"zh-CN": "上游商品"},
					PriceAmount:     "10.00",
					Currency:        "CNY",
					FulfillmentType: constants.FulfillmentTypeAuto,
					IsActive:        true,
					SKUs: []upstream.UpstreamSKU{
						{ID: 201, SKUCode: "SKU-A", SpecValues: jsonmap.JSON{"name": "A"}, PriceAmount: "10.00", IsActive: true},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	defer cleanup()

	deletedCategory := categorydomain.Category{
		Slug:     "upstream-streaming",
		NameJSON: jsonmap.JSON{"zh-CN": "已删除分类"},
		IsActive: true,
	}
	if err := db.Create(&deletedCategory).Error; err != nil {
		t.Fatalf("create soft-delete target category failed: %v", err)
	}
	if err := categorygormstore.NewCategoryStore(db).Delete(fmt.Sprintf("%d", deletedCategory.ID)); err != nil {
		t.Fatalf("soft delete category failed: %v", err)
	}

	body := fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true}`, connID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/product-mappings/batch-import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.BatchImportUpstreamProducts(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}

	var imported productdomain.Product
	if err := db.First(&imported).Error; err != nil {
		t.Fatalf("load imported product failed: %v", err)
	}
	if imported.CategoryID != deletedCategory.ID {
		t.Fatalf("expected imported product category %d, got %d", deletedCategory.ID, imported.CategoryID)
	}

	var restored categorydomain.Category
	if err := db.First(&restored, deletedCategory.ID).Error; err != nil {
		t.Fatalf("expected category to be restored, got %v", err)
	}
	if restored.NameJSON["zh-CN"] != "流媒体" {
		t.Fatalf("expected restored category name to be refreshed, got %+v", restored.NameJSON)
	}
}

func TestBindDraftPreservesPriceAndCannotRepeatOrBindForeignSKU(t *testing.T) {
	h, db, connectionID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/upstream/products/101" {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "product": upstream.UpstreamProduct{ID: 101, IsActive: true, FulfillmentType: constants.FulfillmentTypeAuto, SKUs: []upstream.UpstreamSKU{{ID: 101, IsActive: true, StockQuantity: -1}}}})
	})
	defer cleanup()
	product := productdomain.Product{Slug: "draft-binding", TitleJSON: jsonmap.JSON{"zh-CN": "保留名称"}, FulfillmentType: constants.FulfillmentTypeUpstream, PriceAmount: money.FromDecimal(decimal.NewFromInt(23))}
	if err := db.Create(&product).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&product).Update("is_active", false)
	sku := productdomain.ProductSKU{ProductID: product.ID, SKUCode: "plus", PriceAmount: product.PriceAmount, IsActive: true}
	if err := db.Create(&sku).Error; err != nil {
		t.Fatal(err)
	}
	invoke := func(local uint) int {
		body := fmt.Sprintf(`{"connection_id":%d,"local_product_id":%d,"upstream_product_id":101,"skus":[{"local_sku_id":%d,"upstream_sku_id":101}]}`, connectionID, product.ID, local)
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest("POST", "/admin/product-mappings/bind-draft", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		h.BindDraft(ctx)
		var result struct {
			Code int `json:"status_code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Code
	}
	if invoke(sku.ID+999) == 0 {
		t.Fatal("foreign SKU accepted")
	}
	if code := invoke(sku.ID); code != 0 {
		t.Fatalf("valid draft refused %d", code)
	}
	if invoke(sku.ID) == 0 {
		t.Fatal("repeat binding accepted")
	}
	db.First(&product, product.ID)
	if product.IsActive || !product.IsMapped || product.PriceAmount.StringFixed(2) != "23.00" || product.TitleJSON["zh-CN"] != "保留名称" {
		t.Fatal("draft content, price or state changed")
	}
	var count int64
	db.Model(&mappingdomain.Mapping{}).Count(&count)
	if count != 1 {
		t.Fatalf("mappings=%d", count)
	}
	db.Model(&mappingdomain.SKUMapping{}).Count(&count)
	if count != 1 {
		t.Fatalf("sku mappings=%d", count)
	}
}

type supplyNoStockChecks struct{}

func (supplyNoStockChecks) GetUpstreamSyncConfig(string) (settingsintegration.UpstreamSyncConfig, error) {
	c := settingsintegration.DefaultUpstreamSyncConfig()
	c.PreOrderStockCheckEnabled = false
	return c, nil
}
func (supplyNoStockChecks) GetUpstreamSyncInterval(string) (time.Duration, error) {
	return time.Hour, nil
}

func TestFactorySupplyImportBindingAndDisabledStockCheck(t *testing.T) {
	h, db, connID, cleanup := setupAdminHandlerTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "categories") {
			w.Write([]byte(`{"ok":true,"categories":[{"id":9,"slug":"supply","name":{"zh-CN":"订阅"}}]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"product":{"id":101,"category_id":9,"title":{"zh-CN":"ChatGPT Plus"},"price_amount":"4.00","currency":"USDT","fulfillment_type":"auto","is_active":true,"skus":[{"id":201,"sku_code":"month","price_amount":"4.00","stock_quantity":100,"is_active":true}]}}`))
	})
	defer cleanup()
	file := filepath.Join(t.TempDir(), "policy.json")
	policy := factorysupply.Policy{SchemaVersion: 1, ShopID: "shop_test", MainOrigin: "https://coco9.cc", MainConnectionID: 99, MainProducts: map[string]string{"101": "ChatGPT"}}
	save := func() {
		b, _ := json.Marshal(policy)
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	t.Setenv("FACTORY_SHOP_ID", "shop_test")
	t.Setenv("FACTORY_SUPPLY_POLICY_FILE", file)
	importProduct := func() {
		q := httptest.NewRequest("POST", "/api/v1/admin/product-mappings/batch-import", strings.NewReader(fmt.Sprintf(`{"connection_id":%d,"upstream_product_ids":[101],"auto_create_category":true,"supply_source":"main"}`, connID)))
		q.Header.Set("Content-Type", "application/json")
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = q
		h.BatchImportUpstreamProducts(c)
	}
	importProduct()
	var count int64
	db.Model(&productdomain.Product{}).Count(&count)
	if count != 0 {
		t.Fatal("third-party ChatGPT imported")
	}
	policy.MainConnectionID = connID
	save()
	// The adapter stays a loopback fixture; the connection metadata exercises the real origin policy.
	if err := db.Model(&siteconnectiondomain.Connection{}).Where("id = ?", connID).Update("base_url", "https://coco9.cc").Error; err != nil {
		t.Fatal(err)
	}
	importProduct()
	var product productdomain.Product
	if err := db.First(&product).Error; err != nil {
		t.Fatal(err)
	}
	if product.SupplyBrand != "ChatGPT" || product.SupplySource != "main" || product.FulfillmentType != "upstream" {
		t.Fatalf("wrong import identity: %+v", product)
	}
	var mapping mappingdomain.Mapping
	db.First(&mapping)
	var sku mappingdomain.SKUMapping
	db.First(&sku)
	svc, err := mappingapp.NewService(mappingapp.Options{Mappings: mappinggormstore.NewMappingStore(db), SKUMappings: mappinggormstore.NewSKUMappingStore(db), Products: productgormstore.NewProductStore(db), Connections: &mappingConnectionProvider{db: db}, Media: adminNoopLocalMediaRecorder{}})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetSettings(supplyNoStockChecks{})
	if err := svc.EnsureUpstreamStockForOrder(sku.LocalSKUID, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureUpstreamStockForOrder(sku.LocalSKUID+999, 1); err == nil {
		t.Fatal("invented SKU bypassed supply policy")
	}
	if err := svc.Delete(mapping.ID); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if _, err := svc.BindDraft(mappingapp.BindDraftInput{ConnectionID: connID, LocalProductID: product.ID, UpstreamProductID: 101}); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if err := db.Model(&mapping).Update("connection_id", 99).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureUpstreamStockForOrder(sku.LocalSKUID, 1); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatalf("stock switch bypass: %v", err)
	}
}
