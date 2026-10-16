package tapecommercebootstrap

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/dujiao-next/internal/modules/tapecommerce/domain"
	"io"
	"net/http"
	"strconv"
)

// DemoHandler is an authenticated loopback bridge for the Node sandbox. The
// buyer ID is trusted session context supplied by Node; this must not be exposed
// to browsers or mounted in the production router.
func DemoHandler(d *Demo, secret string) http.Handler {
	mux := http.NewServeMux()
	type buyerRequest struct {
		BuyerID   uint   `json:"buyerId"`
		ProductID uint   `json:"productId"`
		OrderID   uint   `json:"orderId"`
		Payer     string `json:"payer"`
		InvoiceID string `json:"invoiceId"`
		TxHash    string `json:"txHash"`
	}
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, r *http.Request) {
		p, e := d.Catalog(r.Context())
		demoWrite(w, map[string]any{"products": p}, e)
	})
	mux.HandleFunc("POST /v1/buyers/new", func(w http.ResponseWriter, r *http.Request) {
		id, e := d.NewBuyer(r.Context())
		demoWrite(w, map[string]any{"buyerId": id}, e)
	})
	mux.HandleFunc("POST /v1/orders/create", func(w http.ResponseWriter, r *http.Request) {
		var q buyerRequest
		if e := demoDecode(w, r, &q); e != nil {
			demoWrite(w, nil, e)
			return
		}
		v, e := d.CreateOrder(r.Context(), q.BuyerID, q.ProductID, q.Payer)
		demoWrite(w, v, e)
	})
	mux.HandleFunc("POST /v1/quotes/prepare", func(w http.ResponseWriter, r *http.Request) {
		var q buyerRequest
		if e := demoDecode(w, r, &q); e != nil {
			demoWrite(w, nil, e)
			return
		}
		v, e := d.Bridge.Prepare(r.Context(), q.BuyerID, q.OrderID, q.Payer)
		demoWrite(w, v, e)
	})
	mux.HandleFunc("POST /v1/quotes/persist", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			BuyerID uint `json:"buyerId"`
			OrderID uint `json:"orderId"`
			Invoice struct {
				InvoiceID  string `json:"invoiceId"`
				TenantHash string `json:"tenantHash"`
				Payer      string `json:"payer"`
				Merchant   string `json:"merchant"`
				AmountWei  string `json:"amountWei"`
				Deadline   string `json:"deadline"`
			} `json:"invoice"`
			FXSnapshot domain.FXSnapshot `json:"fxSnapshot"`
			Signature  string            `json:"signature"`
		}
		if e := demoDecode(w, r, &q); e != nil {
			demoWrite(w, nil, e)
			return
		}
		deadline, e := strconv.ParseUint(q.Invoice.Deadline, 10, 64)
		if e != nil || strconv.FormatUint(deadline, 10) != q.Invoice.Deadline {
			demoWrite(w, nil, domain.ErrInvalid)
			return
		}
		i := domain.Invoice{InvoiceID: q.Invoice.InvoiceID, ShopID: d.Bridge.ShopID, OrderID: q.OrderID, ChainID: domain.ChainID, Checkout: d.Bridge.Checkout, TenantHash: q.Invoice.TenantHash, Payer: q.Invoice.Payer, Merchant: q.Invoice.Merchant, AmountWei: q.Invoice.AmountWei, Deadline: deadline, FXSnapshot: q.FXSnapshot}
		v, e := d.Bridge.Persist(r.Context(), q.BuyerID, i, q.Signature)
		demoWrite(w, v, e)
	})
	mux.HandleFunc("POST /v1/invoices/submit", func(w http.ResponseWriter, r *http.Request) {
		var q buyerRequest
		if e := demoDecode(w, r, &q); e != nil {
			demoWrite(w, nil, e)
			return
		}
		v, e := d.Bridge.Submit(r.Context(), q.BuyerID, q.InvoiceID, q.TxHash)
		demoWrite(w, v, e)
	})
	mux.HandleFunc("POST /v1/invoices/status", func(w http.ResponseWriter, r *http.Request) {
		var q buyerRequest
		if e := demoDecode(w, r, &q); e != nil {
			demoWrite(w, nil, e)
			return
		}
		v, e := d.Bridge.Status(r.Context(), q.BuyerID, q.InvoiceID)
		demoWrite(w, v, e)
	})
	mux.HandleFunc("POST /v1/invoices/claim", func(w http.ResponseWriter, r *http.Request) {
		var q buyerRequest
		if e := demoDecode(w, r, &q); e != nil {
			demoWrite(w, nil, e)
			return
		}
		v, e := d.Bridge.ClaimDelivery(r.Context(), q.BuyerID, q.InvoiceID)
		demoWrite(w, v, e)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		if r.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "server_only"})
			return
		}
		if e := d.Sweep(); e != nil {
			demoWrite(w, nil, e)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func demoDecode(w http.ResponseWriter, r *http.Request, target any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil {
		return domain.ErrInvalid
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return domain.ErrInvalid
	}
	return nil
}
func demoWrite(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		code := "internal_error"
		status := http.StatusInternalServerError
		for _, known := range []error{domain.ErrInvalid, domain.ErrNotFound, domain.ErrConflict, domain.ErrReview, domain.ErrDisabled, domain.ErrPending, domain.ErrLease, domain.ErrRPC} {
			if errors.Is(err, known) {
				code = known.Error()
				status = http.StatusConflict
				if errors.Is(err, domain.ErrNotFound) {
					status = http.StatusNotFound
				}
				if errors.Is(err, domain.ErrInvalid) {
					status = http.StatusBadRequest
				}
				break
			}
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
