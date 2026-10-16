package integrationtest

import (
	"context"
	"encoding/json"
	"github.com/dujiao-next/internal/authz"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dujiao-next/internal/app/httpserver/middleware"
	admincontract "github.com/dujiao-next/internal/modules/identity/admin/contract"
	admindomain "github.com/dujiao-next/internal/modules/identity/admin/domain"
	adminauthapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
	suppliercontract "github.com/dujiao-next/internal/modules/suppliermanagement/contract"
	supplierhttp "github.com/dujiao-next/internal/modules/suppliermanagement/transport/http"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type adminStore struct{ admincontract.Store }

func (adminStore) GetByID(id uint) (*admindomain.Admin, error) {
	return &admindomain.Admin{ID: id, IsSuper: id == 2}, nil
}

type proxy struct{ calls int }

func (p *proxy) Forward(_ context.Context, _, _ string, _ []byte) (json.RawMessage, *suppliercontract.Failure) {
	p.calls++
	return json.RawMessage(`{"items":[]}`), nil
}
func TestActualJWTAndSuperAdminRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "authz.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	acl, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	// Even an ordinary administrator granted every RBAC permission remains denied.
	_, err = acl.Enforcer().AddPolicy("admin:1", "/admin/*", "*")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{}
	r := gin.New()
	supplierhttp.RegisterAdminRoutes(r.Group("/api/v1/admin", middleware.JWTAuthMiddleware("synthetic-secret", adminStore{}), middleware.AdminRBACMiddleware(acl)), p)
	token := func(id uint) string {
		claims := adminauthapp.JWTClaims{AdminID: id, Typ: "access", RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
		s, e := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("synthetic-secret"))
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	for _, route := range suppliercontract.Routes {
		path := strings.ReplaceAll(route.Path, ":id", "1")
		for _, role := range []uint{0, 1, 2} {
			before := p.calls
			req := httptest.NewRequest(route.Method, "/api/v1/admin"+path, strings.NewReader(`{"request_id":"synthetic"}`))
			if role != 0 {
				req.Header.Set("Authorization", "Bearer "+token(role))
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if role == 2 {
				if w.Code != 200 || p.calls != before+1 {
					t.Fatalf("%s super: %d %s", path, w.Code, w.Body.String())
				}
			} else {
				if p.calls != before {
					t.Fatal("unauthorized proxy call")
				}
				var result map[string]any
				_ = json.Unmarshal(w.Body.Bytes(), &result)
				if result["status_code"] != float64(401) && result["status_code"] != float64(403) {
					t.Fatalf("role %d: %s", role, w.Body.String())
				}
			}
		}
	}
	for _, path := range []string{"/suppliers?url=http://evil", "/suppliers/0/check", "/suppliers/abc/check"} {
		method := "POST"
		if strings.Contains(path, "?") {
			method = "GET"
		}
		req := httptest.NewRequest(method, "/api/v1/admin"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token(2))
		w := httptest.NewRecorder()
		before := p.calls
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || p.calls != before {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
