package procurement_test

import (
	context "context"
	"encoding/json"
	errors "errors"
	factorysupply "github.com/dujiao-next/internal/factorysupply"
	mappingstore "github.com/dujiao-next/internal/modules/catalog/mapping/infrastructure/gormstore"
	orderstore "github.com/dujiao-next/internal/modules/order/infrastructure/gormstore"
	procurementapp "github.com/dujiao-next/internal/modules/procurement/application"
	contract "github.com/dujiao-next/internal/modules/procurement/contract"
	procstore "github.com/dujiao-next/internal/modules/procurement/infrastructure/gormstore"
	mappingreader "github.com/dujiao-next/internal/modules/procurement/infrastructure/mappingreader"
	orderreader "github.com/dujiao-next/internal/modules/procurement/infrastructure/orderreader"
	"net/http"
	"net/http/httptest"
	os "os"
	filepath "path/filepath"
	"testing"

	orderdomain "github.com/dujiao-next/internal/modules/order/domain"

	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"

	"github.com/dujiao-next/internal/constants"
	siteconnectionapp "github.com/dujiao-next/internal/modules/siteconnection/application"
)

// ── SubmitToUpstream tests ──

func TestSubmitToUpstream_Success(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-SUBMIT-001", constants.OrderStatusPaid, constants.FulfillmentTypeUpstream)
	// 创建 product mapping 和 sku mapping
	pm := &mappingdomain.Mapping{
		ConnectionID:      1,
		LocalProductID:    1,
		UpstreamProductID: 101,
		IsActive:          true,
	}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{
		ProductMappingID: pm.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	}
	db.Create(sm)

	// mock upstream server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":       true,
			"order_id": 999,
			"order_no": "UP-999",
			"status":   "accepted",
			"amount":   "50.00",
			"currency": "CNY",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name:      "test-upstream",
		BaseURL:   server.URL,
		ApiKey:    "key",
		ApiSecret: "secret",
		Protocol:  constants.ConnectionProtocolDujiaoNext,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")

	svc := newTestProcurementService(db, connSvc)

	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("SubmitToUpstream: %v", err)
	}

	// 验证采购单状态 = accepted
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "accepted" {
		t.Errorf("expected procurement status 'accepted', got %q", updatedProc.Status)
	}
	if updatedProc.UpstreamOrderID != 999 {
		t.Errorf("expected upstream_order_id=999, got %d", updatedProc.UpstreamOrderID)
	}

	// 验证本地订单状态 = fulfilling
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusFulfilling {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusFulfilling, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_NonRetryableError_Rejects(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-NONRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "product_out_of_stock",
			"error_message": "product out of stock",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// 不可重试错误应返回 error
	_ = svc.SubmitToUpstream(proc.ID)

	// 验证采购单状态 = rejected
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单状态回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q after rejection, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

func TestSubmitToUpstream_RetryableError_Retries(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-RETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	pm := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(pm)
	sm := &mappingdomain.SKUMapping{ProductMappingID: pm.ID, LocalSKUID: 1, UpstreamSKUID: 201, UpstreamIsActive: true}
	db.Create(sm)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "temporary failure",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, _ := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 3,
	})

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "pending")
	svc := newTestProcurementService(db, connSvc)

	// 可重试错误不应返回 error（已入队重试）
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatalf("expected no error for retryable failure, got: %v", err)
	}

	// 验证采购单状态 = failed（而非 rejected）
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "failed" {
		t.Errorf("expected procurement status 'failed', got %q", updatedProc.Status)
	}
	if updatedProc.RetryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", updatedProc.RetryCount)
	}
}

func TestHandleSubmitFailure_MaxRetriesExhausted(t *testing.T) {
	db := setupProcurementTestDB(t)

	order := createProcTestOrder(t, db, "PROC-MAXRETRY-001", constants.OrderStatusFulfilling, constants.FulfillmentTypeUpstream)
	productMapping := &mappingdomain.Mapping{ConnectionID: 1, LocalProductID: 1, UpstreamProductID: 101, IsActive: true}
	db.Create(productMapping)
	db.Create(&mappingdomain.SKUMapping{
		ProductMappingID: productMapping.ID,
		LocalSKUID:       1,
		UpstreamSKUID:    201,
		UpstreamIsActive: true,
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            false,
			"error_code":    "server_error",
			"error_message": "timeout after retries",
		})
	}))
	defer server.Close()

	connSvc := newTestSiteConnectionService(db, "test-key", t.TempDir())
	conn, err := connSvc.Create(siteconnectionapp.CreateInput{
		Name: "test-upstream", BaseURL: server.URL,
		ApiKey: "key", ApiSecret: "secret", Protocol: constants.ConnectionProtocolDujiaoNext,
		RetryMax: 2, RetryIntervals: "[30,60]",
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}

	proc := createTestProcurementOrder(t, db, conn.ID, order.ID, order.OrderNo, "failed")
	// 设置 retry_count 已达上限
	db.Model(proc).Update("retry_count", 2)

	svc := newTestProcurementService(db, connSvc)

	// 通过公开提交入口验证：可重试错误在次数耗尽后仍必须转为 rejected。
	_ = svc.SubmitToUpstream(proc.ID)

	// 验证采购单状态 = rejected
	var updatedProc ProcurementOrder
	db.First(&updatedProc, proc.ID)
	if updatedProc.Status != "rejected" {
		t.Errorf("expected procurement status 'rejected', got %q", updatedProc.Status)
	}

	// 验证本地订单回退到 paid
	var updatedOrder orderdomain.Order
	db.First(&updatedOrder, order.ID)
	if updatedOrder.Status != constants.OrderStatusPaid {
		t.Errorf("expected order status %q, got %q", constants.OrderStatusPaid, updatedOrder.Status)
	}
}

type supplyRecorder struct {
	opens    int
	requests []contract.CreateOrderRequest
}

func (s *supplyRecorder) Open(uint) (contract.UpstreamConnection, error) { s.opens++; return s, nil }
func (*supplyRecorder) CallbackURL() string                              { return "" }
func (*supplyRecorder) RetryMax() int                                    { return 1 }
func (*supplyRecorder) RetryIntervals() string                           { return "[1]" }
func (s *supplyRecorder) CreateOrder(_ context.Context, r contract.CreateOrderRequest) (*contract.CreateOrderResult, error) {
	s.requests = append(s.requests, r)
	return &contract.CreateOrderResult{OK: true, OrderID: 99, OrderNo: "SYNTHETIC-MAIN-99", Amount: "8.00", Currency: "USDT"}, nil
}
func (*supplyRecorder) GetOrder(context.Context, uint) (*contract.UpstreamOrder, error) {
	return nil, errors.New("unused")
}
func (*supplyRecorder) CancelOrder(context.Context, uint) error { return errors.New("unused") }

func TestFactorySupplyWorkerRejectsRerouteAndUsesFrozenCostCap(t *testing.T) {
	db := setupProcurementTestDB(t)
	order := createProcTestOrder(t, db, "FACTORY-SUPPLY-001", "paid", "upstream")
	if err := db.Model(&orderdomain.OrderItem{}).Where("order_id = ?", order.ID).Updates(map[string]any{"supply_brand": "ChatGPT", "supply_source": "main", "cost_price": "4.00", "quantity": 2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(order).Update("currency", "USDT").Error; err != nil {
		t.Fatal(err)
	}
	mapping := mappingdomain.Mapping{ConnectionID: 8, LocalProductID: 1, UpstreamProductID: 11, IsActive: true}
	if err := db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mappingdomain.SKUMapping{ProductMappingID: mapping.ID, LocalSKUID: 1, UpstreamSKUID: 21, UpstreamIsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(file, []byte(`{"schema_version":1,"shop_id":"shop_test","main_origin":"https://coco9.cc","main_connection_id":7,"main_products":{"11":"ChatGPT"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_SHOP_ID", "shop_test")
	t.Setenv("FACTORY_SUPPLY_POLICY_FILE", file)
	recorder := &supplyRecorder{}
	svc := procurementapp.NewService(procurementapp.Options{Repository: procstore.New(db), Orders: orderreader.New(orderstore.New(db, "test-guest-credential-secret-with-32-bytes")), ProductMappings: mappingreader.NewProducts(mappingstore.NewMappingStore(db)), SKUMappings: mappingreader.NewSKUs(mappingstore.NewSKUMappingStore(db)), Connections: recorder})
	if err := svc.CreateForOrder(order.ID); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	proc := createTestProcurementOrder(t, db, 8, order.ID, order.OrderNo, "pending")
	if err := svc.SubmitToUpstream(proc.ID); !errors.Is(err, factorysupply.ErrManagedSupply) {
		t.Fatal(err)
	}
	if recorder.opens != 0 || len(recorder.requests) != 0 {
		t.Fatal("contacted unauthorized supplier")
	}
	if err := db.Model(proc).Update("connection_id", 7).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.SubmitToUpstream(proc.ID); err != nil {
		t.Fatal(err)
	}
	if len(recorder.requests) != 1 {
		t.Fatal("main supplier not called exactly once")
	}
	r := recorder.requests[0]
	if r.MaxTotal != "8.00" || r.DownstreamOrderNo != order.OrderNo || r.SKUID != 21 || r.Quantity != 2 {
		t.Fatalf("wrong frozen request: %+v", r)
	}
	if err := svc.SubmitToUpstream(proc.ID); err == nil {
		t.Fatal("accepted procurement resubmitted")
	}
	if len(recorder.requests) != 1 {
		t.Fatal("duplicate procurement request")
	}
}
