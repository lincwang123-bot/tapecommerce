package nodeclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func TestGoldenSignature(t *testing.T) {
	body := []byte("{ \"name\": \"小九测试\", \"request_id\": \"demo-admin-request-0001\" }\n")
	got := signature("synthetic-admin-secret-only-32-bytes", "POST", "/internal/admin/suppliers", "1730000000", "12345678-1234-4123-8123-123456789abc", body)
	if got != "3e9d424d5d71d3f67896de09d8d130d839f5733d5d2d57747bb8388c2ebcf900" {
		t.Fatal(got)
	}
}
func TestSignedFixedTargetAndFreshNonce(t *testing.T) {
	c := New("http://127.0.0.1:4311", "synthetic-key", strings.Repeat("s", 32))
	body := []byte("{ \"request_id\": \"same-request\" }\n")
	last := ""
	calls := 0
	c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		nonce := r.Header.Get("X-Voucher-Admin-Nonce")
		if _, err := uuid.Parse(nonce); err != nil || nonce == last {
			t.Fatal("nonce")
		}
		last = nonce
		if r.URL.String() != "http://127.0.0.1:4311/internal/admin/suppliers/12/check" || string(raw) != string(body) {
			t.Fatal("target/body changed")
		}
		if r.Header.Get("X-Voucher-Admin-Signature") != signature(c.secret, r.Method, r.URL.Path, r.Header.Get("X-Voucher-Admin-Timestamp"), nonce, raw) {
			t.Fatal("signature")
		}
		return response(200, `{"ok":true,"data":{"available":true}}`), nil
	})
	for range 2 {
		if _, err := c.Forward(context.Background(), "POST", "/suppliers/12/check", body); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"https://evil.test", "/suppliers/../check", "/suppliers/1?url=evil", "/suppliers/%31/check", "/suppliers/0/check"} {
		if _, e := c.Forward(context.Background(), "POST", path, body); e == nil || e.Status != 400 {
			t.Fatal(path, e)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestFailClosedResponsesAndLimits(t *testing.T) {
	cases := []struct {
		status     int
		body, code string
	}{
		{409, `{"ok":false,"error_code":"version_conflict","message":"SECRET"}`, "version_conflict"},
		{400, `{"ok":false,"error_code":"invalid_input"}`, "invalid_input"},
		{401, `{"ok":false,"error_code":"unauthorized","secret":"SECRET"}`, "invalid_supplier_response"},
		{200, `<html>SECRET</html>`, "invalid_supplier_response"},
		{200, `{"ok":true}`, "invalid_supplier_response"},
		{302, `{"ok":true,"data":{}}`, "invalid_supplier_response"},
		{200, strings.Repeat("s", 2<<20+1), "invalid_supplier_response"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			c := New("http://127.0.0.1:4311", "key", strings.Repeat("s", 32))
			c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil })
			_, e := c.Forward(context.Background(), "GET", "/suppliers", nil)
			if e == nil || e.Code != tc.code || strings.Contains(e.Error(), "SECRET") {
				t.Fatal(e)
			}
		})
	}
	for _, base := range []string{"", "http://user:pass@localhost", "http://localhost/redirect", "http://localhost?target=x"} {
		c := New(base, "key", strings.Repeat("s", 32))
		if _, e := c.Forward(context.Background(), "GET", "/suppliers", nil); e == nil || e.Code != "admin_not_configured" {
			t.Fatal(base, e)
		}
	}
}
func TestCancellationTimeoutNoRetryAndRedirectPolicy(t *testing.T) {
	c := New("http://127.0.0.1:4311", "key", strings.Repeat("s", 32))
	transport := c.http.Transport.(*http.Transport)
	if !transport.DisableKeepAlives || transport.Proxy != nil {
		t.Fatal("transport may retry or use proxy")
	}
	c.http.Timeout = 10 * time.Millisecond
	calls := 0
	c.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	if _, e := c.Forward(context.Background(), "GET", "/suppliers", nil); e == nil || e.Status != 503 {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.Forward(ctx, "GET", "/suppliers", nil); e == nil || e.Status != 503 {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal("retried", calls)
	}
	if c.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirect allowed")
	}
}

func TestInvalidBodiesNeverReachNode(t *testing.T) {
	c := New("http://127.0.0.1:4311", "key", strings.Repeat("s", 32))
	c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("invalid body forwarded"); return nil, nil })
	for _, input := range []struct {
		method string
		body   []byte
	}{
		{"GET", []byte("{}")}, {"POST", []byte("{")}, {"POST", []byte(strings.Repeat(" ", 65537))},
	} {
		_, e := c.Forward(context.Background(), input.method, "/suppliers", input.body)
		if e == nil || e.Status != 400 {
			t.Fatal(e)
		}
	}
}
