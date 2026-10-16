package factoryquota

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestQuotaAdmissionSerializesLastSlotAndPreservesExistingWork(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(root+"/commerce", 0700)
	os.Mkdir(root+"/uploads", 0700)
	file := filepath.Join(root, "quota.json")
	t.Setenv("FACTORY_QUOTA_FILE", file)
	write := func(expires int64) {
		t.Helper()
		if e := os.WriteFile(file, []byte(fmt.Sprintf(`{"products":1,"orders":1,"bytes":100000000,"expires":%d}`, expires)), 0600); e != nil {
			t.Fatal(e)
		}
	}
	db, e := gorm.Open(sqlite.Open(root+"/commerce/store.db"), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := db.DB()
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	db.Exec("CREATE TABLE products (id INTEGER PRIMARY KEY, deleted_at DATETIME)")
	db.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, parent_id INTEGER, paid_at DATETIME, status TEXT)")
	write(time.Now().Add(time.Hour).UnixMilli())
	r := gin.New()
	r.Use(Gate(db))
	r.POST("/api/v1/admin/products", func(c *gin.Context) {
		if e := db.Exec("INSERT INTO products DEFAULT VALUES").Error; e != nil {
			c.Status(500)
		} else {
			c.Status(201)
		}
	})
	r.POST("/api/v1/orders/create-and-pay", func(c *gin.Context) { db.Exec("INSERT INTO orders (status) VALUES ('pending_payment')"); c.Status(201) })
	for _, route := range []string{"/api/v1/payments/1/capture", "/api/v1/admin/orders/1/refund-to-wallet", "/api/v1/payments/webhook/nowpayments"} {
		r.POST(route, func(c *gin.Context) { c.Status(204) })
	}
	request := func(p string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", p, nil))
		return w.Code
	}
	for _, route := range []string{"/api/v1/admin/products", "/api/v1/orders/create-and-pay"} {
		var wg sync.WaitGroup
		results := make(chan int, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); results <- request(route) }()
		}
		wg.Wait()
		close(results)
		counts := map[int]int{}
		for code := range results {
			counts[code]++
		}
		if counts[201] != 1 || counts[503] != 1 {
			t.Fatal(route, counts)
		}
	}
	write(1)
	if request("/api/v1/orders/create-and-pay") != 503 {
		t.Fatal("expiry must block new orders")
	}
	for _, p := range []string{"/api/v1/payments/1/capture", "/api/v1/admin/orders/1/refund-to-wallet", "/api/v1/payments/webhook/nowpayments"} {
		if request(p) != 204 {
			t.Fatal("existing work blocked", p)
		}
	}
	os.WriteFile(file, []byte(`broken`), 0600)
	if request("/api/v1/admin/products") != 503 {
		t.Fatal("invalid policy must fail closed")
	}
}
