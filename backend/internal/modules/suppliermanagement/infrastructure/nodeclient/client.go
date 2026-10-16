package nodeclient

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	suppliercontract "github.com/dujiao-next/internal/modules/suppliermanagement/contract"
	"github.com/google/uuid"
)

type Client struct {
	base, key, secret string
	http              *http.Client
	configured        bool
}

func New(base, key, secret string) *Client {
	u, err := url.Parse(base)
	valid := err == nil && u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/") && strings.TrimSpace(key) != "" && len(secret) >= 32
	// Dedicated transport avoids proxy environment credential exposure and
	// reused-connection retries of signed requests. Each caller gets one attempt.
	transport := &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	return &Client{base: strings.TrimRight(base, "/"), key: key, secret: secret, configured: valid, http: &http.Client{
		Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func signature(secret, method, path, timestamp, nonce string, body []byte) string {
	digest := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.Join([]string{method, path, timestamp, nonce, hex.EncodeToString(digest[:])}, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}
func allowed(method, path string) bool {
	for _, r := range suppliercontract.Routes {
		if r.Method != method {
			continue
		}
		want := strings.Split(r.Path, "/")
		got := strings.Split(path, "/")
		if len(want) != len(got) {
			continue
		}
		match := true
		for i := range want {
			if want[i] == ":id" {
				match = match && suppliercontract.ValidID(got[i])
			} else {
				match = match && want[i] == got[i]
			}
		}
		if match {
			return true
		}
	}
	return false
}

var safeCodes = map[int]map[string]bool{
	400: {"invalid_input": true, "invalid_endpoint": true, "query_not_supported": true},
	404: {"not_found": true},
	409: {"version_conflict": true, "idempotency_conflict": true, "active_lease": true, "reconciliation_unverified": true},
	503: {"supplier_unavailable": true, "admin_not_configured": true, "routing_unavailable": true},
}

func fail(status int, code string) (json.RawMessage, *suppliercontract.Failure) {
	return nil, &suppliercontract.Failure{Status: status, Code: code}
}
func (c *Client) Forward(ctx context.Context, method, path string, body []byte) (json.RawMessage, *suppliercontract.Failure) {
	if !allowed(method, path) {
		return fail(400, "invalid_endpoint")
	}
	if len(body) > suppliercontract.MaxRequestBytes || (method == "GET" && len(body) > 0) || (method != "GET" && !json.Valid(body)) {
		return fail(400, "invalid_input")
	}
	if !c.configured {
		return fail(503, "admin_not_configured")
	}
	pathname := "/internal/admin" + path
	req, err := http.NewRequestWithContext(ctx, method, c.base+pathname, bytes.NewReader(body))
	if err != nil {
		return fail(503, "supplier_unavailable")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Voucher-Admin-Key", c.key)
	req.Header.Set("X-Voucher-Admin-Timestamp", timestamp)
	req.Header.Set("X-Voucher-Admin-Nonce", nonce)
	req.Header.Set("X-Voucher-Admin-Signature", signature(c.secret, method, pathname, timestamp, nonce, body))
	res, err := c.http.Do(req)
	if err != nil {
		return fail(503, "supplier_unavailable")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, suppliercontract.MaxResponseBytes+1))
	if err != nil || len(raw) > suppliercontract.MaxResponseBytes {
		return fail(503, "invalid_supplier_response")
	}
	var envelope struct {
		OK   *bool           `json:"ok"`
		Data json.RawMessage `json:"data"`
		Code string          `json:"error_code"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.OK == nil {
		return fail(503, "invalid_supplier_response")
	}
	if res.StatusCode == 200 && *envelope.OK && len(envelope.Data) > 0 {
		return envelope.Data, nil
	}
	if !*envelope.OK && safeCodes[res.StatusCode][envelope.Code] {
		return fail(res.StatusCode, envelope.Code)
	}
	return fail(503, "invalid_supplier_response")
}
