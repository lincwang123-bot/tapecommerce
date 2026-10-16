package tapecommercebootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dujiao-next/internal/config"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
)

var ErrSigner = errors.New("tapecommerce_signer_unavailable")
var ErrExpired = errors.New("tapecommerce_quote_expired_new_order")
var ErrIneligible = errors.New("tapecommerce_order_ineligible")

type SignerHealth struct {
	Ready       bool   `json:"ready"`
	Mode        string `json:"mode"`
	ChainID     uint64 `json:"chainId"`
	Tenant      string `json:"tenant"`
	Checkout    string `json:"checkout"`
	Merchant    string `json:"merchant"`
	QuoteSigner string `json:"quoteSigner"`
}
type SignRequest struct {
	OrderID     uint   `json:"orderId"`
	Tenant      string `json:"tenant"`
	Buyer       string `json:"buyer"`
	Currency    string `json:"currency"`
	AmountUSDT  string `json:"amountUSDT"`
	Status      string `json:"status"`
	Payer       string `json:"payer"`
	ExpiresAtMS int64  `json:"expiresAtMs"`
}
type SignedInvoice struct {
	InvoiceID  string `json:"invoiceId"`
	TenantHash string `json:"tenantHash"`
	Payer      string `json:"payer"`
	Merchant   string `json:"merchant"`
	AmountWei  string `json:"amountWei"`
	Deadline   string `json:"deadline"`
}
type SigningDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           uint64 `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}
type TypedField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var invoiceFields = []TypedField{{"invoiceId", "bytes32"}, {"tenantHash", "bytes32"}, {"payer", "address"}, {"merchant", "address"}, {"amountWei", "uint256"}, {"deadline", "uint64"}}

type SignedQuote struct {
	Domain     SigningDomain           `json:"domain"`
	Types      map[string][]TypedField `json:"types"`
	Invoice    SignedInvoice           `json:"invoice"`
	Signature  string                  `json:"signature"`
	FXSnapshot domain.FXSnapshot       `json:"fxSnapshot"`
}
type QuoteSigner interface {
	Health(context.Context) (SignerHealth, error)
	Sign(context.Context, SignRequest) (SignedQuote, error)
}
type HTTPSigner struct {
	base, secret string
	client       *http.Client
}

func NewHTTPSigner(c config.TapeCommerceConfig) (*HTTPSigner, error) {
	u, e := url.Parse(c.SignerURL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return nil, ErrSigner
	}
	st, e := os.Stat(c.SignerSecretFile)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		return nil, ErrSigner
	}
	secret, e := os.ReadFile(c.SignerSecretFile)
	if e != nil {
		return nil, ErrSigner
	}
	value := strings.TrimSpace(string(secret))
	if len(value) < 32 || strings.ContainsAny(value, "\r\n") {
		return nil, ErrSigner
	}
	return &HTTPSigner{base: strings.TrimSuffix(c.SignerURL, "/"), secret: value, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *HTTPSigner) call(ctx context.Context, path string, input, output any) error {
	var body []byte
	method := http.MethodGet
	if input != nil {
		var e error
		body, e = json.Marshal(input)
		if e != nil {
			return ErrSigner
		}
		method = http.MethodPost
	}
	req, e := http.NewRequestWithContext(ctx, method, s.base+path, bytes.NewReader(body))
	if e != nil {
		return ErrSigner
	}
	req.Header.Set("Authorization", "Bearer "+s.secret)
	req.Header.Set("Content-Type", "application/json")
	res, e := s.client.Do(req)
	if e != nil {
		return ErrSigner
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return ErrSigner
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(raw) > 65536 {
		return ErrSigner
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(output) != nil {
		return ErrSigner
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return ErrSigner
	}
	return nil
}
func (s *HTTPSigner) Health(ctx context.Context) (SignerHealth, error) {
	var v SignerHealth
	e := s.call(ctx, "/internal/tapecommerce/health", nil, &v)
	return v, e
}
func (s *HTTPSigner) Sign(ctx context.Context, q SignRequest) (SignedQuote, error) {
	var v SignedQuote
	e := s.call(ctx, "/internal/tapecommerce/quote", q, &v)
	return v, e
}
