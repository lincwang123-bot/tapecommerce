package producthttp

import (
	bytes "bytes"
	json "encoding/json"
	mapping "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	writeapp "github.com/dujiao-next/internal/modules/catalog/product/application/write"
	product "github.com/dujiao-next/internal/modules/catalog/product/domain"
	money "github.com/dujiao-next/internal/shared/money"
	gin "github.com/gin-gonic/gin"
	decimal "github.com/shopspring/decimal"
	httptest "net/http/httptest"
	testing "testing"
)

type draftWriter struct {
	ProductWriter
	calls int
	last  writeapp.CreateProductInput
}

func (w *draftWriter) Create(input writeapp.CreateProductInput) (*product.Product, error) {
	w.calls++
	w.last = input
	return &product.Product{ID: 1, IsActive: false, FulfillmentType: input.FulfillmentType}, nil
}
func (w *draftWriter) Update(_ string, input writeapp.CreateProductInput) (*product.Product, error) {
	return w.Create(input)
}

type draftCommands struct {
	ProductAdminCommands
	calls int
}

func (d *draftCommands) QuickUpdate(_ string, _ map[string]interface{}) (*product.Product, error) {
	d.calls++
	return &product.Product{ID: 1, IsActive: true}, nil
}
func draftRequest(t *testing.T, h gin.HandlerFunc, method, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	c.Request = httptest.NewRequest(method, "/api/v1/admin/products/1", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	var out struct {
		Code int `json:"status_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Code
}
func TestVoucherDraftHandlersKeepPublishingGuard(t *testing.T) {
	t.Setenv("VOUCHER_SERVICE_URL", "http://127.0.0.1:18411")
	gin.SetMode(gin.TestMode)
	writer := &draftWriter{}
	commands := &draftCommands{}
	h := AdminProductHandler{writer: writer, admin: commands}
	body := `{"category_id":1,"slug":"draft","title":{"zh-CN":"草稿"},"price_amount":20,"fulfillment_type":"upstream","is_active":false,"skus":[{"sku_code":"plan","price_amount":20,"is_active":true}]}`
	if code := draftRequest(t, h.CreateProduct, "POST", body); code != 0 {
		t.Fatal("draft failed", code)
	}
	if writer.calls != 1 || writer.last.IsActive == nil || *writer.last.IsActive || writer.last.FulfillmentType != "upstream" {
		t.Fatal("invalid draft forwarded")
	}
	for _, body := range []string{`{"category_id":1,"slug":"draft","title":{"zh-CN":"x"},"price_amount":20,"fulfillment_type":"upstream","is_active":true}`, `{"category_id":1,"slug":"draft","title":{"zh-CN":"x"},"price_amount":20,"fulfillment_type":"manual","is_active":false}`} {
		if draftRequest(t, h.CreateProduct, "POST", body) == 0 {
			t.Fatal("unsafe create allowed")
		}
	}
	if draftRequest(t, h.QuickUpdateProduct, "PATCH", `{"is_active":true}`) == 0 || commands.calls != 0 {
		t.Fatal("unmapped publication allowed")
	}
	if draftRequest(t, h.UpdateProduct, "PUT", `{"category_id":1,"slug":"draft","title":{"zh-CN":"x"},"price_amount":20,"fulfillment_type":"manual","is_active":true}`) == 0 {
		t.Fatal("PUT bypassed mapping guard")
	}
	if code := draftRequest(t, h.UpdateProduct, "PUT", body); code != 0 {
		t.Fatal("draft update failed", code)
	}
	h.mappings = voucherMappings{}
	h.skuMappings = voucherSKUs{}
	h.products = voucherProducts{p: &product.Product{SKUs: []product.ProductSKU{{ID: 1, IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(20))}}}}
	if code := draftRequest(t, h.QuickUpdateProduct, "PATCH", `{"is_active":true}`); code != 0 || commands.calls != 1 {
		t.Fatal("valid mapped publish failed", code)
	}
}

type voucherProducts struct {
	ProductQueries
	p *product.Product
}

func (q voucherProducts) GetAdminByID(string) (*product.Product, error) { return q.p, nil }

type voucherMappings struct{}

func (voucherMappings) ListByLocalProductIDs([]uint) ([]mapping.Mapping, error) {
	return []mapping.Mapping{{ID: 1, LocalProductID: 1, IsActive: true}}, nil
}

type voucherSKUs struct{}

func (voucherSKUs) ListByProductMapping(uint) ([]mapping.SKUMapping, error) {
	return []mapping.SKUMapping{{LocalSKUID: 1}}, nil
}
func TestVoucherPublishRequiresMappedPositiveSKU(t *testing.T) {
	p := &product.Product{SKUs: []product.ProductSKU{{ID: 1, IsActive: true}}}
	h := AdminProductHandler{products: voucherProducts{p: p}, mappings: voucherMappings{}, skuMappings: voucherSKUs{}}
	if err := h.requireVoucherMapping("1", nil); err == nil {
		t.Fatal("zero-priced imported SKU allowed")
	}
	p.SKUs[0].PriceAmount = money.FromDecimal(decimal.NewFromInt(20))
	if err := h.requireVoucherMapping("1", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.requireVoucherMapping("1", []ProductSKURequest{{ID: 1, PriceAmount: 0}}); err == nil {
		t.Fatal("zero-price update allowed")
	}
	if err := h.requireVoucherMapping("1", []ProductSKURequest{{ID: 2, PriceAmount: 20}}); err == nil {
		t.Fatal("unmapped new SKU allowed")
	}
}

func TestStockDraftAndIndependentPublish(t *testing.T) {
	t.Setenv("VOUCHER_SERVICE_URL", "http://127.0.0.1:18411")
	writer := &draftWriter{}
	commands := &draftCommands{}
	p := &product.Product{FulfillmentType: "auto", SKUs: []product.ProductSKU{{ID: 1, IsActive: true, PriceAmount: money.FromDecimal(decimal.NewFromInt(20))}}}
	h := AdminProductHandler{writer: writer, admin: commands, products: voucherProducts{p: p}}
	if code := draftRequest(t, h.CreateProduct, "POST", `{"category_id":1,"slug":"stock","title":{"zh-CN":"库存"},"price_amount":20,"fulfillment_type":"auto","is_active":false}`); code != 0 {
		t.Fatalf("stock draft rejected %d", code)
	}
	if code := draftRequest(t, h.QuickUpdateProduct, "PATCH", `{"is_active":true}`); code != 0 {
		t.Fatalf("stock independent publish rejected %d", code)
	}
	p.SKUs[0].PriceAmount = money.FromDecimal(decimal.Zero)
	if code := draftRequest(t, h.QuickUpdateProduct, "PATCH", `{"is_active":true}`); code == 0 {
		t.Fatal("zero-priced stock published")
	}
}

func TestPublicDeliveryModePreservesOriginalType(t *testing.T) {
	h := &PublicHandler{mappings: publicVoucherMapping{}, skuMappings: voucherSKUs{}}
	for kind, want := range map[string]string{"auto": "stock", "upstream": "voucher", "manual": "manual"} {
		p := &product.Product{FulfillmentType: kind}
		v := publicProductView{Product: *p}
		h.decorateProductStock(p, &v)
		if got := v.toProductResp().DeliveryMode; got != want {
			t.Fatalf("%s delivery mode=%s want %s", kind, got, want)
		}
	}
}

type publicVoucherMapping struct{}

func (publicVoucherMapping) GetByLocalProductID(uint) (*mapping.Mapping, error) {
	return &mapping.Mapping{ID: 1, UpstreamFulfillmentType: "auto"}, nil
}
