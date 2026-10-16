package integrationtest

import (
	"fmt"
	"github.com/dujiao-next/internal/shared/jsonmap"
	"strings"
	"sync"
	"testing"

	app "github.com/dujiao-next/internal/modules/cardsecret/application"
	domain "github.com/dujiao-next/internal/modules/cardsecret/domain"
	store "github.com/dujiao-next/internal/modules/cardsecret/infrastructure/gormstore"
	product "github.com/dujiao-next/internal/modules/catalog/product/domain"
	productstore "github.com/dujiao-next/internal/modules/catalog/product/store/gormstore"
)

func TestStockImportSafety(t *testing.T) {
	t.Setenv("VOUCHER_DELIVERY_KEY", "stock-import-test-key")
	db := setupCardSecretServiceTestDB(t)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	p := product.Product{Slug: "stock-safe", TitleJSON: jsonmap.JSON{"zh-CN": "stock"}, FulfillmentType: "auto"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	sku := product.ProductSKU{ProductID: p.ID, SKUCode: "one", IsActive: true}
	if err := db.Create(&sku).Error; err != nil {
		t.Fatal(err)
	}
	foreign := product.ProductSKU{ProductID: p.ID + 1, SKUCode: "foreign", IsActive: true}
	db.Create(&foreign)
	svc := NewCardSecretService(store.New(db), store.NewBatch(db), productstore.NewProductStore(db), productstore.NewSKUStore(db))
	input := app.CreateCardSecretBatchInput{ProductID: p.ID, SKUID: sku.ID, Secrets: []string{"\ufeffaccount: demo\r\npassword: sample", "account: demo\npassword: sample"}, PreserveMultiline: true, BatchNo: "safe"}
	_, n, err := svc.CreateCardSecretBatch(input)
	if err != nil || n != 1 {
		t.Fatalf("multiline import: count=%d err=%v", n, err)
	}
	var raw string
	db.Table("card_secrets").Select("secret").Limit(1).Scan(&raw)
	if !strings.HasPrefix(raw, "dv1:") {
		t.Fatal("inventory was not encrypted")
	}
	var rows []domain.Secret
	db.Find(&rows)
	if len(rows) != 1 || rows[0].Secret != "account: demo\npassword: sample" {
		t.Fatal("multiline roundtrip failed")
	}
	if matches, total, err := svc.ListCardSecrets(app.ListCardSecretInput{ProductID: p.ID, Secret: "password", PageSize: 10}); err != nil || total != 1 || len(matches) != 1 {
		t.Fatalf("encrypted inventory search failed: total=%d err=%v", total, err)
	}
	originalSecrets := input.Secrets
	input.Secrets = []string{"new-content-same-batch"}
	if _, _, err := svc.CreateCardSecretBatch(input); err == nil {
		t.Fatal("reused batch number accepted")
	}
	input.Secrets = originalSecrets
	db.Model(&sku).Update("is_active", false)
	if _, _, err := svc.CreateCardSecretBatch(input); err != app.ErrProductSKUInvalid {
		t.Fatal("inactive SKU accepted")
	}
	for _, readSKU := range []uint{sku.ID, 0} {
		if stats, err := svc.GetStats(p.ID, readSKU); err != nil || stats.Total != 1 {
			t.Fatalf("disabled SKU stats unavailable: %v", err)
		}
		if batches, total, err := svc.ListBatches(p.ID, readSKU, 1, 10); err != nil || total != 1 || len(batches) != 1 {
			t.Fatalf("disabled SKU batches unavailable: %v", err)
		}
		if items, total, err := svc.ListCardSecrets(app.ListCardSecretInput{ProductID: p.ID, SKUID: readSKU, PageSize: 10}); err != nil || total != 1 || len(items) != 1 {
			t.Fatalf("disabled SKU inventory unavailable: %v", err)
		}
	}
	if _, err := svc.GetStats(p.ID, foreign.ID); err != app.ErrProductSKUInvalid {
		t.Fatalf("foreign SKU stats accepted: %v", err)
	}
	if _, _, err := svc.ListBatches(p.ID, foreign.ID, 1, 10); err != app.ErrProductSKUInvalid {
		t.Fatalf("foreign SKU batches accepted: %v", err)
	}
	if _, _, err := svc.ListCardSecrets(app.ListCardSecretInput{ProductID: p.ID, SKUID: foreign.ID}); err != app.ErrProductSKUInvalid {
		t.Fatalf("foreign SKU inventory accepted: %v", err)
	}
	db.Model(&sku).Update("is_active", true)
	input.SKUID = foreign.ID
	input.BatchNo = "foreign"
	if _, _, err := svc.CreateCardSecretBatch(input); err != app.ErrProductSKUInvalid {
		t.Fatalf("foreign SKU accepted: %v", err)
	}
	input.SKUID = sku.ID
	for _, state := range []string{"reserved", "used", "available"} {
		db.Model(&domain.Secret{}).Where("id = ?", rows[0].ID).Update("status", state)
		input.BatchNo = "duplicate-" + state
		if _, _, err := svc.CreateCardSecretBatch(input); err == nil {
			t.Fatalf("duplicate accepted in %s", state)
		}
	}
	if n, err := svc.BatchDeleteCardSecrets([]uint{rows[0].ID}, 0, app.ListCardSecretInput{}); err != nil || n != 1 {
		t.Fatal("available deletion failed")
	}
	input.BatchNo = "duplicate-deleted"
	if _, _, err := svc.CreateCardSecretBatch(input); err == nil {
		t.Fatal("deleted secret reimported")
	}
	db.Model(&domain.Secret{}).Where("id = ?", rows[0].ID).Update("deleted_at", nil)
	db.Model(&domain.Secret{}).Where("id = ?", rows[0].ID).Update("status", "used")
	if n, err := svc.BatchUpdateCardSecretStatus([]uint{rows[0].ID}, 0, app.ListCardSecretInput{}, "available"); err != nil || n != 0 {
		t.Fatal("sold inventory returned to stock")
	}
	if _, err := svc.UpdateCardSecret(rows[0].ID, "", "available"); err == nil {
		t.Fatal("sold inventory edited")
	}
	if n, err := svc.BatchDeleteCardSecrets([]uint{rows[0].ID}, 0, app.ListCardSecretInput{}); err != nil || n != 0 {
		t.Fatal("sold inventory deleted")
	}
	var wg sync.WaitGroup
	successes := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := svc.CreateCardSecretBatch(app.CreateCardSecretBatchInput{ProductID: p.ID, SKUID: sku.ID, BatchNo: fmt.Sprintf("parallel-%d", i), Secrets: []string{"concurrent-once"}})
			successes <- err == nil
		}(i)
	}
	wg.Wait()
	close(successes)
	n = 0
	for success := range successes {
		if success {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one concurrent import, got %d", n)
	}
	input.Secrets = []string{strings.Repeat("x", 16385)}
	input.BatchNo = "oversize"
	if _, _, err := svc.CreateCardSecretBatch(input); err != app.ErrInvalid {
		t.Fatal("oversized secret accepted")
	}
	db.Model(&p).Update("fulfillment_type", "upstream")
	input.Secrets = []string{"wrong-mode"}
	if _, _, err := svc.CreateCardSecretBatch(input); err != app.ErrInvalid {
		t.Fatal("upstream import accepted")
	}
}
